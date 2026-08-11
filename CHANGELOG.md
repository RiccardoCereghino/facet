# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project aims to
follow [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- **`facet tree orphans --repo owner/name [--json]` answers "which issues hang
  under nothing?"** Every other `tree` command reads *downward* from an issue
  you name, so none of them could: an issue with no parent is by definition not
  below any node you could pass in. The only way to ask was to list a
  repository's open issues and check each one's parent by hand — which is how
  **nine unparented issues in one repository went unnoticed** until someone
  thought to look.

  **It is a question, not a verdict.** An unparented issue is a valid issue and
  plenty are deliberately outside a hierarchy, so **finding some is exit 0**.
  **Exit 2 means it could not look** — a repository could not be read, or none
  was named — since silence about a repository nobody could list would
  otherwise read as "nothing unparented there", and the report names it rather
  than dropping it. **There is no exit 1**: this verb has no *I looked, and here
  is what is wrong* answer, because finding orphans is exit 0.

  One GraphQL query per hundred issues rather than one per issue, and it
  deliberately does not fetch labels: GitHub bills a query for the nodes it
  *could* return and connections multiply, so `labels(first: 30)` on 100 issues
  is 3,000 possible nodes against a 5,000-an-hour budget. Measured against a
  per-issue oracle over 27 open issues: identical sets, 1 query instead of 27.

- **`facet tree labels` asserts that every routed repository DEFINES the labels
  the structure declares.** `wire` records the level it enforced by applying a
  label; a repository that never defined that label cannot be given it — so the
  edge landed, the level did not, and **the command exited 0.** The tree gained
  a node whose level nothing can read, and `tree doctor` can only check a level
  it can see.

  Nothing asserted the parity, and the failure was invisible because it is a
  **warning inside a successful command**. Measured on the setup that produced
  the issue: **9 of 14 routed repositories** were missing at least one label
  they can actually be asked for, and each looked fine when checked alone.

  With no `--repo` it sweeps **every repository in the routing file**, because
  the ones that are short are the ones nobody has touched recently — a check
  aimed at the repository in front of you is the check that already missed it.
  `--create` closes the gap, copying the colour and description from a routed
  repository that already has the label.

  **The required set is read from the routing file, never a list inside facet,
  and it is PER REPOSITORY.** A label declared on a repo-scoped shape —
  `{"repo": "stele", "label": "type/seat"}` — is reachable in that repository
  and nowhere else, so requiring it everywhere would report a gap the structure
  itself says can never be used, and `--create` would then define a label no
  wire there could ever apply. `Structure.Labels()` remains the *recognition*
  set (is this one of ours?); `Structure.LabelsFor(repo)` is the *requirement*
  set (could a wire here ever need this?), and the two are documented against
  each other.

  Exit codes are `tree doctor`'s: `0` full parity, `1` something missing, `2`
  no repository could be read. A gap found is `1` even alongside an unreadable
  repository — there is a real finding, and the unchecked repositories are
  named in the message.

### Changed

- **`facet tree doctor` now says whether it looked.** It exited `1` for *I read
  the tree and here are its defects* and `1` for *I never read anything* — a
  malformed reference, an issue that does not exist, an HTTP error. Three
  different answers, one exit code, so no caller could tell a finding from a
  failure.

  ```
  0  looked, and the tree is clean
  1  looked, and here are the defects        (unchanged)
  2  could NOT look
  ```

  **Only the failure path moved**, so anything treating non-zero as "not clean"
  is unaffected. Every other command still exits `1` on any error.

  The alternative a caller was left with is the reason this is facet's to fix
  rather than theirs to work around: **a classifier built on another tool's
  prose is one release note away from silently answering the wrong thing.**
  `argano`'s console had to declare "exit 1 from `facet tree doctor` means
  findings", which then reads a 404 as a finding. `gad hold --fleet --check`
  already answers this way — `1` while held, `2` when unreadable.

- **`tree wire` creates a declared label the repository lacks, and FAILS when
  the level still cannot be recorded.** It used to print

  ```
  WARNING: the edge is wired but type/work was not applied to …: 'type/work' not found
  wired … under …
  ```

  and exit 0. **A command that reports success while leaving the tree
  unlabelled is the defect underneath the defect.** It now asks whether the
  label exists — structurally, by reading the repository's labels, never by
  matching gh's sentence — creates it if it does not, and retries. If the level
  still cannot be recorded the command exits non-zero, **with every line about
  what did happen printed first and the error saying in as many words that the
  edge IS wired**. That wording is the answer to the objection the old code
  recorded: that a non-zero exit here "reads as nothing happened".

- **A tree is read conditionally, so re-reading an unchanged one costs nothing.**
  The walk asked GitHub for a node's children, then asked again for each child —
  hundreds of sequential requests, **84% of the wall clock spent waiting** with
  the CPU idle. Now the reads of a level run together, and every one of them is
  conditional.

  **THE BILL, NOT THE CLOCK, TURNED OUT TO BE THE BINDING CONSTRAINT — and it
  points the opposite way to the obvious fix.** GitHub charges a GraphQL query
  for the nodes it *could* return rather than the ones it does, against a
  separate 5,000-points-an-hour budget, and possible nodes **multiply down
  nesting levels**. A single query nested four rungs deep was measured at ~1,790
  points to return 89 nodes, and a whole walk that way at **4,651 points — 93% of
  the hour, for one walk.** Faster, and unaffordable.

  **The two APIs have separate budgets and only one can be asked conditionally.**
  GraphQL has no conditional-request mechanism at all, so it bills the same
  answer in full every time, for ever. A REST request that answers **304 Not
  Modified costs zero**. So the reads moved to REST: `sub_issues` returns whole
  child objects, which is everything a walk needs about a child, and it carries
  an ETag.

  Measured on a 160-node tree. **One GraphQL point per walk remains and is not
  none** — an issue's *parent* has no REST endpoint at all, so the root's climb
  stays GraphQL. Every per-node read moved:

  | | requests | GraphQL | wall |
  | --- | --- | --- | --- |
  | before | ~160 GraphQL | ~3,360 points | 60.8s |
  | cold | 161 REST | **1 point** | 4.7s |
  | **immediately repeated, unchanged** | **0 REST** | **1 point** | 4.6s |

  `deps ready` over the same tree went from **91.9s to 6.6s**, reporting an
  identical ready set, with its dependency reads gathered together and each
  distinct blocker resolved once instead of once per edge.

  **A walk that runs out of budget does not fail, it SHORTENS.** Measured: with
  15 points left, the walk printed 49 of 160 nodes, exited zero and wrote nothing
  to stderr — and every consumer downstream then filters a short tree and reports
  confidently on it. That is why the budget is treated as a correctness property
  here rather than a courtesy.

  **The cache cannot serve stale data**, because it never decides freshness
  itself: GitHub decides by answering 304, and a 200 replaces the entry outright.
  There is no expiry to tune and no staleness window to get wrong. An entry
  carrying no ETag is unusable and is ignored rather than served.

  **The traversal is untouched.** The reading-ahead wraps the source rather than
  replacing the walk, so order, cycle handling, the depth limit and every error
  message stay where they were — and a differential test walks the same fixture
  both ways and requires the reported trees to be identical.

### Added

- **A grouping nobody is working can be placed in the tree, without inventing a
  node for a worker who does not exist.** Two rungs' worth of shapes may now
  share one rung and permit different things below them, via `childMustBe` on an
  accepted shape. The case it was built for: a rung under the root holding
  either a live record of someone working, or a grouping filed for later — where
  the first may hold work directly and the second must have its work bundled
  first, because bundling is the whole reason it was filed. Position cannot tell
  those apart, since position is exactly what they share.

  `childMustBe` may only name a rung the children could already occupy;
  anything else is refused when the routing file loads, so a narrowing removes a
  candidate and never introduces one. A report names the candidates the node was
  **actually judged against**, recorded at walk time rather than re-derived —
  re-deriving them from the parent's rung names expectations the node was never
  judged by, the same class of mistake as deriving them from depth.

### Changed

- **A label on an accepted shape is a matcher, not only a record.** With a
  `titlePattern` beside it the two are alternatives, either sufficient. **With
  no `titlePattern`, the label is the test** — previously such a shape was read
  as "anything, recorded as this", so it admitted everything, and on a skippable
  rung that meant the rung silently absorbed the one below it. Shapes carrying
  both a repo and a title pattern are unaffected.

- **A refusal from `tree wire` now offers labelling as a remedy**, alongside
  re-parenting and retitling. Most issues carry no title convention, so for them
  the printed fix could not be performed at all — and a refusal whose only
  suggested remedy is impossible teaches the reader to force the edge instead.

- **`tree` reads and writes GitHub's sub-issue graph** — `wire`, `show`,
  `list`, `status`, `doctor`, across repositories. **The hierarchy is optional
  and stays optional:** an issue with no parent is a valid issue, `spawn` never
  asks about one, and no command here is a precondition of another. `wire`
  establishes the parent's depth by climbing to the root before judging
  anything — a child's level only means something relative to its parent's —
  and it climbs rather than descends because the child→parent direction is the
  immediately consistent one. An unreadable parent refuses rather than
  proceeding: skipping the check is how a wrong edge gets written by the tool
  meant to prevent it. It also reads the previous parent before writing, since
  an issue has exactly one parent and the write is silently a *move* otherwise.

  Every `wire` prints both tiers and states that the **child's own** governs. A
  parent's is an at-a-glance worst case for the grouping and is never
  inherited; saying so at the edge is what stops it being re-derived as an
  obvious improvement, given that an edge quietly moving merge authority would
  look exactly like filing.

- **`structure` in the routing file declares what levels a tree should have.**
  Levels are matched by position, may list alternative accepted shapes, and may
  be skippable — a real hierarchy has a rung that is sometimes not needed, and
  forcing a placeholder in to satisfy a schema is worse than allowing the gap.
  Skipping stops at the first required rung, which is what catches a node filed
  straight under the root with the rung above it missing. **Without the block,
  no shape is checked at all** — not leniently — because which shape is right is
  the adopter's contract rather than facet's. `doctor` still reports cycles,
  unreadable nodes and a closed parent with open children, which are wrong on
  any tree's own terms, and it says explicitly when shape went unchecked so
  silence about it does not read as a clean bill of health.

- **`comment list|last|post|edit`, filtered by kind.** `last --kind plan` is
  the point: where a decision is revised by posting it again, the newest one is
  what binds. A kind is a named regexp in the routing file's `commentKinds` —
  facet knows some comments have kinds, never which ones — and `--grep` needs
  no configuration at all. The match count is always reported, because a loose
  pattern does not error, it silently makes an older comment "the latest". An
  edited comment is flagged as edited.

- **`deps show|check|ready`, for the dependency graph — which is not the issue
  graph.** Blocked-by says what must land first; a parent says what a thing is
  part of. `check` compares the body's declared blockers against the wired
  edges using the same parser that files them, and treats only one direction as
  a defect: declared-and-unwired means the write failed silently at filing and
  the dependency exists as prose nobody schedules from, while wired-and-
  undeclared is ordinary ageing. `ready` reports which open issues below one
  have no open blockers left.

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
