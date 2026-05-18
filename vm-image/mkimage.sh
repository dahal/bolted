#!/usr/bin/env bash
# mkimage.sh - build the Bolted VM rootfs and package it for both backends.
#
# Outputs (under vm-image/dist/):
#   bolted-vm-<version>-rootfs.tar.gz   - consumed by `wsl --import`
#   bolted-vm-<version>.qcow2           - booted by Lima's Alpine template
#
# Requirements on the build host:
#   - docker (or a docker-compatible CLI exposed as `docker`)
#   - qemu-img  (for the Lima qcow2 conversion)
#   - gzip, tar (standard)
#
# Usage:
#   ./mkimage.sh                 # build both artifacts
#   VERSION=vm-1.2.3 ./mkimage.sh
#   SKIP_QCOW2=1 ./mkimage.sh    # WSL2 tar only (useful on hosts without qemu-img)
#   SKIP_WSL=1 ./mkimage.sh      # qcow2 only

set -euo pipefail

# ---------- config ----------------------------------------------------------

VERSION="${VERSION:-vm-1.0.0}"
IMAGE_NAME="${IMAGE_NAME:-bolted-vm}"
IMAGE_TAG="${IMAGE_NAME}:${VERSION}"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DIST_DIR="${SCRIPT_DIR}/dist"
ROOTFS_TAR="${DIST_DIR}/${IMAGE_NAME}-${VERSION}-rootfs.tar"
ROOTFS_TGZ="${ROOTFS_TAR}.gz"
RAW_IMG="${DIST_DIR}/${IMAGE_NAME}-${VERSION}.raw"
QCOW2_IMG="${DIST_DIR}/${IMAGE_NAME}-${VERSION}.qcow2"

# Sparse raw disk size. Alpine + base tools fit in well under 1 GiB; the
# qcow2 only stores allocated blocks so the published artifact stays small.
RAW_SIZE_MB="${RAW_SIZE_MB:-1024}"

DOCKER="${DOCKER:-docker}"

# ---------- helpers ---------------------------------------------------------

log()  { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m!! \033[0m %s\n' "$*" >&2; }
die()  { printf '\033[1;31mxx \033[0m %s\n' "$*" >&2; exit 1; }

require() {
    command -v "$1" >/dev/null 2>&1 || die "missing required tool: $1"
}

# ---------- pre-flight ------------------------------------------------------

require "${DOCKER}"
require tar
require gzip

mkdir -p "${DIST_DIR}"

# ---------- 1. build the rootfs image --------------------------------------

log "building ${IMAGE_TAG} from $(realpath --relative-to="${PWD}" "${SCRIPT_DIR}/Dockerfile" 2>/dev/null || echo "${SCRIPT_DIR}/Dockerfile")"

# --pull keeps the alpine base honest across rebuilds.
# --platform: Bolted runs on darwin-arm64, darwin-amd64, windows-amd64.
# We bake amd64 by default since both Lima (Rosetta) and WSL2 expect it;
# override via PLATFORM=linux/arm64 for native Apple Silicon images.
PLATFORM="${PLATFORM:-linux/amd64}"

"${DOCKER}" build \
    --pull \
    --platform "${PLATFORM}" \
    --tag "${IMAGE_TAG}" \
    --file "${SCRIPT_DIR}/Dockerfile" \
    "${SCRIPT_DIR}"

# ---------- 2. WSL2 artifact: flat rootfs tarball --------------------------

if [[ "${SKIP_WSL:-0}" != "1" ]]; then
    log "exporting rootfs tar (for wsl --import) -> ${ROOTFS_TGZ}"

    cid="$("${DOCKER}" create --platform "${PLATFORM}" "${IMAGE_TAG}")"
    trap '"${DOCKER}" rm -f "${cid}" >/dev/null 2>&1 || true' EXIT

    rm -f "${ROOTFS_TAR}" "${ROOTFS_TGZ}"
    "${DOCKER}" export "${cid}" -o "${ROOTFS_TAR}"
    gzip -9 -f "${ROOTFS_TAR}"

    "${DOCKER}" rm -f "${cid}" >/dev/null
    trap - EXIT

    log "wsl rootfs: $(du -h "${ROOTFS_TGZ}" | awk '{print $1}')"
else
    log "SKIP_WSL=1; skipping WSL2 rootfs tar"
fi

# ---------- 3. Lima artifact: qcow2 disk image -----------------------------

if [[ "${SKIP_QCOW2:-0}" != "1" ]]; then
    if ! command -v qemu-img >/dev/null 2>&1; then
        warn "qemu-img not found; cannot produce qcow2."
        warn "install qemu (\`brew install qemu\` / \`apt install qemu-utils\`) and re-run,"
        warn "or set SKIP_QCOW2=1 to skip this artifact."
        exit 1
    fi
    require mkfs.ext4 || true  # may be absent on macOS; we fall back to a tar-in-qcow approach below

    log "building raw rootfs disk (${RAW_SIZE_MB} MiB sparse) -> ${RAW_IMG}"

    # Strategy: export the docker rootfs as a tar, stuff it into a fresh
    # ext4 filesystem inside a sparse raw file, then convert to qcow2.
    #
    # On macOS we don't have mkfs.ext4 natively, so we run that step inside
    # the just-built image (which has busybox/util-linux available via the
    # alpine package set). This keeps the build host requirement to just
    # docker + qemu-img.

    rm -f "${RAW_IMG}" "${QCOW2_IMG}"
    truncate -s "${RAW_SIZE_MB}M" "${RAW_IMG}"

    # Re-use the WSL rootfs tar if we just built it; otherwise export now.
    tmp_tar=""
    if [[ -f "${ROOTFS_TGZ}" ]]; then
        src_tar="${ROOTFS_TGZ}"
    else
        tmp_tar="$(mktemp -t bolted-vm-rootfs.XXXXXX.tar)"
        cid="$("${DOCKER}" create --platform "${PLATFORM}" "${IMAGE_TAG}")"
        "${DOCKER}" export "${cid}" -o "${tmp_tar}"
        "${DOCKER}" rm -f "${cid}" >/dev/null
        src_tar="${tmp_tar}"
    fi

    log "formatting raw image as ext4 and unpacking rootfs"
    # Run mkfs + unpack inside an alpine container so we don't depend on
    # Linux-only tools being on the host.
    "${DOCKER}" run --rm \
        --platform "${PLATFORM}" \
        --privileged \
        -v "${RAW_IMG}:/out/disk.img" \
        -v "${src_tar}:/in/rootfs.tar$([[ "${src_tar}" == *.gz ]] && echo .gz || true):ro" \
        "${IMAGE_TAG}" \
        bash -eu -c '
            apk add --no-cache e2fsprogs util-linux >/dev/null
            mkfs.ext4 -q -F -L bolted-vm /out/disk.img
            mkdir -p /mnt/rootfs
            mount -o loop /out/disk.img /mnt/rootfs
            if [ -f /in/rootfs.tar.gz ]; then
                tar -xzf /in/rootfs.tar.gz -C /mnt/rootfs
            else
                tar -xf /in/rootfs.tar -C /mnt/rootfs
            fi
            sync
            umount /mnt/rootfs
        '

    [[ -n "${tmp_tar}" ]] && rm -f "${tmp_tar}"

    log "converting raw -> qcow2 (compressed) -> ${QCOW2_IMG}"
    qemu-img convert -c -O qcow2 "${RAW_IMG}" "${QCOW2_IMG}"
    rm -f "${RAW_IMG}"

    qcow_size_bytes="$(stat -f%z "${QCOW2_IMG}" 2>/dev/null || stat -c%s "${QCOW2_IMG}")"
    qcow_size_mb=$(( qcow_size_bytes / 1024 / 1024 ))
    log "lima qcow2: ${qcow_size_mb} MiB"

    if (( qcow_size_mb > 200 )); then
        warn "qcow2 is ${qcow_size_mb} MiB; spec 07 AC 3 requires <= 200 MiB."
        warn "review what got added to Dockerfile."
    fi
else
    log "SKIP_QCOW2=1; skipping Lima qcow2"
fi

log "done. artifacts in ${DIST_DIR}:"
ls -lh "${DIST_DIR}"
