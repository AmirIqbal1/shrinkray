#!/usr/bin/env bash
# Recover only Shrinkray and its configured private Serve listener.
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)"
# shellcheck source=scripts/docker-common.sh
. "${SCRIPT_DIR}/docker-common.sh"

load_shrinkray_env
[ "$SHRINKRAY_TAILSCALE_HTTPS_PORT" != 443 ] ||
  docker_common_die 'watchdog refuses configured HTTPS port 443'
command -v docker >/dev/null 2>&1 || docker_common_die 'docker is required'
docker info >/dev/null 2>&1 || docker_common_die 'Docker daemon is unavailable'

container_id="$(shrinkray_container_id)"
if [ -z "$container_id" ]; then
  printf 'Watchdog: Shrinkray container is missing; recreating only Shrinkray.\n'
  run_compose up -d --no-deps shrinkray
  container_id="$(shrinkray_container_id)"
fi

running="$(docker inspect --format '{{.State.Running}}' "$container_id" 2>/dev/null || true)"
health="$(docker inspect --format '{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}' "$container_id" 2>/dev/null || true)"
if [ "$running" != true ] || [ "$health" != healthy ] || ! api_health_ok; then
  printf 'Watchdog: Shrinkray is unhealthy; restarting only the Shrinkray service.\n'
  run_compose restart shrinkray
  wait_for_shrinkray_health || docker_common_die 'Shrinkray stayed unhealthy after restart'
fi

serve_status="$(tailscale serve status 2>&1 || true)"
if ! serve_backend_at_port "$serve_status" "$SHRINKRAY_TAILSCALE_HTTPS_PORT" "$SHRINKRAY_BACKEND"; then
  printf 'Watchdog: expected private Serve route is missing; restoring only port %s.\n' \
    "$SHRINKRAY_TAILSCALE_HTTPS_PORT"
  "${SCRIPT_DIR}/configure-tailscale.sh"
fi

printf 'Watchdog: Shrinkray is healthy.\n'
