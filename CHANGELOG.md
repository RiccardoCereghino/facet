# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project aims to
follow [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

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

[Unreleased]: https://github.com/RiccardoCereghino/facet/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/RiccardoCereghino/facet/releases/tag/v0.1.0
