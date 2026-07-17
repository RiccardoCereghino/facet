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

## Working from GitHub issues

The workspace core turned out to be the perfect base for a second habit: opening a
throwaway workspace for a single issue, ready to work in seconds. Two features grew
here and became mainstays — inferring an issue's repositories, and generating a
`CLAUDE.md` that hands an agent everything it needs to start.

```sh
facet spawn 67 --repo acme/platform
```

`spawn` reads the issue, works out which repositories it needs, **prints why each
one was chosen, and waits.** On confirmation it creates an issue-linked branch
(`gh issue develop`), clones each repo, and writes a `CLAUDE.md` carrying the issue
body and the durable hazards recorded for its `area/*` labels. Then it stops and
tells you where to work — opening an editor or starting an agent is yours.

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

### Tidying up

`facet issues` lists the ephemeral workspaces. `facet reap` deletes one, and
**refuses** while there are unpushed commits, uncommitted changes, or an open pull
request — the states where deleting would lose work.

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
implies, and what hazards an area carries, are all *data*, read from your
workspaces root:

| File | What it holds |
| --- | --- |
| `.tools/routing.json` | the repo table, the label → repos prior, and the project board |
| `.knowledge/area-*.md` | durable hazards, inlined into a spawned workspace |

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

## Status

Early, but held together by a real test suite. It grew on one person's machine and
is used daily on Windows; the OS-specific parts sit behind build tags, and CI runs
the tests on Linux, macOS and Windows on every change. Treat a first run on a new
platform as worth watching, and please open an issue if something looks off.

## Licence

MIT.
