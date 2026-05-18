# Bolted

We are seeing more and more supply-chain attacks against developer toolchains. A backdoored dependency. A typo-squatted package. A compromised editor extension. A third-party AI skill, MCP server, or agent plugin with broad shell access. An AI coding assistant acting on a prompt-injected README, issue, or web page. An AI-generated PR pulling in malware the model hallucinated into existence. Any one of them runs arbitrary code as you, the moment you install or invoke it. When that code lands on a developer's laptop, the cleanup is rarely cheap: weeks of incident response, rotated credentials, customers to notify, source leaked, repos held to ransom. Lockfile audits and SBOMs help you trace the damage afterwards. They don't stop the `postinstall` that has already shipped your SSH keys to a server you've never heard of.

**Bolted is a password-locked, encrypted Linux dev environment for Mac and Windows.** Prefix your normal commands with `bolt` - `bolt git clone`, `bolt npm install`, `bolt kubectl …` - and they run inside an isolated VM on an encrypted disk. When you walk away, `bolt lock` turns the volume back into ciphertext. The host can't reach into the VM, and the VM can't reach out to the host: a compromised package gets exactly one repo's dev container, and a compromised host can't touch your source.

If you ship software for a living - or for a team that does - that's a contained blast radius worth installing in five minutes.

**Docs:**

- [About](https://bolted.sh)
- [Quickstart](https://bolted.sh/docs/quickstart)
- [CLI reference](https://bolted.sh/docs/commands)

---

## What it protects

**The VM can't corrupt the host:**

- A malicious `postinstall` / `build.rs` / `setup.py` runs inside the dev container, with no path to your host shell, `~/.ssh`, OS keychain, browser cookies, or other clients' repos.
- A compromised `npm`, `pip`, `cargo`, or Homebrew package is bounded to the one repo's dev container. It cannot pivot to other repos on the same Bolted instance.

**The host can't corrupt the VM:**

- An already-compromised host - a backdoored editor extension, a malicious `brew`-installed CLI, malware sideloaded from a phishing email - cannot read or modify the source, dependencies, or build outputs inside the VM. There is no host-side mount of the encrypted volume; the host sees one opaque disk image and nothing more.
- Host-side telemetry, A/V hooks, MDM probes, and screen recorders can't snoop on the source you're working on or the secrets your dev containers hold.

**At rest:**

- A lost or stolen laptop yields ciphertext. The LUKS2 volume is opened by an Argon2id-derived key from a password you set. Lose the password, lose the data.

Same UX on macOS (via Lima) and Windows (via WSL2).

## How it works

```
┌─────────────────────── your host (macOS / Windows) ───────────────────────┐
│                                                                            │
│   bolt git clone …   ─┐                                                    │
│   bolt npm install   ─┼──►  passthrough router  ──►  ┌──────────────────┐  │
│   bolt dev my-app    ─┘                              │   Linux VM       │  │
│                                                      │  ┌────────────┐  │  │
│   bolt unlock / lock  ────►  LUKS open / close  ────►│  │ /bolted/   │  │  │
│                                                      │  │   repos/   │  │  │
│   localhost:3000  ◄──────────  port forward  ◄───────│  │ (LUKS2)    │  │  │
│                                                      │  └────────────┘  │  │
│                                                      └──────────────────┘  │
└────────────────────────────────────────────────────────────────────────────┘
```

- **Encrypted at rest.** Repo contents live in a LUKS2 volume with an Argon2id-derived key. Locked means locked - the host never holds the key.
- **Multi-repo, multi-container.** Many repos in one Bolted instance, many dev containers running concurrently, port collisions remapped automatically.
- **Passthrough by default.** Any `bolt <something>` that isn't a reserved subcommand is routed into the VM verbatim - no bespoke wrappers around `git`, `gh`, `gcloud`, `kubectl`, language toolchains, or anything else.
- **Toolchain layering.** Minimal VM base + a shareable `bolted.yaml` profile (team-wide pinned tooling) + standard per-repo `devcontainer.json` (project-specific languages). Three layers, no surprises.

Read the full threat model at [bolted.sh/docs/concepts/encryption](https://bolted.sh/docs/concepts/encryption) - including what Bolted explicitly does *not* protect against (host root, keyloggers, weak passwords).

## Install

**macOS:**

```sh
curl -fsSL https://raw.githubusercontent.com/dahal/bolted/main/install.sh | sh
```

**Windows:** grab `bolted-<version>-windows-amd64.zip` from [Releases](https://github.com/dahal/bolted/releases), extract `bolt.exe` onto your `PATH`.

Verify:

```sh
bolt version
```

## Quickstart

```sh
bolt init                                              # create encrypted volume; sets your password
bolt unlock                                            # boot VM, open LUKS, mount /bolted/repos
bolt git clone https://github.com/octocat/Hello-World  # passthrough - runs inside the VM
bolt dev Hello-World                                   # launch dev container, drop into a shell
bolt lock                                              # unmount, wipe key, ciphertext on disk
```

The loop is `unlock` → work → `lock`. The VM keeps running between sessions so the next `unlock` is fast; pass `--stop-vm` to shut it down.

> **No password recovery.** Lose the password, lose the data - that's the point. Put it in a password manager before you forget it.

Full walkthrough at [bolted.sh/docs/quickstart](https://bolted.sh/docs/quickstart).

## CLI tour

Reserved subcommands (Bolted itself):

| Group | Commands |
|---|---|
| **Lifecycle** | `init`, `unlock`, `lock`, `status`, `restart`, `stop`, `rm` |
| **Auth & secrets** | `password`, `keychain`, `trust` |
| **Repos & dev containers** | `ls`, `dev`, `exec`, `shell`, `logs`, `provision` |
| **Networking** | `ports`, `forward`, `unforward` |
| **Portability** | `export`, `import`, `profiles`, `config` |
| **Meta** | `version`, `help`, `completion` |

Everything else is passthrough - it runs verbatim inside the VM:

```sh
bolt git push
bolt npm install
bolt pnpm test
bolt cargo build
bolt gh pr create
bolt gcloud auth login
bolt kubectl get pods
bolt -- ls /bolted/repos     # `--` forces passthrough for names that would shadow reserved cmds
```

Per-command reference: [bolted.sh/docs/commands](https://bolted.sh/docs/commands).

## Status

Pre-release. The CLI surface, encryption flow, passthrough router, multi-repo runtime, and devcontainer trust gate are implemented and tested. VM image builds, profile import/export, and a handful of UX polish items are in flight. See [Releases](https://github.com/dahal/bolted/releases) for the current state.

## For contributors

| Path | What's there |
|---|---|
| [`docs/`](docs/) | Source for [bolted.sh](https://bolted.sh) - living truth for what's implemented. Waku + Fumadocs |
| `cmd/bolt/`, `internal/` | Go source for the `bolt` CLI |
| `vm-image/` | Build pipeline for the minimal Alpine VM image |
| `Taskfile.yml` | Common tasks - `task` lists them |

If you're hacking on Bolted itself, start with [Architecture](https://bolted.sh/docs/developers/architecture) and [Security model](https://bolted.sh/docs/developers/security-model).

## License

TBD - see [Issues](https://github.com/dahal/bolted/issues) for licensing discussion.
