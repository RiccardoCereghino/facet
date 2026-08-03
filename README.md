```
         /\
        /  \
       / /\ \
      /_/  \_\        f a c e t
      \ \  / /
       \ \/ /         one task, many repositories,
        \  /          one disposable view
         \/
```

# facet

**Task-scoped workspaces over many git repositories.** A workspace is a directory
that assembles several repositories into one view for one task — and because its
whole layout is declared in `.workspace.json`, the directory is regenerable from
the manifest. Nothing about it is precious.

That is the core, and it stands on its own: no GitHub, no issues, no agents — just
a clean way to lay several repositories side by side and rebuild them anywhere. The
issue-driven features further down grew on top of it and became mainstays, but the
workspace is the thing.

```
~/Workspaces/
  delivery/               # a long-lived, topical workspace
    .workspace.json
    platform/             # a clone this workspace owns outright
    infra/
  iss-platform-67-…/      # an ephemeral workspace, one GitHub issue
```

Each entry is either a **clone** the workspace owns outright — its own branch, its
own index, safe from every other workspace — or a **link** into a shared checkout,
where one working tree is visible everywhere at once.

## Why

Several agents, or several people, working several issues at once will fight over
one working tree: one branch, one dirty index. Giving each task its own checkout
fixes that, and costs disk. `facet` makes the checkouts nearly free, and makes the
throwaway ones disposable without losing work.

## Install

```sh
go install github.com/RiccardoCereghino/facet/cmd/facet@latest
```

Requires `git`. The issue features additionally use the [`gh`](https://cli.github.com)
CLI. `facet` shells out to both, so it inherits your existing credentials, SSH agent
and `gh` accounts, and never handles a token itself.

## The core: workspaces

Everything here works with plain git repositories and needs nothing else.

```sh
facet new delivery --clone platform=git@github.com:acme/platform.git \
                   --clone infra=git@github.com:acme/infra.git
facet sync                 # idempotently rebuild; never touches an existing clone
facet ls                   # what is here, and is it healthy
facet restore              # a fresh machine: rebuild every workspace
```

`new` scaffolds the manifest and its entries; `add` and `rm` adjust them later.
`sync` makes the directory match the manifest and is safe to run at any time — it
creates what is missing and leaves what already exists alone. On a fresh machine,
`restore` walks every workspace and brings them all back from their manifests.
Run `ls` from a directory with no manifest of its own that holds workspace
subdirectories — the workspaces root itself — and it lists every workspace
under it with a rolled-up health summary, instead of erroring.

## Working from GitHub issues

The workspace core turned out to be the perfect base for a second habit: opening a
throwaway workspace for a single issue, ready to work in seconds. Two features grew
here and became mainstays — inferring an issue's repositories, and generating a
`CLAUDE.md` that hands an agent everything it needs to start.

```sh
facet spawn 67 --repo acme/platform --seat w-platform-67
```

`spawn` reads the issue, works out which repositories it needs, **prints why each
one was chosen, and waits.** On confirmation it creates an issue-linked branch
(`gh issue develop`), clones each repo, and writes a `CLAUDE.md` carrying the issue
body and the durable hazards recorded for its `area/*` labels. Then it stops and
tells you where to work — opening an editor or starting an agent is yours.

### Who the workspace belongs to

`--seat` is required, and names whoever the workspace is being created for. It
goes in `.seat` at the workspace root, one line. The issues the workspace
legitimately covers go in `.scope`, one `owner/repo#n` per line — the spawned
issue always, plus any `--scope owner/repo#n` you pass, because one worker
regularly carries a coherent group of issues rather than exactly one.

`--seat-issue owner/repo#n` is optional and names the issue that describes the
**seat** rather than the work — its workload and order, its per-issue tiers, the
orchestration notes, and the channel it escalates on. It goes in `.seat-issue`,
one line. A workspace without one is ordinary: every workspace created before the
file existed has none, and so does one an operator drives directly.

All three are written here rather than by whatever works in the workspace
afterwards, and none is versioned — they are session state.

Both are written **here**, by the thing creating the workspace, and never by
whatever works in it afterwards. That is the entire point: an identity a thing
asserts about itself is not evidence of anything, so attribution has to be
derived from something the subject did not write. Be clear about the ceiling —
this defeats accidents (a stale environment variable, a typo, a command pasted
from another workspace) and it does not defeat deliberate tampering, because the
agent runs as the same user and can rewrite the file. The step up is a per-seat
credential, which composes with this rather than replacing it: the file becomes
the claim, and the credential becomes the proof.

Neither file is versioned — they are session state, not recipe — and **`facet
sync` never touches either one**, on a workspace that already exists. They are
facts about the moment of creation, and a workspace that confidently reports the
wrong owner is worse than one that reports none.

`facet scope list` prints both, found by walking **up** from the working
directory so it works from inside a repository subdirectory. `facet scope add
owner/repo#n` records an issue handed over after the workspace already existed;
it is additive and safe to repeat.

```sh
facet spawn 67 --repo acme/platform --seat w-platform-67 \
  --scope acme/platform#68 --scope acme/tools#12
```

With no pane to put it in, one agent launch is opt-in: a Remote Control session.
With a `spawn` block in `.tools/routing.json` (`{"spawn": {"rc": true}}`), spawn
runs `claude --remote-control` in the home clone once the workspace is ready, so
the session is reachable over Anthropic's relay independent of the tailnet.
`--claude` turns it on for one invocation and `--claude=false` off. It runs last
and is never fatal: if `claude` is missing or not signed in, spawn warns and
leaves the ready workspace for you to open yourself.

Starting claude *here* takes over this terminal, which is why it stays off by
default. In a multiplexer pane it costs nothing, so `--attach` runs it by
default — see below.

```
acme/platform#67  Rehearse a database restore: nothing has ever been restored
  labels: P0-critical, area/backups, blocked

repos to clone, and why:
  platform    home; label:area/backups          [home, gets the branch]
  infra       blocked-by:acme/infra#41; label:area/backups
```

**Labels cannot decide which repositories an issue needs.** A label describes a
topic, and the same topic label gets used in several repos. The decisive evidence
is in the issue body: `owner/repo#n` cross-references, `Blocked by` lines, and —
for issues filed through a form — an explicit "Repos in scope" field. The issue
above is labelled `area/backups` with no Terraform label, and still cannot be
closed without a change in another repository. So the inference is always shown,
never silently trusted, and correctable with `--clone` / `--add` / `--rm`.
`--dry-run` prints it and creates nothing.

### Moving the issue on a project board

A GitHub issue has no "in progress" state — it is open or closed. "In progress" is
an option on the **Status** field of a Projects v2 board, and it belongs to the
board *item*, not to the issue. So give `.tools/routing.json` a board to drive, and
`facet spawn` puts the issue on it and sets the field once the workspace is real:

```json
"project": { "owner": "acme", "number": 4, "statusField": "Status", "onSpawn": "In progress" }
```

The board is named, never by node ID: `PVTSSF_lADOD…` is stable but unreadable, and
would rot in a config file without anyone noticing. `facet` resolves the names on
each spawn, matching case-insensitively, and reports the transition:

```
+ project acme/4: Status = In progress
```

Both fields are optional and both are shown by `--dry-run` before anything happens.
Omit `project` and no board is touched. A board that has been renamed, or a `gh`
missing the `project` scope, **warns and does not fail the spawn** — the clones,
the branch and the `CLAUDE.md` are the point, and a complete workspace is never
stranded by GitHub Projects being briefly uncooperative.

### The confirmed repo set is written back

`facet spawn` prints its inference and waits for you. That answer is worth keeping:
on confirmation it records the confirmed repos in the issue's **Repos in scope**
section, so the next spawn reads a decision (`scope-field`) instead of repeating a
guess — and an issue never filed through a form finally declares what it touches.

```
+ issue body: Repos in scope = platform, infra
```

Rewriting someone's issue body is unforgiving, so the rewrite is timid: the
neighbouring sections come back byte for byte, an existing heading keeps the level
its author chose, an empty set writes nothing, and a body that already says the
right thing is left alone — spawning twice does not churn the issue's history. The
body is re-read immediately before the write, because several agents work the same
issues and the copy fetched at the top of `spawn` is minutes old by then.
`--no-writeback` opts out.

### Filing an issue that the board can see

```sh
facet file --repo acme/platform \
  --title "gateway: last_login_at is never written" \
  --label P1-high --label area/security --label complexity/2 --label env/dev \
  --repos platform,gateway --body-file issue.md
```

`facet file` searches for a duplicate before it creates one — concurrent sessions
file into the same repository, and closed issues count, because refiling something
you decided against is the expensive kind of duplicate. Then it checks the title and
the labels against the `conventions` block, reporting **every** violation at once,
so a single filing tells you everything it needs rather than one rule at a time:

```json
"conventions": {
  "titlePattern": "^[^:\\n]{2,60}: .+",
  "requireOneOf": {
    "priority":   ["P0-critical", "P1-high", "P2-medium", "P3-low"],
    "complexity": ["complexity/1", "complexity/2", "complexity/3"]
  },
  "requirePrefix": { "area": "area/" }
}
```

facet knows that *some* labels are required, never which ones. Omit the block and
nothing is enforced. `--repos` is recorded in the body, so the first spawn of that
issue is exact.

### Opening the workspace

`facet attach` opens a tmux session for the workspace: one pane, the agent,
rooted at the home clone. One session per issue, so `tmux list-sessions`
becomes the dashboard of what is running. Inside an existing tmux session it
adds the workspace as a new window instead, because sessions do not nest —
being moved out of the session you are typing in is never a default. Pass
`--switch` when you do want to be moved.

`facet spawn --attach` runs the same path immediately after setup. Without
`--attach` `spawn` just prints where to work and leaves opening a session to
you. `--mux wt` selects the Windows Terminal fallback (a plain new tab, no
session persistence).

#### What the pane runs

By default the pane starts `claude` with Remote Control, so the session is
reachable from anywhere over Anthropic's relay rather than only from this host.
Its session URL is printed back here once the pane has it — the CLI writes it
only into its own pane, and reading it off a background window by hand is the
one step you would otherwise still be doing manually.

| flags | the pane runs |
| --- | --- |
| *(default)* | `$SHELL -lc "claude --remote-control=<workspace>; exec $SHELL -il"` |
| `--remote=false` | `$SHELL -lc "claude; exec $SHELL -il"` |
| `--claude=false` | `$SHELL -lc "exec $SHELL -il"` — a plain login shell |
| `--claude=false --remote=…` | as `--claude=false`; `--remote` only says *how* to launch, never *whether* |

The session name is attached as `--remote-control=<name>`, never as a positional
argument: the value is optional, so `--remote-control <name> "<prompt>"` is
ambiguous the moment a prompt follows it.

Two properties worth knowing:

- **The pane outlives the agent.** On exit you land in an interactive login
  shell in the same directory rather than losing the window and its scrollback,
  so restarting the agent is `↑`, not rebuilding the window.
- **The window keeps the name facet gave it.** An agent writes its own terminal
  title within seconds, and tmux's `automatic-rename` would copy that over the
  window name — after which `-t <session>:<name>` targets nothing. Both rename
  options are turned off on the windows facet creates.

`FACET_AGENT` overrides all of this: when it is set the pane runs
`$SHELL -lc "$FACET_AGENT; exec $SHELL -il"` and neither `--claude` nor
`--remote` applies. It predates these flags, so it is not made to lose to one of
their defaults.

(`--rc` and `--no-rc`, the names the first Remote Control launch shipped under,
still work as deprecated aliases for `--claude --remote` and `--claude=false`.)

The layout is built inline by facet and needs no configuration. To customise
it — say, adding a shell pane split, a status pane, or a different focus —
drop an executable script at `.tools/issue-layout.sh`; it receives the session
name, home clone, workspace, issue number, agent executable, and agent
arguments, and is expected to leave a session of that name ready to attach.
A non-zero exit warns and falls back to the built-in layout.

### Tidying up

`facet issues` lists the ephemeral workspaces. `facet reap` deletes one, and
**refuses** while there are unpushed commits, uncommitted changes, an open pull
request, a live multiplexer session, or a tmux pane or process still rooted in
the workspace — the states where deleting would lose work, or delete a
directory out from under something still running in it.

## Issue hierarchies, if you want one

GitHub lets an issue be a sub-issue of another, across repositories. `facet
tree` reads and writes that graph:

```sh
facet tree wire   owner/repo#121 --parent owner/other#72
facet tree show   owner/repo#46
facet tree list   owner/repo#46 --level seat
facet tree status owner/repo#46
facet tree doctor owner/repo#46
```

**None of this is required to use facet, and none of it is assumed anywhere
else.** `facet spawn` never asks whether an issue has a parent; an issue with
no parent is a valid issue rather than a degraded one; and no command here is a
precondition of another — `show` works on a tree facet never built. That is
deliberate. Somebody adopting facet may want workspaces and none of the rest.

So the levels a tree *ought* to have are declared in the routing file, beside
the label rules that are already data:

```json
"structure": {
  "levels": [
    { "name": "programme" },
    { "name": "record", "requiresChildren": true,
      "accepts": [{ "repo": "notes", "titlePattern": "^record: " }] },
    { "name": "bundle", "optional": true },
    { "name": "task" }
  ]
}
```

A level with no `accepts` admits anything. `optional` lets a tree skip a rung —
a bundle of one is just the task, and forcing a placeholder in to satisfy a
schema is worse than allowing the gap — and skipping stops at the first
required rung, which is what catches a task filed straight under the programme
with the record missing.

**Without a `structure` block, `doctor` checks no shape at all.** It still
reports cycles, unreadable nodes, and a closed parent with open children, which
are wrong on any tree's own terms, and it says when shape went unchecked rather
than letting silence read as approval.

`wire` prints both tiers on every edge, and states that the child's own governs
— a parent's complexity is an at-a-glance worst case for the grouping, never
inherited. An edge that quietly changed who may merge something would look
exactly like filing, which is the whole reason it is said out loud.

### Dependencies are a different graph

Blocked-by says what must land first. A parent says what a thing is part of.
Neither substitutes for the other, so they have separate commands:

```sh
facet deps show  owner/repo#75   # both directions
facet deps check owner/repo#75   # declared in the body vs actually wired
facet deps ready owner/repo#46   # what below this could be started now
```

`check` exists because `facet file` creates these edges once, at filing, and
every failure there is a warning rather than a refusal — the issue is already
filed. Nothing looked afterwards. Only one direction is a defect: a blocker
**declared and not wired** means the write failed silently and the dependency
is prose nobody schedules from, while one wired and not mentioned in the body
is ordinary ageing.

### Comments, by kind

```sh
facet comment last owner/repo#282 --kind plan
```

Where a decision is revised by posting it again, the newest one is what binds,
and finding it by eye in a long thread is how the wrong revision gets acted on.
A kind is a named regexp in the routing file's `commentKinds`; `--grep` needs
no configuration. Anchor the pattern to the whole heading rather than to a
word — `^#{1,6} +Plan\b`, not `^#+ .*plan` — because a loose one does not
error, it silently returns an older comment as the latest. The match count is
always printed so that is visible.

## The credential preflight

Everything facet does on the forge rides one `gh` credential, shared with
every other tool and agent session on the machine. That credential has been
invalidated before with no announcement, and the fault was found mid-operation
rather than up front.

`facet preflight` checks the credential surface before it is needed:

```
$ facet preflight
host        github.com
account     RiccardoCereghino
token type  ghp_ (value never read)
scopes      read:org, repo, workflow
protocol    ssh
source      /Users/cerre/.config/gh/hosts.yml

✓ credential preflight passed
```

It checks that gh is logged in and the account is active and the expected one;
that the token is **not** a `gho_` OAuth-App token — GitHub issues one per (app,
user) pair, so any other `gh auth login` anywhere silently invalidates it, which
is what happened; that `repo`, `read:org` and `workflow` are all granted; that
git's **host-level** protocol is `ssh` (`gh auth login --with-token` flips it to
https silently, and `gh config get git_protocol` reports the *global* default, so
it is the wrong thing to read); and that `~/.ssh/id_ed25519` exists and is not
group- or world-readable, since that key authenticates every push.

**The permission half of the key check is Unix-only, and says so on Windows.**
Go's `os.FileMode` does not represent NTFS ACLs, so a mode test there would pass
or fail for reasons unrelated to who can actually read the key. Rather than ship
a meaningless test — or drop the check to get a green tick — Windows prints a
`!` line stating the check **did not run**, tells you to verify it with
`icacls`, and the pass line itself reads *"passed — but 1 check did NOT run on
this platform"*. **A green preflight must never be readable as "your key
permissions were verified" when they were not.** The existence half is
platform-neutral and runs everywhere.

`facet spawn` runs the same checks first, before routing and before the issue
lookup, so a bad credential produces a refusal rather than a half-created
workspace. **There is no skip flag** — a gate with an escape hatch is not a gate.

Two properties are deliberate. It reads `gh auth status` and nothing else,
because gh reports a credential as invalid *without* a valid credential: the
check does not go blind during the fault it exists to catch. And it never reads,
prints or stores a token value — only the type prefix — and never calls
`gh auth login`, `logout` or `refresh`, because a repair path that can leave the
machine with no credential is the same class of fault as the outage.

Both are also true of the failure path, and `preflight` works with no workspaces
root and a broken config: the thing you reach for when the lab is wrong cannot
require the lab to be right.

## Timestamps

`facet date` is the fleet's one canonical timestamp source (facet#71). Two
real incidents came from hand-rolling one: a two-minute gap between an
escalation and its answer was misread as two hours (a UTC timestamp compared
against a local clock reading), and an L3 escalation watch was left dead for
74 minutes by a watermark built from a local time and silently 74 minutes
ahead of the real clock -- a check that can never fire reads exactly like one
that found nothing.

```
$ facet date
2026-07-30T15:00:00Z

$ facet date --local
2026-07-30T17:00:00+02:00

$ facet date --check 2026-07-30T16:14:00Z
facet: 2026-07-30T16:14:00Z is 1h14m0s in the future (now is 2026-07-30T15:00:00Z) -- refusing a timestamp this far ahead of now
```

The default is RFC 3339 in UTC -- the format GitHub's API returns and compares
against, so the output can be piped straight into a `jq` comparison against
`createdAt`/`mergedAt` with no conversion. `--local` renders the same instant
with its local offset, for a human. **Every rendering carries an explicit UTC
or offset marker**; there is no mode that produces a bare local time with no
offset, because that bare shape is exactly what caused both incidents above.

`--check <timestamp>` answers the second incident directly: it refuses (exit
1) a timestamp unexpectedly still ahead of now, past a few seconds of ordinary
clock skew, and reports by how much. A watch built with this before trusting
its own watermark would have refused rather than gone dark.

## Mirrors make the clones cheap

`facet sync --via-mirror`, and every `facet spawn`, clones from a bare mirror under
`~/Projects/.mirrors/` rather than from the forge. Git hardlinks `.git/objects`
when cloning from a local path, so a second workspace over the same repository
costs its working tree and **zero bytes of objects**. Each clone keeps an
independent `.git`, and `origin` is repointed at the forge, so pushes and fetches
reach GitHub.

Hardlinks rather than `--shared`/alternates: an inode outlives the mirror's
directory entry, so repacking or garbage-collecting either side is safe. And
correctness never depends on a mirror being fresh — a failed mirror fetch is a
warning, because every clone's origin is the forge.

## Design

**`facet` knows nothing about your organisation.** Which repositories a label
implies, what hazards an area carries, and the multiplexer layout are all
*data*, read from your workspaces root:

| File | What it holds |
| --- | --- |
| `.tools/routing.json` | the repo table, the label → repos prior, and the project board |
| `.knowledge/area-*.md` | durable hazards, inlined into a spawned workspace |
| `.tools/issue-layout.sh` | optional override script for the tmux layout |

A knowledge fragment holds **invariants only** — things true about a system
whichever issue you happen to be working on. Status, phase and "as of" notes belong
in the long-lived workspace named by the fragment's `source_workspace`. Keeping the
two apart is the only thing that stops a fragment quietly becoming a second, staler
source of truth. The loader rejects a `kind:` other than `invariants`.

**`facet` shells out to `git` and `gh`** rather than using a pure-Go git library.
It needs Git-LFS, credential helpers, SSH-agent auth and — decisively — the
`--local` hardlink clone, none of which `go-git` provides. And `gh` already holds
working, multi-account authentication.

**The manifest format is frozen.** `facet` reproduces one byte for byte, inserting
only the empty schema keys a file predates. It never reformats or reorders one, so
it can be adopted by an existing, versioned set of workspaces without churn.

## Guarantees, and the tests that hold them

- **An existing clone is never touched** by `sync` — no pull, no reset, no clean.
  It may hold the only copy of unpushed work.
- **`--prune` deletes only links**, never a clone. On Windows a link is a junction,
  which reports as `ModeIrregular` rather than `ModeSymlink` — as does every other
  reparse point. `facet` reads the reparse tag, so it cannot mistake a plain
  directory for a link and delete it.
- **`reap` counts commits reachable from any local branch and from no remote.**
  Unlike `@{u}..HEAD`, that also catches a branch which was never pushed at all —
  the branch most easily lost. It also steps out of the working directory before
  deleting, because Windows will not remove a directory a process is sitting in.
- **`reap` never touches the mirror.** Deleting a hardlinked object drops that
  name; the mirror keeps its own.
- **`reap` also refuses while a tmux pane or process is rooted in the
  workspace**, in any session — not only the one named after it, which a
  `link-window`ed pane would slip past. `tmux list-panes -a` and `lsof -d cwd`
  are both a convenience layered on the git-based checks: a missing tool
  degrades to "nothing found", never a false refusal.

## Status

Early, but held together by a real test suite. It grew on one person's machine and
is used daily on Windows; the OS-specific parts sit behind build tags, and CI runs
the tests on Linux, macOS and Windows on every change. Treat a first run on a new
platform as worth watching, and please open an issue if something looks off.

## Licence

MIT.
