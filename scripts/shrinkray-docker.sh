#!/usr/bin/env bash
# Manage only the Shrinkray Compose service.
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)"
# shellcheck source=scripts/docker-common.sh
. "${SCRIPT_DIR}/docker-common.sh"

usage() {
  cat <<'EOF'
Usage: scripts/shrinkray-docker.sh <command>

Commands:
  start    Build and start Shrinkray, then verify health
  stop     Stop only the Shrinkray Compose service
  restart  Restart only Shrinkray, then verify health
  status   Show Compose, health, ports and Tailscale status
  logs     Follow the last 200 Shrinkray log lines
  build    Pull bases and build the Shrinkray image
  update   Fast-forward Git, rebuild, recreate Shrinkray and roll back on failure
  doctor   Run read-only Docker, filesystem, port and Tailscale checks
  repair   Repair only Shrinkray and its configured non-443 Serve listener
EOF
}

require_runtime() {
  command -v docker >/dev/null 2>&1 || docker_common_die 'docker is required'
  docker info >/dev/null 2>&1 || docker_common_die 'Docker daemon is unavailable'
  docker compose version >/dev/null 2>&1 || docker_common_die 'Docker Compose is unavailable'
}

verify_health() {
  wait_for_shrinkray_health || {
    container_id="$(shrinkray_container_id)"
    [ -z "$container_id" ] || docker logs --tail 100 "$container_id" >&2 || true
    docker_common_die "Shrinkray did not become healthy at ${SHRINKRAY_HEALTH_URL}"
    return 1
  }
  printf 'Shrinkray is healthy at %s\n' "$SHRINKRAY_HEALTH_URL"
}

start_shrinkray() {
  if [ "${SHRINKRAY_START_NO_BUILD:-0}" = 1 ]; then
    run_compose up -d --no-build --no-deps shrinkray
  else
    run_compose up -d --build shrinkray
  fi
  verify_health
}

restart_shrinkray() {
  run_compose restart shrinkray
  verify_health
}

show_port_owner() {
  local status="$1" port="$2" lines
  lines="$(ss_port_lines "$status" "$port")"
  if [ -n "$lines" ]; then
    printf 'Host port %s:\n%s\n' "$port" "$lines"
  else
    printf 'Host port %s: not listening\n' "$port"
  fi
}

show_status() {
  local container_id health ss_status
  run_compose ps
  container_id="$(shrinkray_container_id)"
  if [ -n "$container_id" ]; then
    health="$(docker inspect --format '{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}' "$container_id" 2>/dev/null || true)"
    printf 'Container health: %s\n' "${health:-unknown}"
  else
    printf 'Container health: missing\n'
  fi
  if api_health_ok; then
    printf 'Local API health: ok (%s)\n' "$SHRINKRAY_HEALTH_URL"
  else
    printf 'Local API health: failed (%s)\n' "$SHRINKRAY_HEALTH_URL"
  fi
  ss_status="$(ss -ltnp 2>&1 || true)"
  show_port_owner "$ss_status" 443
  show_port_owner "$ss_status" "$SHRINKRAY_TAILSCALE_HTTPS_PORT"
  show_port_owner "$ss_status" "$SHRINKRAY_PORT"
  printf 'Tailscale status:\n'
  tailscale status 2>&1 || true
  printf 'Tailscale Serve status:\n'
  tailscale serve status 2>&1 || true
}

doctor_check() {
  local label="$1"
  shift
  if "$@"; then
    printf 'OK: %s\n' "$label"
  else
    printf 'PROBLEM: %s\n' "$label" >&2
    DOCTOR_ISSUES=$((DOCTOR_ISSUES + 1))
  fi
}

doctor() {
  local container_id running health mounts user_spec ss_status serve_status tailscale_json render_devices device_permissions container_devices capabilities nvidia_runtime
  DOCTOR_ISSUES=0

  doctor_check 'Docker daemon is available' docker info
  container_id="$(shrinkray_container_id)"
  doctor_check 'Shrinkray container exists' test -n "$container_id"
  running=false
  health=missing
  mounts=""
  user_spec=""
  if [ -n "$container_id" ]; then
    running="$(docker inspect --format '{{.State.Running}}' "$container_id" 2>/dev/null || true)"
    health="$(docker inspect --format '{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}' "$container_id" 2>/dev/null || true)"
    mounts="$(docker inspect --format '{{range .Mounts}}{{println .Destination}}{{end}}' "$container_id" 2>/dev/null || true)"
    user_spec="$(docker inspect --format '{{.Config.User}}' "$container_id" 2>/dev/null || true)"
  fi
  doctor_check 'Shrinkray container is running' test "$running" = true
  doctor_check 'Container health is healthy' test "$health" = healthy
  doctor_check "API health succeeds at ${SHRINKRAY_HEALTH_URL}" api_health_ok
  doctor_check 'Movies directory is accessible to configured IDs' configured_user_can_access_directory "$SHRINKRAY_MOVIES_PATH"
  doctor_check 'TV directory is accessible to configured IDs' configured_user_can_access_directory "$SHRINKRAY_TV_PATH"
  doctor_check 'State directory is accessible to configured IDs' configured_user_can_access_directory "$SHRINKRAY_STATE_DIR"
  doctor_check 'Movies mount is present' grep -Fxq /media/movies <<<"$mounts"
  doctor_check 'TV mount is present' grep -Fxq /media/tv <<<"$mounts"
  doctor_check 'State mount is present' grep -Fxq /var/lib/shrinkray <<<"$mounts"
  doctor_check 'Container UID/GID matches .env' test "$user_spec" = "${SHRINKRAY_UID}:${SHRINKRAY_GID}"

  render_devices="$(find /dev/dri -maxdepth 1 -type c -name 'renderD*' -print 2>/dev/null || true)"
  printf 'DRM render devices detected:\n%s\n' "${render_devices:-  none}"
  device_permissions=""
  while IFS= read -r device; do
    [ -n "$device" ] || continue
    device_permissions+="$(stat -c '  %n mode=%a uid=%u gid=%g' -- "$device")"$'\n'
  done <<<"$render_devices"
  printf 'Render-device permissions:\n%s' "${device_permissions:-  none$'\n'}"
  container_devices=""
  [ -z "$container_id" ] || container_devices="$(docker exec "$container_id" sh -c 'find /dev/dri -maxdepth 1 -name "renderD*" -print 2>/dev/null' || true)"
  printf 'Container-visible render devices:\n%s\n' "${container_devices:-  none}"
  if nvidia_runtime_installed; then nvidia_runtime=available; else nvidia_runtime=unavailable; fi
  printf 'NVIDIA container runtime: %s\n' "$nvidia_runtime"
  capabilities="$(curl --fail --silent --show-error "${SHRINKRAY_BACKEND}/api/capabilities" 2>/dev/null || true)"
  printf 'Container encoder capabilities: %s\n' "${capabilities:-unavailable}"

  ss_status="$(ss -ltnp 2>&1 || true)"
  show_port_owner "$ss_status" 443
  show_port_owner "$ss_status" "$SHRINKRAY_TAILSCALE_HTTPS_PORT"
  show_port_owner "$ss_status" "$SHRINKRAY_PORT"
  if ss_port_lines "$ss_status" "$SHRINKRAY_PORT" | grep -Fq '127.0.0.1:'; then
    printf 'OK: backend port is loopback-bound\n'
  else
    printf 'PROBLEM: backend port is not visibly loopback-bound\n' >&2
    DOCTOR_ISSUES=$((DOCTOR_ISSUES + 1))
  fi

  tailscale_json="$(tailscale status --json 2>/dev/null || true)"
  if printf '%s' "$tailscale_json" | grep -Eq '"BackendState"[[:space:]]*:[[:space:]]*"Running"'; then
    printf 'OK: Tailscale is running\n'
  else
    printf 'PROBLEM: Tailscale is not running\n' >&2
    DOCTOR_ISSUES=$((DOCTOR_ISSUES + 1))
  fi
  serve_status="$(tailscale serve status 2>&1 || true)"
  printf 'Tailscale Serve status:\n%s\n' "$serve_status"
  if serve_backend_at_port "$serve_status" "$SHRINKRAY_TAILSCALE_HTTPS_PORT" "$SHRINKRAY_BACKEND"; then
    printf 'OK: Shrinkray is served on private HTTPS port %s\n' "$SHRINKRAY_TAILSCALE_HTTPS_PORT"
  else
    printf 'PROBLEM: expected private Shrinkray Serve route is missing\n' >&2
    DOCTOR_ISSUES=$((DOCTOR_ISSUES + 1))
  fi
  if serve_backend_at_port "$serve_status" 443 "$SHRINKRAY_BACKEND"; then
    printf 'PROBLEM: Tailscale incorrectly serves Shrinkray on port 443\n' >&2
    DOCTOR_ISSUES=$((DOCTOR_ISSUES + 1))
  else
    printf 'OK: Tailscale does not serve Shrinkray on port 443\n'
  fi
  [ "$DOCTOR_ISSUES" -eq 0 ]
}

repair() {
  local container_id running serve_status
  [ "$SHRINKRAY_TAILSCALE_HTTPS_PORT" != 443 ] ||
    docker_common_die 'repair refuses configured HTTPS port 443'

  container_id="$(shrinkray_container_id)"
  running=false
  [ -z "$container_id" ] ||
    running="$(docker inspect --format '{{.State.Running}}' "$container_id" 2>/dev/null || true)"
  if [ -z "$container_id" ]; then
    run_compose up -d --build --no-deps shrinkray
  elif [ "$running" != true ] || ! api_health_ok; then
    run_compose restart shrinkray
  fi
  verify_health
  "${SCRIPT_DIR}/configure-tailscale.sh"

  serve_status="$(tailscale serve status)"
  if serve_port_is_only_backend "$serve_status" 443 "$SHRINKRAY_BACKEND"; then
    printf 'Removing the clearly exclusive legacy Shrinkray Serve listener on port 443.\n'
    tailscale serve --https=443 off
  fi
}

update_shrinkray() {
  local container_id old_image_id old_image_ref rollback_ref update_ok
  container_id="$(shrinkray_container_id)"
  old_image_id=""
  old_image_ref=""
  rollback_ref="shrinkray-rollback:$(date +%Y%m%d%H%M%S)-$$"
  if [ -n "$container_id" ]; then
    old_image_id="$(docker inspect --format '{{.Image}}' "$container_id")"
    old_image_ref="$(docker inspect --format '{{.Config.Image}}' "$container_id")"
    docker image tag "$old_image_id" "$rollback_ref"
  fi

  git -C "$SHRINKRAY_REPO_DIR" pull --ff-only
  run_compose build --pull shrinkray
  run_compose up -d --no-deps --force-recreate --no-build shrinkray
  update_ok=false
  if verify_health; then
    update_ok=true
  fi
  if [ "$update_ok" = true ]; then
    [ -z "$old_image_id" ] || docker image rm "$rollback_ref" >/dev/null
    printf 'Shrinkray update completed successfully.\n'
    return 0
  fi

  printf 'Update health validation failed; restoring the previous Shrinkray image.\n' >&2
  run_compose stop shrinkray || true
  if [ -n "$old_image_id" ] && [ -n "$old_image_ref" ]; then
    docker image tag "$rollback_ref" "$old_image_ref"
    run_compose up -d --no-deps --force-recreate --no-build shrinkray
    verify_health || docker_common_die 'previous Shrinkray image was restored but is not healthy'
    docker image rm "$rollback_ref" >/dev/null 2>&1 || true
    printf 'Previous Shrinkray image and container configuration restored.\n' >&2
  fi
  return 1
}

command_name="${1-}"
[ "$#" -eq 1 ] || {
  usage >&2
  exit 2
}

case "$command_name" in
  -h|--help|help) usage; exit 0 ;;
  start|stop|restart|status|logs|build|update|doctor|repair) ;;
  *) usage >&2; exit 2 ;;
esac

load_shrinkray_env
require_runtime

case "$command_name" in
  start) start_shrinkray ;;
  stop) run_compose stop shrinkray ;;
  restart) restart_shrinkray ;;
  status) show_status ;;
  logs) run_compose logs -f --tail=200 shrinkray ;;
  build) run_compose build --pull shrinkray ;;
  update) update_shrinkray ;;
  doctor) doctor ;;
  repair) repair ;;
esac
