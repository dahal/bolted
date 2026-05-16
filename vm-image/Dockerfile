# syntax=docker/dockerfile:1.6
#
# Bolted VM base image — layer 1 of the toolchain stack.
#
# Contains only the always-on plumbing every Bolted install needs.
# Anything user-customizable (language toolchains, gh/gcloud/etc., dotfiles)
# lives in the Bolted profile (spec 15) or per-repo devcontainer (spec 16).
#
# Keep this list tight. Adding a package here forces a re-publish of the
# image and a version bump for every backend.

ARG ALPINE_VERSION=3.20

FROM alpine:${ALPINE_VERSION}

# Exactly the six base tools called out by spec 07. No extras.
#   podman      — container runtime inside the VM
#   cryptsetup  — LUKS unlock/lock of the encrypted volume
#   git         — repo clone inside the volume
#   openssh     — exec channel into the VM
#   bash        — predictable shell for bolt exec / scripts
#   curl        — bootstrap downloads (devcontainer CLI, features)
RUN apk add --no-cache \
        podman \
        cryptsetup \
        git \
        openssh \
        bash \
        curl \
    && rm -rf /var/cache/apk/*

# Podman rootless prerequisites — enables containers to start without
# requiring further package installs at first boot.
RUN apk add --no-cache --virtual .podman-runtime \
        fuse-overlayfs \
        shadow-uidmap \
        slirp4netns \
    && rm -rf /var/cache/apk/*

# Sanity: the six tools must resolve on PATH. Build fails loudly if not.
RUN set -eux; \
    for bin in podman cryptsetup git ssh bash curl; do \
        command -v "$bin" >/dev/null; \
    done

# Image metadata. The tag (vm-1.0.0) is applied by mkimage.sh / Taskfile.
LABEL org.opencontainers.image.title="bolted-vm" \
      org.opencontainers.image.description="Minimal Alpine base for the Bolted dev VM (Lima + WSL2)." \
      org.opencontainers.image.source="https://github.com/dahal/bolted" \
      org.opencontainers.image.licenses="MIT"

# No CMD/ENTRYPOINT: this image is consumed as a rootfs by Lima and
# WSL2, not run as a long-lived container. Booting is the backend's job.
