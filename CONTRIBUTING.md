# Contributing to facet

The house style — layout, errors, boundaries, tests, platform policy, CI, and
the privacy rules this public repository is written to — lives in
**[`docs/CODE.md`](docs/CODE.md)**. Every rule there carries the reason it
exists; if you find one whose reason no longer holds, change the rule rather
than working around it.

Two things worth knowing before you open a pull request:

- **Run `make check`.** It is every gate CI's first tier runs, target for
  target, so a green run locally means a green run there.
- **This repository is public.** No private repository names and no issue
  references an outside reader cannot follow. Keep the reasoning, drop the
  coordinate — `docs/CODE.md` section 8 has the detail.
