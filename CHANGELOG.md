# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project aims to
follow [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- **`spawn` can launch a Remote Control session**: once the workspace is ready,
  `spawn` optionally runs `claude` with Remote Control in the home clone. Remote
  Control rides Anthropic's relay over outbound HTTPS, so an agent stays
  reachable even if the tailnet drops. Off unless enabled — starting it here
  takes over the terminal `spawn` was run from — so a `spawn` block in
  `.tools/routing.json` (`{"spawn": {"rc": true, "sessionNamePrefix": "..."}}`)
  sets the default and `--claude` / `--claude=false` override per invocation.
  The launch runs last and is never fatal: a missing or unauthenticated `claude`
  warns and leaves the ready workspace untouched.
- **`facet attach`, and `spawn --attach`**, open the workspace in tmux: one
  pane, the agent, rooted at the home clone. One session per issue, so
  `tmux list-sessions` is the dashboard of what is running; from inside a
  session it adds a window instead, because sessions do not nest. `--mux wt`
  selects a degraded Windows Terminal fallback, `--mux none` opens nothing, and
  `.tools/issue-layout.sh` overrides the built-in layout. `reap` and `issues`
  now hold back on a live session.
- **The pane launches claude with Remote Control by default**, and prints the
  session URL back once the pane has it — the CLI writes that URL only into its
  own pane, and it is the whole deliverable for reaching a session from
  elsewhere. `--remote=false` runs claude without Remote Control;
  `--claude=false` leaves a plain login shell; `FACET_AGENT` still overrides all
  of it. In a pane this is on by default, where the terminal-seizing launch
  above is not: a pane costs nothing to give away.

### Changed

- **A pane outlives its agent.** Panes run `<agent>; exec $SHELL -il`, so
  quitting the agent drops you into a login shell in the same directory instead
  of closing the window and losing its scrollback.
- **The session name is passed as `--remote-control=<name>`**, not as a
  positional argument. The value is optional, so a positional name is ambiguous
  the moment an initial prompt follows it.

### Fixed

- **A window facet creates keeps the name facet gave it.** `automatic-rename`
  and `allow-rename` are turned off on it: an agent writes its own terminal
  title within seconds of starting, and tmux would copy that over the window
  name, after which `-t <session>:<name>` targets nothing.

### Fixed

- **Lock acquisition no longer fails on a Windows delete-pending race**: when a
  holder releases the lockfile, Windows keeps the name in a delete-pending state
  until the last handle closes, and a second acquirer creating the same name in
  that window gets `ERROR_ACCESS_DENIED` (not "already exists"). `acquire` now
  retries that Windows-only permission error with a short bounded backoff (~2s
  budget); POSIX has no such window and is unchanged. Surfaced by a flaky
  `internal/mirror` Windows CI run.

## [0.1.1] - 2026-07-21

Hardening and correctness, from the first pass of the audit backlog. No command,
flag or manifest change.

### Fixed

- **`reap` fails safe**: every git-probe failure now blocks the reap instead of
  reading as a clean tree — an unverifiable clone, a clone dir that is not a
  repo, detached-HEAD commits, stash entries, and a failed PR lookup.
- **Mirrors are never half-adopted**: a clone lands in a sibling `.incoming` dir
  and is renamed into place only on success, so an interrupted clone can no
  longer look complete to the next `Update` and hardlink corrupt objects into a
  workspace.
- **The mirror lock is heartbeaten**: a live holder re-stamps the lock's mtime
  while its clone/fetch runs, so a peer tells a slow holder from a crashed one
  and waiters no longer abort a healthy clone at a fixed 120s cap.
- **`sync` serialises across processes** with a per-workspace lock (the
  heartbeat logic now lives in `internal/lockfile`, shared with the mirror).
- **Path traversal in `mirror.PathFor`**: the host segment is validated like the
  repo-path segments, so a URL such as `https://../etc/passwd` cannot walk the
  mirror path out of root.
- **Fence-aware heading demotion**: a code fence closes only on a fence of the
  same marker and length, so a crafted issue body can no longer desync the
  renderer and slip a heading out of code to pose as a section of the generated
  `CLAUDE.md`.
- **Cross-references match case-insensitively**: a body writing `acme/gateway`
  resolves against the routing file's `acme/Gateway` instead of silently
  dropping the cross-repo reference.
- Smaller correctness fixes: control chars stripped from the issue title in the
  rendered H1, `--slug` rejected unless lowercase `[a-z0-9-]`, deterministic
  `Hint.Reason` when prefixes share a key, a leading thematic break read as body
  rather than frontmatter, the real `ResolveWorkspace` error reported instead of
  always "not found", and link bootstrap routed through `gitx.Clone`.
- The organisation-name privacy guard **fails closed in CI** and scans every
  text file rather than an extension allowlist.

## [0.1.0] - 2026-07-17

Initial public release.

### Added

- **Workspaces**: manifest-declared (`.workspace.json`) directories that assemble
  several git repositories into one task-scoped view — each entry either a clone
  the workspace owns outright or a link into a shared checkout. Commands: `new`,
  `sync`, `restore`, `ls`, `add`, `rm`.
- **Cheap clones from local mirrors**: `sync --via-mirror` (and every `spawn`)
  clones from a bare mirror using git hardlinks, so a second workspace over the
  same repository costs its working tree and no object bytes.
- **Issue workspaces**: `spawn` reads a GitHub issue, infers the repositories it
  needs from the issue body, shows its reasoning, and on confirmation creates an
  issue-linked branch, clones each repo, and writes a `CLAUDE.md` carrying the
  issue body and the durable hazards recorded for its `area/*` labels.
- **Project boards**: `spawn` can move the issue's board item to a configured
  status, and writes the confirmed repo set back to the issue body.
- **Issue filing**: `file` creates issues that satisfy configurable title and
  label conventions, checking for duplicates first.
- **Reaping**: `issues` lists ephemeral workspaces; `reap` deletes one, refusing
  while there are unpushed commits, uncommitted changes, or an open pull request,
  and never touching the shared mirror.
- **`--version`** flag and `version` subcommand.

### Notes

- Portable by construction (OS-specific code sits behind build tags); CI runs the
  test suite on Linux, macOS, and Windows.

[Unreleased]: https://github.com/RiccardoCereghino/facet/compare/v0.1.1...HEAD
[0.1.1]: https://github.com/RiccardoCereghino/facet/compare/v0.1.0...v0.1.1
[0.1.0]: https://github.com/RiccardoCereghino/facet/releases/tag/v0.1.0
