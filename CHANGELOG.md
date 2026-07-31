# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project aims to
follow [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- **`spawn` records who a workspace belongs to, in `.seat` and `.scope`.**
  `--seat` is now required and names the seat the workspace is created for; it
  is written to `.seat` at the workspace root, one line. `.scope` lists the
  issues the workspace legitimately covers, one `owner/repo#n` per line — the
  spawned issue always, plus any `--scope owner/repo#n` given, because one
  worker regularly carries a coherent group of issues rather than exactly one,
  which neither the manifest's single issue number nor the branch name can
  express.

- **`spawn --seat-issue owner/repo#n` records the issue that describes the
  seat.** Written to `.seat-issue`, one line — the third member of the
  `.seat`/`.scope` family, same spawner-writes rule and same walk-up
  resolution. It names the seat's workload and order, its per-issue tiers, the
  orchestration notes, and the channel it escalates on: everything that is true
  of the *seat* rather than of the work, which previously had nowhere to live.
  Optional, because a workspace without one is ordinary. **Missing is not an
  error; present-but-empty is** — "no seat issue" and "the spawner meant to
  write one and did not" are both defensible readings of an empty file. Reported
  at write time and shown in the plan, so `--dry-run` confirms it before
  anything is created, and `facet scope list` reports it including when absent.

  Both files are written by the command that *creates* the workspace and never
  by whatever works in it afterwards, which is the whole property: an identity a
  thing asserts about itself is not evidence of anything. The mechanism this
  replaces was an environment variable that every session could set to any
  value. The honest ceiling, stated because it should not be oversold: files on
  disk defeat accidents — a stale export, a typo, a command pasted from
  elsewhere — and do not defeat deliberate tampering, since the agent runs as
  the same user. A per-seat credential is the step up and composes with this
  rather than replacing it.

  Both are written and then **read back**, because a write that reports success
  and does not land is this project's most repeated failure mode.

  `facet scope list` prints them and `facet scope add owner/repo#n` extends the
  scope, both resolving the workspace by walking **up** from the working
  directory: the work happens inside a repository subdirectory, and the leaf of
  that path is the repository's name rather than the workspace's.

  A seat name containing `.` is refused with the fix printed. A multiplexer
  target address uses `.` as the pane separator, so such a name addresses a pane
  of a differently-named session and every command aimed at it lands somewhere
  else. This had already been worked around by hand, by renaming a seat.

  Neither file is versioned — they are session state, not recipe — and `facet
  sync` does not touch either one on a workspace that already exists. There is a
  test whose only job is to keep it that way: a workspace that confidently
  reports the wrong owner is worse than one that reports none.

- **`facet preflight`, and a credential gate on `spawn`**: every tool and agent
  session on this machine shares one GitHub credential, and it has been
  invalidated silently before — the failure was found mid-operation, by trying
  to use it. The new command reports whether this machine holds a sound
  credential: logged in and active, the expected account, a token type that
  another `gh auth login` elsewhere cannot rotate away (`gho_` is refused even
  while it works), the scopes facet actually calls, git talking SSH at the
  *host* level, and the push key present and private. The key's *permission*
  half is Unix-only —
  `os.FileMode` cannot represent NTFS ACLs — so on Windows it reports that the
  check **did not run**, on the pass line itself, rather than letting a green
  tick imply a verification that did not happen. `spawn` runs the same checks first, before
  routing and before the issue lookup, so a bad credential is a refusal rather
  than a half-created workspace. Every failure states the incident it exists
  for. There is no skip flag.

  It reads `gh auth status` and nothing else, deliberately: gh reports a
  credential as invalid *without* a valid credential, so the check does not go
  blind during the fault it catches. No token value is ever read, printed or
  stored — only the type prefix — and nothing here calls `gh auth`
  `login`/`logout`/`refresh`.
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

- **BREAKING: `facet spawn` now requires `--seat`.** Every existing invocation
  without it fails, with the reason and the fix printed. Nothing is derived as a
  fallback, deliberately: a workspace's owner is a decision somebody makes, and
  a name nobody chose is a name nobody can be held to. Deriving one from the
  workspace name was considered and dropped — the obvious derivation emits a
  dotted name for any repository whose name contains a dot, which the same
  change refuses, so the fallback would have been a refusal of the tool's own
  output.

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
- **`ls` no longer errors at the workspaces root.** Run against a directory
  with no manifest of its own, it used to fail with `no .workspace.json in
  <dir>` even when that directory held several workspace subdirectories. It
  now falls back to listing every workspace under it with a rolled-up health
  summary, the same way `restore` already walks them.

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
