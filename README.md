# Bolted

A password-locked, encrypted Linux dev environment CLI for Mac and Windows. Run normal dev commands prefixed with `bolt` — `bolt git clone …`, `bolt gh auth login`, `bolt gcloud …` — and they execute inside an isolated, encrypted VM. Multiple repos, concurrent dev containers, transparent `localhost` port forwarding.

## Repo layout

- **[`.claude/brainstorm/`](.claude/brainstorm/)** — early ideation, design exploration, validating ideas
- **[`specs/`](specs/)** — actionable implementation specs (the work queue)
- **[`docs/`](docs/)** — living source of truth for what is implemented (Waku + Fumadocs site)
- **`Taskfile.yml`** — common dev tasks. `task` lists everything

Each directory has its own `CLAUDE.md` explaining conventions.

## Status

Pre-implementation. Brainstorm specs are settled enough to read top-to-bottom; first implementation specs land next.
