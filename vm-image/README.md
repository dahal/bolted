# vm-image

Layer 1 of Boltedchain stack: the minimal Alpine VM rootfs that
both the Lima (Mac) and WSL2 (Windows) backends boot.

> Layer 1 is "what every Bolted install always has." Anything user- or
> project-specific belongs in `bolted.yaml` (layer 2, spec 15) or the
> per-repo `devcontainer.json` (layer 3, spec 16). Do not bake those tools
> here.

## Baked tools

Exactly six packages, hardcoded in [`Dockerfile`](./Dockerfile):

| Package      | Why it's in the base                            |
| ------------ | ----------------------------------------------- |
| `podman`     | Container runtime; everything else runs inside it |
| `cryptsetup` | LUKS open/close on the encrypted volume         |
| `git`        | Clone repos into the encrypted volume           |
| `openssh`    | Backend exec channel (`bolt exec`, `bolt shell`)    |
| `bash`       | Predictable shell for scripts and exec          |
| `curl`       | Bootstrap downloads (devcontainer CLI, features) |

A small set of podman runtime helpers (`fuse-overlayfs`, `shadow-uidmap`,
`slirp4netns`) is also installed so rootless containers start without further
package work at first boot. These are implementation detail, not user-facing.

## Build

```sh
task vm:build         # from repo root, runs ./mkimage.sh
# or directly:
./mkimage.sh
```

Outputs land in `vm-image/dist/`:

- `bolted-vm-vm-1.0.0-rootfs.tar.gz` - consumed by `wsl --import` on the
  Windows backend.
- `bolted-vm-vm-1.0.0.qcow2` - booted by Lima's Alpine template on the
  Mac backend.

### Build-host requirements

- `docker` (or a Docker-compatible CLI exposed as `docker`). The build
  process produces the rootfs via `docker build` + `docker export`.
- `qemu-img`, for the raw -> qcow2 conversion. macOS: `brew install qemu`.
  Debian/Ubuntu: `apt install qemu-utils`.
- Standard `tar`, `gzip`.

### Useful env vars

| Var           | Default        | Purpose                                  |
| ------------- | -------------- | ---------------------------------------- |
| `VERSION`     | `vm-1.0.0`     | Image tag and artifact filename suffix   |
| `PLATFORM`    | `linux/amd64`  | `linux/arm64` for native Apple Silicon   |
| `RAW_SIZE_MB` | `1024`         | Sparse raw disk size before qcow2 convert |
| `SKIP_WSL`    | unset          | `=1` to skip the WSL rootfs tar          |
| `SKIP_QCOW2`  | unset          | `=1` to skip the Lima qcow2              |

### Publish

```sh
task vm:publish
```

Tags the locally built image as `ghcr.io/dahal/bolted-vm:vm-1.0.0` and
pushes it. Update the org placeholder in `Taskfile.yml` before the first real
publish.

## Adding or upgrading a baked tool

Adding to the base image is intentionally friction-heavy: every change forces
a re-publish and a version bump everywhere the image is consumed. Before you
add a tool, ask whether it belongs in the Bolted profile (spec 15) or a
devcontainer feature instead. The base is for "every Bolted install, always."

### Worked example - adding `rsync`

Suppose `bolt sync` (a future spec) needs `rsync` in every VM. Procedure:

1. **Edit `vm-image/Dockerfile`.** Add `rsync` to the `apk add` list and to
   the sanity-check loop:

   ```Dockerfile
   RUN apk add --no-cache \
           podman \
           cryptsetup \
           git \
           openssh \
           bash \
           curl \
           rsync \
       && rm -rf /var/cache/apk/*

   RUN set -eux; \
       for bin in podman cryptsetup git ssh bash curl rsync; do \
           command -v "$bin" >/dev/null; \
       done
   ```

2. **Bump the version.** Edit the default `VERSION` in `mkimage.sh` and the
   `vm:publish` ref in the repo-root `Taskfile.yml` from `vm-1.0.0` to
   `vm-1.1.0` (minor bump for an additive change; patch for a bugfix-only
   rebuild; major if the change is breaking).

3. **Rebuild and inspect size.**

   ```sh
   task vm:build
   ls -lh vm-image/dist/
   ```

   Confirm the qcow2 is still <= 200 MB. If it isn't, reconsider: rsync
   shouldn't push us over, but a heavier tool might.

4. **Publish.**

   ```sh
   task vm:publish
   ```

5. **Pin the new version in the backends.** Update `internal/backend/lima`
   and `internal/backend/wsl` (or wherever `EnsureVM` references the image
   tag) to fetch `vm-1.1.0`. Older installs keep working off the older tag
   until the user runs `bolt upgrade`.

### Upgrading a tool's pinned version

Alpine packages float on the Alpine release. To pin tighter, switch from
`apk add podman` to `apk add 'podman=5.1.2-r0'`, then rebuild and bump the
image tag. Alpine version itself is pinned via the `ALPINE_VERSION` build
arg in the Dockerfile.

## Verification status

The Dockerfile and `mkimage.sh` were validated as follows on a developer
laptop while spec 07 was implemented:

| Check                                                  | Status |
| ------------------------------------------------------ | ------ |
| `docker build --check` (Dockerfile lint)               | Pass   |
| Full `docker build` succeeds; all 6 tools on `$PATH`   | Pass   |
| Uncompressed image size (sanity for AC 3)              | 44.8 MB - well under the 200 MB qcow2 ceiling |
| `mkimage.sh` shellcheck / `bash -n`                    | Syntax OK |
| Lima qcow2 boots, six tools present (AC 2, Mac side)   | Requires Lima + qemu-img on the build host; not run in this sandbox |
| WSL2 import + boot, six tools present (AC 2, Win side) | Requires a Windows host; not run in this sandbox |

If you're picking this up on a clean build host, install `qemu-img` and run
`task vm:build`; you should get both artifacts. To exercise booting, attach
the qcow2 to a Lima Alpine template and `wsl --import` the tarball on
Windows.
