# Bolted

A password-locked, encrypted Linux dev environment CLI for Mac and Windows (Linux post-MVP). Run normal dev commands prefixed with `bolt` and they execute inside an isolated, encrypted VM.

## Repo layout

| Directory | Purpose |
|---|---|
| `.claude/brainstorm/` | Early ideation, validating ideas, design spikes. Numbered markdown files |
| `specs/` | Detailed, actionable specs (like GitHub issues). Numbered. Agents pick these up to implement |
| `docs/` | Living source of truth for what's IMPLEMENTED. Waku + Fumadocs site |

Each directory has its own `CLAUDE.md` with conventions specific to that area - read it before working there.

## Task runner

`Taskfile.yml` at the root drives common tasks. `task` lists everything (`task docs:dev`, `task docs:build`, etc.).

## Status

Pre-implementation. Brainstorm and specs are landing first; code follows.

## Conventions

- Don't commit unless explicitly asked.
- Match existing file numbering (`NN-title.md`) when adding to `.claude/brainstorm/` or `specs/`.
- Keep design exploration in `.claude/brainstorm/`, actionable plans in `specs/`, post-implementation reference in `docs/`. Don't mix.
- **Commit messages follow Conventional Commits.** `feat:` → minor bump, `fix:` → patch bump, `!:` or `BREAKING CHANGE:` → major bump. Other types (`chore`, `docs`, `style`, `refactor`, `perf`, `test`, `build`, `ci`, `revert`) don't trigger a release on their own but still land in the changelog. The `commit-msg` lefthook hook validates this locally; the `pr-title` workflow validates it in CI.

## Dev-loop tooling

After cloning, one-time setup:

```sh
brew install lefthook gofumpt golangci-lint   # macOS
lefthook install
```

What runs on `git commit`:

- **Go:** `gofumpt -l` (formatting), `golangci-lint run`, `go vet`
- **Docs:** `pnpm exec oxfmt --check` and `pnpm exec oxlint` against staged files
- **commit-msg:** `scripts/check-commit-msg.sh` enforces Conventional Commits

If a hook fails, the commit aborts. Fix the underlying issue (e.g. `gofumpt -w .` or `pnpm --dir docs format`) and retry.

## Release flow

1. Land conventional-commit PRs on `main`. Each merge feeds the release-please bot, which opens (and keeps updating) a single **Release PR** titled `chore: release vX.Y.Z`. That PR bumps `.release-please-manifest.json` and updates `CHANGELOG.md`.
2. When you're ready to ship, **review and merge the Release PR**.
3. Run `task release:tag`. It reads the new version from `.release-please-manifest.json`, creates a **signed** tag (`git tag -s vX.Y.Z`), and pushes it. release-please itself is configured with `skip-github-release: true` precisely so the maintainer can keep signed tags rather than handing tag creation to the GitHub-Actions bot.
4. Pushing the tag fires `.github/workflows/release.yml` which builds the cross-compiled tarballs + SHA256SUMS and drafts a GitHub Release. Review the draft at <https://github.com/dahal/bolted/releases> and click Publish.
