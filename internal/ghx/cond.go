// Conditional reads: asking GitHub for something we already have, and being
// charged nothing when it has not changed.
//
// WHY THIS EXISTS, AND WHY IT IS REST RATHER THAN GRAPHQL. The two APIs have
// SEPARATE budgets, and only one of them can be asked conditionally:
//
//	GraphQL  5000 POINTS/hour   billed on the nodes a query COULD return,
//	                            in full, every time, for ever. No ETags.
//	REST     5000 REQUESTS/hour a 304 Not Modified costs ZERO.
//
// Measured, on this account, minutes apart: `graphql 0/5000` beside
// `core 4928/5000` -- an exhausted derivation next to an untouched budget. And
// a conditional repeat: remaining 4921 -> 4921, delta 0.
//
// That matters because the walk's real workload is a REPEAT. A loop reading the
// same tree every five minutes re-reads a tree that mostly did not change, and
// GraphQL has no way to say so: it bills the full price for the same answer
// twelve times an hour. Conditional REST bills it once.
//
// THE FAILURE THIS PREVENTS IS NOT SLOWNESS. A GraphQL budget that runs out
// does not error the walk -- it SHORTENS it. Measured: with 15 points left, a
// walk printed 49 of 160 nodes, exited zero and wrote nothing to stderr, and
// every consumer downstream then filtered a short tree and reported
// confidently on it.
//
// AND A CACHE MUST NEVER SERVE STALE DATA. Cheap and wrong is worse than the
// walk this replaces. Nothing here decides for itself that an entry is still
// good: GitHub decides, by answering 304, and a 200 replaces the entry
// outright. There is no expiry to tune and no staleness window to be wrong
// about.

package ghx

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// condResult is one conditional read.
type condResult struct {
	Body []byte
	// NotModified says GitHub answered 304, so Body came from the cache and
	// the read cost nothing.
	NotModified bool
	// More says the response was paginated and this is only the first page.
	// The caller must fall back to a full read rather than treat it as whole.
	More bool
}

// cacheEntry is what is kept on disk per request path.
type cacheEntry struct {
	ETag string `json:"etag"`
	Body []byte `json:"body"`
}

// cacheDir is where conditional entries live. A cache that cannot be created
// is not an error: every read still works, it just costs what it always did.
//
// FACET_CACHE names it explicitly. That exists because os.UserCacheDir is
// PLATFORM-SPECIFIC and does not honour XDG_CACHE_HOME on macOS -- so without
// it a test trying to isolate itself silently writes to the real cache, which
// is how a test suite comes to depend on a developer's machine state. It is
// also the switch for pointing a run at a scratch cache on purpose.
func cacheDir() (string, bool) {
	base := os.Getenv("FACET_CACHE")
	if base == "" {
		var err error
		base, err = os.UserCacheDir()
		if err != nil {
			return "", false
		}
	}
	dir := filepath.Join(base, "facet", "rest")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", false
	}
	return dir, true
}

func cachePath(requestPath string) (string, bool) {
	dir, ok := cacheDir()
	if !ok {
		return "", false
	}
	sum := sha256.Sum256([]byte(requestPath))
	return filepath.Join(dir, hex.EncodeToString(sum[:])+".json"), true
}

func readCache(requestPath string) (cacheEntry, bool) {
	p, ok := cachePath(requestPath)
	if !ok {
		return cacheEntry{}, false
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		return cacheEntry{}, false
	}
	var e cacheEntry
	if err := json.Unmarshal(raw, &e); err != nil || e.ETag == "" {
		return cacheEntry{}, false
	}
	return e, true
}

func writeCache(requestPath string, e cacheEntry) {
	p, ok := cachePath(requestPath)
	if !ok {
		return
	}
	raw, err := json.Marshal(e)
	if err != nil {
		return
	}
	// Write beside and rename, so a walk interrupted mid-write cannot leave a
	// half-written entry that later reads as a valid one.
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return
	}
	_ = os.Rename(tmp, p)
}

// condGet performs a conditional GET through gh and returns the body, whether
// it came free, and whether more pages exist.
//
// `gh api -i` is what makes this expressible: WITHOUT it, gh exits 1 on a 304
// with only "gh: HTTP 304" on stderr, and the response is indistinguishable
// from a failure. With it, gh exits 0 and the status line and headers are
// readable. Measured both ways.
func condGet(requestPath string) (condResult, error) {
	cached, haveCache := readCache(requestPath)

	args := []string{"api", "-i", requestPath}
	if haveCache {
		args = append(args, "-H", "If-None-Match: "+cached.ETag)
	}
	cmd := exec.Command("gh", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	runErr := cmd.Run()

	// THE EXIT CODE IS NOT THE ANSWER HERE. gh exits NON-ZERO on 304 -- the
	// very outcome this function exists to produce -- while still printing the
	// full status line and headers. So the response decides, and the exit code
	// is consulted only when the response says nothing.
	//
	// This was measured wrongly first, and the mistake is worth naming: the
	// probe that reported exit 0 was piped through `head`, so it read HEAD's
	// exit code rather than gh's. A verification behind a pipe reports on the
	// pipe.
	status, headers, body := splitResponse(stdout.Bytes())
	if status == 0 {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" && runErr != nil {
			msg = runErr.Error()
		}
		if msg == "" {
			msg = "no HTTP status in the response"
		}
		return condResult{}, fmt.Errorf("gh api -i %s: %s", requestPath, msg)
	}
	switch {
	case status == 304 && haveCache:
		return condResult{Body: cached.Body, NotModified: true}, nil
	case status == 304:
		// A 304 with nothing to serve it from. Never observed, and if it ever
		// happens the honest answer is that this read did not answer.
		return condResult{}, fmt.Errorf("%s: 304 Not Modified with no cached copy to serve", requestPath)
	case status < 200 || status > 299:
		return condResult{}, fmt.Errorf("%s: HTTP %d", requestPath, status)
	}

	if etag := headers["etag"]; etag != "" {
		writeCache(requestPath, cacheEntry{ETag: etag, Body: body})
	}
	return condResult{Body: body, More: hasNextPage(headers["link"])}, nil
}

// splitResponse pulls the status code, the headers and the body out of what
// `gh api -i` prints. gh may print more than one header block when it follows
// a redirect, so the LAST status line is the one that answered.
func splitResponse(raw []byte) (status int, headers map[string]string, body []byte) {
	headers = map[string]string{}
	rest := raw
	for {
		sep := []byte("\r\n\r\n")
		i := bytes.Index(rest, sep)
		if i < 0 {
			sep = []byte("\n\n")
			i = bytes.Index(rest, sep)
		}
		if i < 0 {
			break
		}
		block := rest[:i]
		remainder := rest[i+len(sep):]
		if !bytes.HasPrefix(block, []byte("HTTP/")) {
			break
		}
		status, headers = parseHeaderBlock(block)
		rest = remainder
		if !bytes.HasPrefix(rest, []byte("HTTP/")) {
			break
		}
	}
	return status, headers, rest
}

func parseHeaderBlock(block []byte) (int, map[string]string) {
	headers := map[string]string{}
	status := 0
	for i, line := range strings.Split(strings.ReplaceAll(string(block), "\r\n", "\n"), "\n") {
		if i == 0 {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				status, _ = strconv.Atoi(fields[1])
			}
			continue
		}
		name, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		headers[strings.ToLower(strings.TrimSpace(name))] = strings.TrimSpace(value)
	}
	return status, headers
}

// hasNextPage reads GitHub's Link header. A page-one answer read as the whole
// answer is the same defect class as a truncated connection: it looks complete.
func hasNextPage(link string) bool {
	return strings.Contains(link, `rel="next"`)
}
