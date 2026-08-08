#!/usr/bin/env bash
set -euo pipefail

REPO_ROOT="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
TEMP_DIR="$(mktemp -d)"
IMAGE_NAME="shrinkray:docker-smoke-$$"
CONTAINER_NAME="shrinkray-docker-smoke-$$"
MOVIES_DIR="${TEMP_DIR}/Movies"
TV_DIR="${TEMP_DIR}/TV"
STATE_DIR="${TEMP_DIR}/state"
CURRENT_UID="$(id -u)"
CURRENT_GID="$(id -g)"

cleanup() {
  docker rm --force "$CONTAINER_NAME" >/dev/null 2>&1 || true
  docker image rm "$IMAGE_NAME" >/dev/null 2>&1 || true
  rm -rf -- "$TEMP_DIR"
}

trap cleanup EXIT
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM

fail() {
  printf 'docker smoke test: %s\n' "$*" >&2
  exit 1
}

command -v docker >/dev/null 2>&1 || fail 'docker is required'
command -v curl >/dev/null 2>&1 || fail 'curl is required'

mkdir -p "$MOVIES_DIR" "$TV_DIR" "$STATE_DIR"

printf 'Building Docker image...\n'
docker build --tag "$IMAGE_NAME" "$REPO_ROOT"

printf 'Starting isolated test container...\n'
docker run --detach \
  --name "$CONTAINER_NAME" \
  --init \
  --user "${CURRENT_UID}:${CURRENT_GID}" \
  --group-add "$CURRENT_GID" \
  --read-only \
  --tmpfs /tmp \
  --security-opt no-new-privileges:true \
  --cap-drop ALL \
  --publish "127.0.0.1::8787" \
  --mount "type=bind,src=${MOVIES_DIR},dst=/media/movies" \
  --mount "type=bind,src=${TV_DIR},dst=/media/tv" \
  --mount "type=bind,src=${STATE_DIR},dst=/var/lib/shrinkray" \
  "$IMAGE_NAME" \
  --root "Movies=/media/movies" \
  --root "TV Shows=/media/tv" \
  --listen "0.0.0.0:8787" \
  --shrinkray-bin "/usr/local/bin/shrinkray" \
  --state-dir "/var/lib/shrinkray" >/dev/null

HOST_IP="$(
  docker inspect \
    --format '{{(index (index .NetworkSettings.Ports "8787/tcp") 0).HostIp}}' \
    "$CONTAINER_NAME"
)"
[ "$HOST_IP" = "127.0.0.1" ] ||
  fail "container port 8787 is published on ${HOST_IP}, not 127.0.0.1"

PORT_OUTPUT="$(docker port "$CONTAINER_NAME" 8787/tcp)"
case "$PORT_OUTPUT" in
  127.0.0.1:*) ;;
  *) fail "unexpected published port: ${PORT_OUTPUT}" ;;
esac
HOST_PORT="${PORT_OUTPUT##*:}"
case "$HOST_PORT" in
  ''|*[!0-9]*) fail "could not determine temporary host port: ${PORT_OUTPUT}" ;;
esac

PUBLISHED_PORTS="$(
  docker inspect --format '{{json .HostConfig.PortBindings}}' "$CONTAINER_NAME"
)"
EXPOSED_PORTS="$(
  docker inspect --format '{{json .Config.ExposedPorts}}' "$CONTAINER_NAME"
)"
case "${PUBLISHED_PORTS} ${EXPOSED_PORTS}" in
  *'"443/tcp"'*|*'"8443/tcp"'*)
    fail 'container publishes or exposes port 443 or 8443'
    ;;
esac

HEALTH_URL="http://127.0.0.1:${HOST_PORT}/api/health"
HEALTH_JSON=""
printf 'Waiting for %s...\n' "$HEALTH_URL"
for ((attempt = 1; attempt <= 60; attempt++)); do
  if HEALTH_JSON="$(curl --fail --silent --show-error "$HEALTH_URL" 2>/dev/null)"; then
    break
  fi
  if [ "$(docker inspect --format '{{.State.Running}}' "$CONTAINER_NAME")" != "true" ]; then
    docker logs "$CONTAINER_NAME" >&2 || true
    fail 'container stopped before becoming healthy'
  fi
  sleep 1
done

[ -n "$HEALTH_JSON" ] || {
  docker logs "$CONTAINER_NAME" >&2 || true
  fail 'health endpoint did not become ready within 60 seconds'
}

printf '%s\n' "$HEALTH_JSON" |
  grep -Eq '"status"[[:space:]]*:[[:space:]]*"ok"' ||
  fail "health status is not ok: ${HEALTH_JSON}"
printf '%s\n' "$HEALTH_JSON" |
  grep -Eq '"label"[[:space:]]*:[[:space:]]*"Movies"' ||
  fail "Movies root is absent from health response: ${HEALTH_JSON}"
printf '%s\n' "$HEALTH_JSON" |
  grep -Eq '"label"[[:space:]]*:[[:space:]]*"TV Shows"' ||
  fail "TV Shows root is absent from health response: ${HEALTH_JSON}"

printf 'bind-mounted source\n' > "${MOVIES_DIR}/bind-space.mkv"
JOB_JSON="$(curl --fail --silent --show-error \
  --request POST \
  --header 'Content-Type: application/json' \
  --data '{"root_id":"movies","path":"bind-space.mkv","preset":"balanced","container":"mkv","keep_all_audio":false}' \
  "http://127.0.0.1:${HOST_PORT}/api/jobs")"
printf '%s\n' "$JOB_JSON" |
  grep -Eq '"disk_available_bytes"[[:space:]]*:[[:space:]]*[1-9]' ||
  fail "bind-mounted job did not report media filesystem space: ${JOB_JSON}"
printf '%s\n' "$JOB_JSON" |
  grep -Eq '"disk_required_bytes"[[:space:]]*:[[:space:]]*[1-9]' ||
  fail "bind-mounted job did not report a disk requirement: ${JOB_JSON}"

printf 'Docker smoke test passed on 127.0.0.1:%s.\n' "$HOST_PORT"
