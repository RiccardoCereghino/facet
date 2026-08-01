# Reap's refusal logic — a review

**Issue: facet#83.** Ruled by the sculptor 2026-07-31: *"review the facet reap logic."*
Written after facet#77, facet#80 and facet#78, because a review of `proofLanded` written
before them would have been a review of code that was about to change.

The charge is not that `reap` refuses too little. It is that it was **right about once and
wrong constantly**, and that a guard which cries wolf on the ordinary path teaches the habit
of `--force` — the one flag that can actually destroy unpushed work.

## What was wrong, and what is left

Since auto-delete-on-merge was enabled, every squash-merged workspace tripped

> N unpushed commit(s) — this workspace is their only copy

Squash rewrites the sha and GitHub deletes the remote branch, so the local commit is
reachable from no remote **while its content sits on `main`**. The claim was literally true
and materially false. Four issues have now landed against it:

| issue | what it fixed | what it means for the false alarm |
| --- | --- | --- |
| **#66** | the landing proof: ancestor, then merged-PR-by-commit, then a content check | the false alarm stopped being permanent |
| **#77** | the count was summed per ref, so one commit reachable from three branches read as three | the number in the refusal became true |
| **#80** | the `def == ""` fail-safe had never executed in four audit rounds | the last never-run path in the proof is exercised, and names its own cause |
| **#78** | the content check compared the whole tree, so any sibling merge resurrected the refusal | the clean window stopped being "until someone else merges" |

**What remains is a refusal that fires when something is genuinely at risk**, plus three
honest unknowns (a failed probe, a stale fetch, an unresolvable default branch). That is the
state this review is about. The rest of this document answers the four questions facet#83
poses.

## 1. Can "squash-merged" be derived rather than guessed?

**Yes, it already is, and the cost is lower than it looks.** The derivation is
`gh api repos/<repo>/commits/<sha>/pulls` — the commit→pulls REST endpoint —
asking whether a *merged* pull request ever contained this exact sha.

Three properties make it cheap:

- **A clean workspace makes zero calls.** `inspectClone` runs a fast-path
  `rev-list --count HEAD --branches --not --remotes` first; the landing proof only runs when
  that is non-zero. The common case never reaches the network.
- **One call per candidate *ref*, not per commit.** Refs whose commits are all reachable from
  some remote are skipped before the lookup. A squash-merged workspace is one ref, so one call.
- **Measured at ~0.4s per call** (three runs against `RiccardoCereghino/facet`, 0.41 / 0.40 /
  0.43 real). Against a teardown that already fetches from origin, this is not the expensive part.

**It fails closed, and that is checked rather than assumed.** `MergedPRForSHA` returning an
error, or `nil`, both yield `false` from `proofLanded` — the commit stays counted as unpushed
and the workspace is held. A missing `CommitPRLookup` implementation likewise degrades to the
ancestor check alone. There is no path on which a failed derivation clears a blocker.

**One trap is already recorded and must not be re-discovered:** `gh pr list --search <sha>`
does **not** index pull requests by commit sha and can return nothing for a PR that merged
minutes earlier. The commit→pulls endpoint is the only query that answers this.

**Verdict: no change.** The derivation is sound, cheap, and fails in the right direction. What
was wrong was never the derivation — it was checks (c)'s scope (#78) and the arithmetic on its
result (#77).

## 2. What the refusal should say when it cannot tell

This is where the review found the thing worth writing down, because facet#80 added a rung to
a ladder nobody had described. There are now **three** ways `reap` reports a thing it could
not fully establish, and they are not interchangeable:

| rung | example | behaviour |
| --- | --- | --- |
| **`Unverifiable`** | `git status` failed; a clone dir that is not a repo | **blocks on its own**, even on an otherwise clean workspace |
| **disclaimer** | `FetchStale`, `DefaultBranchUnresolved` | **never blocks**; appended to the line it degraded, and only when that line exists |
| **`Notes()`** | `SquashLanded` | does not block and is not a doubt; said so silence is not read as "nothing to report" |

**The rule that decides the rung — and the one this review adds to the file:**

> A failure that makes it impossible to know *whether there is anything to lose* is
> `Unverifiable` and blocks by itself. A failure that leaves the count computable but **less
> trustworthy** is a disclaimer on that count, and must be silent when the count is zero.

The distinction is not stylistic; getting it wrong in either direction is a real defect.
`DefaultBranchUnresolved` as an `Unverifiable` entry would have made **every clean workspace
on a non-`main`-default repo unreapable** — a new permanent false alarm introduced by the fix
for an old one. And `Unverifiable` demoted to a disclaimer would let a git hiccup become
silent data loss, which is the property `Blockers()`'s own doc comment exists to state.

**Verdict: no code change; the rule is now written down.** Every future unknown gets placed by
asking which of the two it is.

## 3. Should `--force` exist at all?

**An escape hatch must exist.** A guard with no override is not safer — it just moves the
operator to `rm -rf`, which has no guard, no confirmation, and no record of what was
overridden. That is not the question.

The question is whether **one flag meaning "ignore every check"** is reviewable. It is not.
`Blockers()` produces eight distinct categories:

| category | what forcing past it costs |
| --- | --- |
| unpushed commits | **irreversible** — the workspace is the only copy |
| stash entries | **irreversible** — no push would ever carry them |
| uncommitted changes | **irreversible** |
| a clone that could not be inspected | **unknown, possibly irreversible** |
| a pull-request state that could not be read | premature at worst |
| an open pull request | premature; nothing is lost |
| a live multiplexer session | annoyance; nothing is lost |
| a pane or process rooted in the tree | corruption of a *running* process, not data loss |

`--force` collapses all eight into one word. From a shell history, `facet reap --force` is
indistinguishable between "a tmux pane was still attached" and "four commits of hand-resolved
merge conflict were the only copy". **A flag that cannot be reviewed after the fact is exactly
the shape that should not be the documented workaround for a common false alarm** — and it was:
the lab's own guidance grew a three-line paragraph explaining that `--force` is *not* a
different operation from the destructive thing the guard named, and that the correct answer is
to make the refusal untrue instead.

**Recommendation: replace the bare flag with a named override — `--force <condition>[,…]` —
so the operator must name the risk they are accepting, and so the shell history records it.
No bare form; a bare `--force` retained as "all conditions" would preserve the entire defect
under a longer name.**

**This review does not implement it, deliberately.** It is a breaking interface change that
reaches the fleet's teardown scripts and habits, and it is worth its own issue, its own
migration note, and its own audit. Filing it is the action item this section produces.

**One thing that is now true and was not:** with #66, #77, #78 and #80 landed, the false alarm
that *drove* people to `--force` is gone. The flag can be narrowed on its merits rather than
in the middle of an incident.

## 4. The invocation trap

`facet reap <name>` answered, measured before this change:

```
facet: unknown command "iss-nonexistent" for "facet reap"
```

`reap` took `--path` only, and `cobra.NoArgs` rejected the positional. **The error names the
wrong problem** — it reads as "reap is broken" rather than "reap wants a flag" — and it has
cost time more than once. It had been written up as a trap in prose, which is where a CLI puts
what it has decided to live with.

**Fixed in this change.** A positional is accepted as a path, and falls back to
`<workspaces root>/<name>` when it is a bare name and no such path exists, because a workspace
is referred to by name far more often than by path. `--path` still works and is unchanged.

**`--path` together with a positional is an error, not a precedence rule.** Two arguments that
disagree about which directory to delete must not have a silent winner.

## What this review deliberately did not touch

- **What counts as unpushed work in general.** Out of scope by facet#83.
- **`facet sync`'s never-pull rule.** Out of scope, and the premise behind it — *a clone is the
  only copy of any branch nobody pushed* — is correct and is not what was under review.
- **Whether `reap` should offer to do the recovery itself** (pruning a merged local branch,
  say). It refuses and explains; making a guard also perform mutations is a larger question
  than the one asked.

## Action items

1. **The `--force` narrowing** (section 3) is filed as **facet#94**: named conditions, no bare
   form, with the migration cost stated as an acceptance criterion.
2. Nothing else. Sections 1, 2 and 4 are closed by this document and the change it accompanies.
