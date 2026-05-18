# Bolted docs

User-facing documentation site for [Bolted](../). Built with [Waku](https://waku.gg) + [Fumadocs](https://fumadocs.dev). Uses `bun` (see `Taskfile.yml`).

Run from the repo root:

- `task docs:dev` - dev server
- `task docs:build` - production build
- `task docs:check` - TypeScript + MDX check
- `task docs:lint` - oxlint

Content lives in `content/docs/*.mdx`. See `CLAUDE.md` for conventions and file layout.
