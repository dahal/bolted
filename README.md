# Bolted

**A password-locked, encrypted Linux dev environment for Mac and Windows.** Run your normal dev commands prefixed with `bolt` - `bolt git clone …`, `bolt npm install`, `bolt gh auth login`, `bolt kubectl …` - and they execute inside an isolated, encrypted VM. When you lock it, the bytes on disk are ciphertext.

Docs: **[bolted.sh](https://bolted.sh)** · [Quickstart](https://bolted.sh/docs/quickstart) · [Threat model](https://bolted.sh/docs/concepts/encryption)

---

## Why Bolted

If you take supply-chain risk seriously, your laptop is the soft spot. A single `npm install`, `pip install`, `cargo build`, or `brew install` can run arbitrary code as your user - with access to your SSH keys, cloud credentials, browser cookies, source trees, and every other repo you've ever cloned. Lockfile audits and SBOMs help, but they don't stop a postinstall script that has already run.

Bolted moves the dangerous half of your day - package installs, build scripts, dev servers, AI agents writing code, anything that touches third-party code - **into an isolated Linux VM with an encrypted disk**. The isolation runs both ways:

**The VM can't corrupt the host:**

- **Malicious `postinstall` / `build.rs` / `setup.py` scripts** run inside the VM. They cannot reach your host shell, your `~/.ssh`, your keychain, your browser cookies, or repos for other clients.
- **A compromised npm / PyPI / crates package** is sandboxed to one repo's dev container, not your whole machine.

**The host can't corrupt the VM:**

- **An already-compromised host** - a backdoored editor extension, a malicious `brew`-installed CLI, a sideloaded macro, malware from a phishing email - cannot read, modify, or inject code into the source trees, dependencies, or build outputs inside the VM. There is no host-side filesystem mount of the encrypted volume; the host sees one opaque disk image and nothing more.
- **Host-side telemetry, A/V, MDM probes, screen recorders** can't snoop on the source you're working on or the secrets your dev containers hold.
- **A lost or stolen laptop** yields ciphertext. The LUKS volume is opened by a password you set; lose the password, lose the data.

It's the security posture of a remote dev VM, without giving up the ergonomics of working locally. Same UX on macOS (via Lima) and Windows (via WSL2).

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
