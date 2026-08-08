#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)"

normal="$(docker compose -f "${ROOT_DIR}/compose.yaml" config 2>/dev/null)"
if grep -Eq '^[[:space:]]+(devices|gpus):' <<<"$normal"; then
  printf 'hardware compose test: normal Compose unexpectedly requires GPU devices\n' >&2
  exit 1
fi

hardware="$(SHRINKRAY_DRM_RENDER_DEVICE=/dev/dri/renderD129 SHRINKRAY_DRM_RENDER_GID=109 \
  docker compose -f "${ROOT_DIR}/compose.yaml" -f "${ROOT_DIR}/compose.hwaccel.yaml" config 2>/dev/null)"
grep -q 'source: /dev/dri/renderD129' <<<"$hardware"
grep -q 'target: /dev/dri/renderD129' <<<"$hardware"
if grep -q '/dev/dri/renderD128' <<<"$hardware"; then
  printf 'hardware compose test: override mapped an unrelated DRM device\n' >&2
  exit 1
fi
grep -q 'no-new-privileges:true' <<<"$hardware"
grep -A2 'cap_drop:' <<<"$hardware" | grep -q 'ALL'
if grep -Eq 'privileged:[[:space:]]*true|seccomp=unconfined' <<<"$hardware"; then
  printf 'hardware compose test: override weakened container security\n' >&2
  exit 1
fi

nvidia="$(docker compose -f "${ROOT_DIR}/compose.yaml" -f "${ROOT_DIR}/compose.nvidia.yaml" config 2>/dev/null)"
grep -q 'gpus:' <<<"$nvidia"
grep -q 'count: -1' <<<"$nvidia"
if grep -Eq 'privileged:[[:space:]]*true|seccomp=unconfined' <<<"$nvidia"; then
  printf 'hardware compose test: NVIDIA override weakened container security\n' >&2
  exit 1
fi

printf 'Hardware Compose test passed.\n'
