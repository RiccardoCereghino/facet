# Contributing to facet

facet is one binary (`cmd/facet`) over a set of small packages under `internal/`.
It is around 12,000 lines of Go. Everything below is written from what the code
already does — it is a description with reasons attached, not an aspiration.

**Every rule here carries its reason.** A rule nobody can justify is noise with a
linter attached, so if you find one whose reason no longer holds, change the rule
rather than working around it.

## Layout

```
cmd/facet/      the CLI: cobra wiring and the command bodies
internal/       everything else, one package per job
testdata/       fixtures
```

There is no `pkg/`, no handler/service/repository layering, and no dependency
injection container. At this size each `internal/` package already has one clear
job stated in its package doc, and `ls internal/` tells you more than a taxonomy
would. **If a layout needs a diagram to explain, it is the wrong layout for this
repo.**

### `internal/` versus `cmd/facet`

`cmd/facet` holds cobra wiring plus the command body it delegates to: a thin
`newXCmd` that parses flags, and a `runX` that does the work — see `runFile`,
`runPreflight`, `runSpawn`. Anything reusable, or independently testable below
the CLI, lives in `internal/`.

Command bodies staying in `cmd/facet` is deliberate. `runSpawn` is orchestration
glue over `routing`, `render`, `workspace`, `mux` and `ghx`; it is not a unit
anything else would import. Giving it its own package would move the size problem
rather than solve it. **Promote code to `internal/` when a second caller needs it,
not in anticipation of one.**

### Where shared helpers live

When the same idea is written twice, it gets one definition and one owner. Where
that owner lives follows from who depends on whom:

- **Neither caller should own it →  its own small package.** `internal/wait` holds
  the one deadline-poll loop that `internal/lockfile` and `cmd/facet` both need.
  Neither should have to import the other, so it belongs to neither.
- **Test-only support for a package → nested under it.** `internal/gitx/gitxtest`
  holds the shared `gitx.Runner` fake, mirroring `net/http/httptest`. An exported
  non-test helper inside `gitx` itself would compile into every binary that
  imports `gitx`, including the production one, to serve callers who will never
  fake anything.
- **Used at several sites inside one package → unexported, in that package.**
  `internal/mux`'s `tmuxRun`/`tmuxOutput` are the only places that package names
  the tmux executable.

Keep the helper as small as its call sites require. `internal/wait` is one
function with a deadline, an interval and an attempt func — no backoff, no jitter,
no builder. **A configurable framework where three call sites wanted a loop is the
ceremony this layout exists to avoid.**

## Errors

**A refusal prints the fix.** facet spends most of its time declining to do
something — a workspace with unpushed work, a missing credential, an unclean
clone — and a refusal that only names the failure leaves the reader to guess the
remedy. `internal/ghx`'s `Problem` is the shape to copy: it carries what was
checked, what was wanted, what was actually there, and *why the check exists*.

When you cannot use a struct, put the same content in the message. Say what
failed, then say what to run or change. Error strings are lowercase and carry no
trailing punctuation, per Go convention.

**Refuse when you cannot tell.** A probe that errors is not a pass. `facet reap`
refuses to delete a workspace when a git probe fails or a PR lookup errors,
because "I could not check" and "there is nothing to lose" must never produce the
same outcome.

## Package documentation

**One file per package opens with a doc comment stating the package's job and the
reasoning behind its one load-bearing decision.** `gitx` explains why facet shells
out to the git CLI instead of using a pure-Go library; `mirror` explains why
mirrors exist at all; `lockfile` explains its heartbeat. These are the comments
that survive a rewrite of everything under them, and they are where a new reader
should be able to start.

## External tools and how they are faked

facet drives `git`, `gh`, `tmux` and the agent CLI by shelling out. Each has one
package that owns the boundary, and each of those has exactly one place that
names the executable:

| Tool | Package | Seam |
| --- | --- | --- |
| `git` | `internal/gitx` | `gitx.Runner`, a one-method interface |
| `gh` | `internal/ghx` | `ghx.Client` |
| `tmux` | `internal/mux` | `mux.Launcher` |
| the agent CLI | `internal/claudex` | — |

**Fake the narrowest thing that works.** `gitx.Runner` has one method, so faking
all of it is trivial and callers take `gitx.Runner` directly. `ghx.Client` covers
the whole `gh` surface, so a caller needing one or two calls declares its own
package-local interface instead — `internal/workspace` defines `PRLookup` (one
method) and `LiveChecker` rather than faking the full client. Both patterns are
legitimate; the choice is about how much a fake has to know, not about
consistency for its own sake.

**Optional capability goes in its own interface, discovered by type assertion.**
`workspace.LiveRootChecker` and `timeoutRunner` are both optional extensions: an
implementation that does not provide them is simply not asked. This keeps the
required interface small enough to fake in three lines.

**Use the shared fake, and know its boundary.** `gitxtest.Runner` replaced three
one-off `gitx.Runner` fakes that differed only in which call they made fail. If a
new case needs it to differ by more than that, keep a separate fake — a shared
fake harder to read than the ones it replaced is a net loss.

## Tests

**The filesystem is not faked.** Tests run against `t.TempDir()` and real `os`
calls, and where the subject is git, against real repositories. facet's job *is*
manipulating the filesystem, so a fake filesystem would test less than doing it
for real. `internal/mux`'s tests likewise drive a real tmux server on an isolated
`-L <socket>`.

The cost is that some tests are slow and some need a tool installed. That is
accepted. What is *not* accepted is a test that silently skips the thing it exists
to check — if a test cannot run, that should be visible.

**There is no `Clock` interface.** Where a test needs a fixed "now", pass it
explicitly: `render.BuildIssueData` takes a `now time.Time` parameter and is
testable against a fixed time without any abstraction. Introduce an interface when
a seam is needed, not to look idiomatic.

## Platform policy

The primary platforms are the ones facet is actually run on. Secondary platforms
are covered in CI all the same.

- **A red secondary platform on the default branch puts the repo in
  `patch-only`:** bugfixes and the platform fix, no new features, until it is
  green again.
- **"CI is green" means the default branch**, not a branch with a passing run. A
  green pull request does not lift a freeze; merging the fix does. This
  distinction is the entire content of the rule.
- **A linter or `govulncheck` going red is not this rule.** That rule is about
  platform coverage. A quality gate reporting a real finding on a platform already
  covered is just work: fix it, or scope it explicitly with a reason.
- **Never weaken a gate to make it pass.** No `continue-on-error`, no
  `if: always()`, no `|| true`. If a gate is wrong, delete it and say why; if it
  is right, fix what it caught. A gate that cannot fail is worse than no gate,
  because it reads as coverage.

Platform-specific code uses the `_unix.go` / `_windows.go` build-tag split —
see `internal/fslink` and `internal/gitx` — rather than `runtime.GOOS` branches
inside a shared function, so the platform-specific path is visible in the file
listing.

## CI

`.github/workflows/ci.yml` runs in two tiers:

1. **Linux**, on every push and every pull request including drafts: formatting,
   build, vet, race tests, lint, vulnerability check, and the privacy guard.
2. **macOS and Windows**, gated behind Linux passing, and skipped on draft pull
   requests and on pull requests not targeting `main`. Build and test only — every
   other gate is platform-independent by construction, so running it three times
   buys nothing.

The tiering is for feedback quality, not billing: this is a public repository, so
GitHub-hosted runners cost nothing on any OS. A formatting slip should fail in
under a minute rather than after three platform runs finish in parallel, and work
in progress should not queue three runners to learn the same thing twice.

**Any new gate must be demonstrated failing on a deliberate violation, then
green,** with both runs linked from the pull request that adds it. A gate never
observed red is not a gate — it is an assumption with a green checkmark.

## Privacy

**This repository is public.** Everything pushed is world-readable: code,
comments, commit messages, pull request bodies.

- **No private repository names, no foreign issue references, no
  employer-internal terms** — in source, docs, workflows, or commit messages.
- A bare `#123` is an issue in *this* repository and is fine. A qualified
  `something#123` or `owner/repo#123` is a coordinate in somewhere else, means
  nothing to an outside reader, and must not appear.
- **Keep the reasoning, drop the coordinate.** A comment explaining *why* a check
  exists is valuable; the ticket number it came from is not, outside the tracker
  that holds it. Rewrite it as a self-contained explanation.
- **Name identifiers for what they are, not who they serve.** An exported name
  that encodes an audience keeps leaking as the audience changes.
- **Describe a leak's shape, never an instance of it.** This applies with
  particular force to test fixtures for the guards below: use synthetic examples,
  built so the fixture cannot trip the repo-wide scan it lives beside.

Two guards in `internal/privacy` enforce this mechanically, each failing
distinctly so a red build says what it caught:

- an organisation-name scan, whose word list is supplied out of band (a
  `FACET_DENYLIST` secret or a gitignored `.denylist`) because committing the
  list of forbidden names would be the disclosure it prevents. It fails rather
  than skips in CI when unconfigured: a guard that passes having checked nothing
  is a hole.
- a pattern scan for the qualified-issue-reference shape above, which the word
  list structurally cannot catch because it is a pattern rather than a fixed word.
  It needs no secret, so it also runs for an outside contributor.

## Working on a change

```sh
go build ./... && go vet ./... && gofmt -l . && go test -race ./...
```

Then the linter, pinned to the version CI uses — see the `golangci-lint-action`
step in `.github/workflows/ci.yml`.

**Keep behaviour changes out of refactors.** A diff that both moves code and
changes what it does cannot be reviewed as either. If a refactor turns up a
semantic problem, land the refactor and open an issue for the problem.

Commit messages say what changed and why the change is right, in the body. Put
closing keywords (`Closes #123`) on a line of their own referencing exactly one
issue: they are parsed greedily, and a sentence written to *disclaim* a closure
will still perform it.
