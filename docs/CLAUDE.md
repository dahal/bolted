# docs

User-facing documentation site. Living source of truth for what Bolted actually does - not what's planned.

## Stack

- **Waku** - React-based framework, single binary build
- **Fumadocs** - docs-specific MDX components, search, navigation
- **Tailwind v4** - styling
- **Bun** - package manager and runtime (see `Taskfile.yml`)

## Where things live

- `content/docs/*.mdx` - all docs pages. File path = URL path. Use frontmatter (`title`, `description`, optionally `icon`).
- `src/pages/(home)/index.tsx` - landing page at `/`.
- `src/lib/shared.ts` - site-wide constants (`appName`, GitHub config).
- `src/lib/layout.shared.tsx` - base layout (nav, GitHub link).
- `src/components/mdx.tsx` - MDX component overrides.
- Generated/build artifacts (`.source/`, `src/pages.gen.ts`, `dist/`) - gitignored. Never commit.

## Common tasks (run from repo root)

- `task docs:dev` - dev server with hot reload
- `task docs:build` - production build
- `task docs:check` - TypeScript + MDX type check
- `task docs:lint` - oxlint

## Conventions

- **Docs describe what is IMPLEMENTED.** Aspirational features live in `specs/`; ideation in `.claude/brainstorm/`.
- When a feature ships, the matching docs page must be added or updated in the same change.
- One concept per page. Cross-link freely.
- Use Fumadocs `<Cards>` / `<Card>` for hub pages, code blocks for examples, tables for option references.
- Keep the home page high-signal: one sentence on what Bolted is + CTA into docs.

## Don't

- Don't add aspirational features here - those belong in `specs/`.
- Don't duplicate brainstorm content; link to it if needed.
- Don't commit generated files (`.source/`, `src/pages.gen.ts`).
- Don't run `npm`/`yarn`/`pnpm` - this project uses `bun` (via `Taskfile.yml`).
