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
