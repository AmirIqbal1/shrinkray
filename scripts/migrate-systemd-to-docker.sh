#!/usr/bin/env bash
# Safely migrate only the native Shrinkray service to Docker.
set -Eeuo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)"
# shellcheck source=scripts/docker-common.sh
. "${SCRIPT_DIR}/docker-common.sh"

TEST_MODE="${SHRINKRAY_MIGRATION_TEST_MODE:-0}"
SYSTEM_ROOT="${SHRINKRAY_SYSTEM_ROOT:-}"
if [ "$TEST_MODE" = 1 ]; then
  if [ -z "$SYSTEM_ROOT" ] || [[ "$SYSTEM_ROOT" != /* ]]; then
    docker_common_die 'test mode requires an absolute SHRINKRAY_SYSTEM_ROOT'
  fi
elif [ "$(id -u)" -ne 0 ]; then
  docker_common_die 'migration must run as root'
fi

system_path() {
  printf '%s%s' "$SYSTEM_ROOT" "$1"
}

load_shrinkray_env

[ -f "${SHRINKRAY_REPO_DIR}/compose.yaml" ] ||
  docker_common_die "compose.yaml is missing from ${SHRINKRAY_REPO_DIR}"
[ -x "${SHRINKRAY_REPO_DIR}/scripts/shrinkray-docker.sh" ] ||
  docker_common_die 'Shrinkray Docker management script is missing or not executable'

for command_name in docker curl systemctl; do
  command -v "$command_name" >/dev/null 2>&1 ||
    docker_common_die "${command_name} is required"
done
docker info >/dev/null
docker compose version >/dev/null
(
  cd -- "$SHRINKRAY_REPO_DIR"
  docker compose config >/dev/null
)

for directory in "$SHRINKRAY_MOVIES_PATH" "$SHRINKRAY_TV_PATH" "$SHRINKRAY_STATE_DIR"; do
  [ -d "$directory" ] ||
    docker_common_die "required directory is missing: ${directory}"
  if [ "$TEST_MODE" = 1 ]; then
    if [ ! -r "$directory" ] || [ ! -w "$directory" ] || [ ! -x "$directory" ]; then
      docker_common_die "required directory is not accessible: ${directory}"
    fi
  else
    configured_user_can_access_directory "$directory" ||
      docker_common_die "configured UID/GID cannot access: ${directory}"
  fi
done

service_was_active=false
if systemctl is-active --quiet shrinkray.service; then
  service_was_active=true
fi

timestamp="$(date +%Y%m%d%H%M%S)"
BACKUP_ROOT="${SHRINKRAY_MIGRATION_BACKUP_ROOT:-$(system_path "/var/backups/shrinkray-migration/${timestamp}")}"
[ ! -e "$BACKUP_ROOT" ] ||
  docker_common_die "backup destination already exists: ${BACKUP_ROOT}"
mkdir -p -- "$BACKUP_ROOT"
chmod 0700 "$BACKUP_ROOT"

declare -a BACKUP_PATHS=(
  /etc/systemd/system/shrinkray.service
  /etc/shrinkray/server.conf
  /usr/local/bin/shrinkray
  /usr/local/bin/shrinkray-server
  /usr/local/bin/shrinkray-server-doctor
)

backup_path() {
  local relative="$1" source destination
  source="$(system_path "$relative")"
  destination="${BACKUP_ROOT}${relative}"
  if [ -e "$source" ] || [ -L "$source" ]; then
    mkdir -p -- "$(dirname -- "$destination")"
    cp -a -- "$source" "$destination"
  fi
}

restore_backups() {
  local relative source destination
  for relative in "${BACKUP_PATHS[@]}"; do
    source="${BACKUP_ROOT}${relative}"
    destination="$(system_path "$relative")"
    if [ -e "$source" ] || [ -L "$source" ]; then
      mkdir -p -- "$(dirname -- "$destination")"
      cp -a -- "$source" "$destination"
    fi
  done
}

for path_to_backup in "${BACKUP_PATHS[@]}"; do
  backup_path "$path_to_backup"
done
printf 'Native Shrinkray backup stored at %s\n' "$BACKUP_ROOT"

native_stopped=false
migration_succeeded=false

rollback() {
  local exit_code="$?"
  trap - EXIT
  if [ "$migration_succeeded" = false ] && [ "$native_stopped" = true ]; then
    set +e
    printf 'Migration failed after stopping native Shrinkray; rolling back.\n' >&2
    (
      cd -- "$SHRINKRAY_REPO_DIR"
      docker compose stop shrinkray
    )
    restore_backups
    systemctl daemon-reload
    if [ "$service_was_active" = true ]; then
      systemctl enable shrinkray.service
      systemctl restart shrinkray.service
      old_port=8787
      old_config="$(system_path /etc/shrinkray/server.conf)"
      if [ -f "$old_config" ]; then
        configured_port="$(sed -n "s/^SHRINKRAY_PORT=['\"]\\{0,1\\}\\([0-9][0-9]*\\).*/\\1/p" "$old_config" | head -n 1)"
        [ -z "$configured_port" ] || old_port="$configured_port"
      fi
      if curl --fail --silent --show-error \
        "http://127.0.0.1:${old_port}/api/health" >/dev/null; then
        printf 'Rollback complete: native shrinkray.service is healthy.\n' >&2
      else
        printf 'ROLLBACK WARNING: native service restarted but its health check failed.\n' >&2
      fi
    fi
    printf 'Backups remain available at %s\n' "$BACKUP_ROOT" >&2
  fi
  exit "$exit_code"
}
trap rollback EXIT

printf 'Building the Docker image before stopping native Shrinkray...\n'
(
  cd -- "$SHRINKRAY_REPO_DIR"
  docker compose build --pull shrinkray
)

if [ "$service_was_active" = true ]; then
  systemctl stop shrinkray.service
  native_stopped=true
fi

SHRINKRAY_START_NO_BUILD=1 "${SHRINKRAY_REPO_DIR}/scripts/shrinkray-docker.sh" start
curl --fail --silent --show-error "$SHRINKRAY_HEALTH_URL" >/dev/null
"${SHRINKRAY_REPO_DIR}/scripts/configure-tailscale.sh"

container_id="$(shrinkray_container_id)"
[ -n "$container_id" ] || docker_common_die 'Shrinkray container is missing after migration'
[ "$(docker inspect --format '{{.State.Running}}' "$container_id")" = true ] ||
  docker_common_die 'Shrinkray container is not running after migration'
wait_for_shrinkray_health ||
  docker_common_die 'Shrinkray failed final container and API health validation'

if [ "$service_was_active" = true ]; then
  systemctl disable shrinkray.service
fi

migration_succeeded=true
printf 'Migration completed. Only shrinkray.service was stopped and disabled.\n'
printf 'Native binaries and backup remain available at %s\n' "$BACKUP_ROOT"
