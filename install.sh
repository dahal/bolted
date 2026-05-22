#!/bin/sh
# install.sh - download and install the bolted CLI (`bolt`)
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/dahal/bolted/main/install.sh | sh
#
# Override the version:
#   BOLTED_VERSION=v0.1.2 sh install.sh
#
# Override the install prefix (default: $HOME/.local/bin):
#   BOLTED_PREFIX=$HOME/bin sh install.sh

set -eu

REPO="dahal/bolted"
PREFIX="${BOLTED_PREFIX:-$HOME/.local/bin}"
VERSION="${BOLTED_VERSION:-}"

log() { printf '==> %s\n' "$*"; }
warn() { printf 'warn: %s\n' "$*" >&2; }
die() { printf 'error: %s\n' "$*" >&2; exit 1; }

need() {
    command -v "$1" >/dev/null 2>&1 || die "missing required tool: $1"
}

detect_os() {
    uname_s=$(uname -s 2>/dev/null || echo unknown)
    case "$uname_s" in
        Darwin) echo darwin ;;
        Linux) echo linux ;;
        MINGW*|MSYS*|CYGWIN*|Windows_NT)
            cat >&2 <<'EOM'
Windows is not supported by this installer.

Download the latest bolted-<version>-windows-amd64.zip from:
    https://github.com/dahal/bolted/releases

Extract bolt.exe to a directory on your PATH (e.g. %USERPROFILE%\bin).
EOM
            exit 1
            ;;
        *) die "unsupported OS: $uname_s" ;;
    esac
}

detect_arch() {
    uname_m=$(uname -m 2>/dev/null || echo unknown)
    case "$uname_m" in
        arm64|aarch64) echo arm64 ;;
        x86_64|amd64) echo amd64 ;;
        *) die "unsupported arch: $uname_m" ;;
    esac
}

# Linux binaries are not built yet (MVP targets darwin + windows only).
guard_linux_unsupported() {
    if [ "$1" = "linux" ]; then
        die "Linux host binaries are not published yet. See https://github.com/dahal/bolted/releases"
    fi
}

resolve_version() {
    if [ -n "$VERSION" ]; then
        printf '%s' "$VERSION"
        return
    fi
    # Use GitHub's redirect from /releases/latest to discover the tag without
    # needing jq or an auth token. When the repo has no published releases
    # GitHub does not 404 - it redirects to /releases (the listing page), so
    # the trailing path segment becomes the literal string "releases". Catch
    # that case explicitly, and also reject anything that does not look
    # remotely like a version tag, so the bad value can't flow downstream.
    need curl
    redirect=$(curl -fsSLI -o /dev/null -w '%{url_effective}' \
        "https://github.com/${REPO}/releases/latest") \
        || die "failed to query latest release"
    tag=${redirect##*/}
    [ -n "$tag" ] || die "could not parse latest release tag"
    case "$tag" in
        releases)
            die "no published releases at https://github.com/${REPO}/releases yet - set BOLTED_VERSION=vX.Y.Z to install a specific tag" ;;
        v[0-9]*|V[0-9]*|[0-9]*) ;;
        *)
            die "resolved tag ${tag} does not look like a version - set BOLTED_VERSION=vX.Y.Z to override" ;;
    esac
    printf '%s' "$tag"
}

download() {
    url=$1
    out=$2
    log "downloading $url"
    curl -fsSL --retry 3 -o "$out" "$url" \
        || die "download failed: $url"
}

verify_checksum() {
    archive=$1
    sums=$2
    archive_name=$(basename "$archive")
    expected=$(grep "  ${archive_name}\$" "$sums" | awk '{print $1}')
    [ -n "$expected" ] || die "no checksum entry for ${archive_name} in SHA256SUMS"

    if command -v shasum >/dev/null 2>&1; then
        actual=$(shasum -a 256 "$archive" | awk '{print $1}')
    elif command -v sha256sum >/dev/null 2>&1; then
        actual=$(sha256sum "$archive" | awk '{print $1}')
    else
        die "need either shasum or sha256sum to verify the download"
    fi

    [ "$expected" = "$actual" ] \
        || die "checksum mismatch for ${archive_name} (expected ${expected}, got ${actual})"
    log "checksum ok ($archive_name)"
}

main() {
    need curl
    need tar
    need mkdir
    need uname

    os=$(detect_os)
    arch=$(detect_arch)
    guard_linux_unsupported "$os"

    version=$(resolve_version)
    log "installing Bolted ${version} for ${os}/${arch}"

    archive_name="bolted-${version}-${os}-${arch}.tar.gz"
    base_url="https://github.com/${REPO}/releases/download/${version}"
    archive_url="${base_url}/${archive_name}"
    sums_url="${base_url}/SHA256SUMS"

    tmp=$(mktemp -d 2>/dev/null || mktemp -d -t bolted-install)
    trap 'rm -rf "$tmp"' EXIT INT TERM

    download "$archive_url" "${tmp}/${archive_name}"
    download "$sums_url" "${tmp}/SHA256SUMS"

    verify_checksum "${tmp}/${archive_name}" "${tmp}/SHA256SUMS"

    log "extracting"
    tar -xzf "${tmp}/${archive_name}" -C "$tmp"
    [ -f "${tmp}/bolt" ] || die "archive did not contain a 'bolt' binary"

    mkdir -p "$PREFIX"
    install_path="${PREFIX}/bolt"

    mv "${tmp}/bolt" "$install_path"
    chmod +x "$install_path"

    log "installed $install_path"

    case ":${PATH}:" in
        *":${PREFIX}:"*) ;;
        *) warn "${PREFIX} is not on your PATH. Add: export PATH=\"${PREFIX}:\$PATH\"" ;;
    esac

    printf '\n'
    printf 'Bolted %s installed.\n' "$version"
    printf 'next: run `bolt init` to set up Bolted.\n'
}

main "$@"
