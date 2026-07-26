#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)"
TEMP_DIR="$(mktemp -d)"
FAKE_BIN="${TEMP_DIR}/bin"
TEST_LOG="${TEMP_DIR}/commands.log"
TEST_STATE_DIR="${TEMP_DIR}/state"
SYSTEM_ROOT="${TEMP_DIR}/system-root"
MEDIA_ROOT="${TEMP_DIR}/media"
STATE_ROOT="${TEMP_DIR}/persistent-state"
ENV_FILE="${TEMP_DIR}/shrinkray.env"

cleanup() {
  rm -rf -- "$TEMP_DIR"
}
trap cleanup EXIT HUP INT TERM

fail() {
  printf 'docker operations test: %s\n' "$*" >&2
  exit 1
}

assert_contains() {
  grep -Fq -- "$2" "$1" || fail "$1 did not contain: $2"
}

assert_not_matches() {
  if grep -Eq -- "$2" "$1"; then
    fail "$1 unexpectedly matched: $2"
  fi
}

reset_test() {
  : >"$TEST_LOG"
  rm -f -- "${TEST_STATE_DIR}/restarted" "${TEST_STATE_DIR}/started" \
    "${TEST_STATE_DIR}/native-restarted" "${TEST_STATE_DIR}/tailscale-configured" \
    "${TEST_STATE_DIR}/port-443-changed"
}

run_script() {
  env \
    PATH="${FAKE_BIN}:/usr/bin:/bin" \
    TEST_LOG="$TEST_LOG" \
    TEST_STATE_DIR="$TEST_STATE_DIR" \
    SHRINKRAY_REPO_DIR="$ROOT_DIR" \
    SHRINKRAY_ENV_FILE="$ENV_FILE" \
    SHRINKRAY_HEALTH_ATTEMPTS=1 \
    SHRINKRAY_HEALTH_SLEEP=0 \
    "$@"
}

mkdir -p -- "$FAKE_BIN" "$TEST_STATE_DIR" "$SYSTEM_ROOT" \
  "$MEDIA_ROOT/movies" "$MEDIA_ROOT/tv" "$STATE_ROOT"
for command_name in docker curl tailscale ss systemctl git sleep; do
  ln -s "$ROOT_DIR/tests/fixtures/docker-ops-fake" "${FAKE_BIN}/${command_name}"
done

cat >"$ENV_FILE" <<EOF
SHRINKRAY_PORT=8787
SHRINKRAY_UID=1000
SHRINKRAY_GID=1000
SHRINKRAY_MEDIA_GID=1000
SHRINKRAY_STATE_DIR=${STATE_ROOT}
SHRINKRAY_MOVIES_PATH=${MEDIA_ROOT}/movies
SHRINKRAY_TV_PATH=${MEDIA_ROOT}/tv
SHRINKRAY_TAILSCALE_HTTPS_PORT=8443
EOF

for relative in \
  /etc/systemd/system/shrinkray.service \
  /etc/shrinkray/server.conf \
  /usr/local/bin/shrinkray \
  /usr/local/bin/shrinkray-server \
  /usr/local/bin/shrinkray-server-doctor; do
  mkdir -p -- "$(dirname -- "${SYSTEM_ROOT}${relative}")"
  printf 'test backup content\n' >"${SYSTEM_ROOT}${relative}"
done
printf "SHRINKRAY_PORT='8787'\n" >"${SYSTEM_ROOT}/etc/shrinkray/server.conf"

printf 'Testing migration builds before stopping systemd...\n'
reset_test
TEST_SERVICE_ACTIVE=1 TEST_HEALTH_OK=1 \
  run_script env \
    SHRINKRAY_MIGRATION_TEST_MODE=1 \
    SHRINKRAY_SYSTEM_ROOT="$SYSTEM_ROOT" \
    SHRINKRAY_MIGRATION_BACKUP_ROOT="${TEMP_DIR}/backup-success" \
    "$ROOT_DIR/scripts/migrate-systemd-to-docker.sh" >/dev/null
build_line="$(grep -n 'docker compose build --pull shrinkray' "$TEST_LOG" | cut -d: -f1)"
stop_line="$(grep -n 'systemctl stop shrinkray.service' "$TEST_LOG" | cut -d: -f1)"
[ "$build_line" -lt "$stop_line" ] || fail 'migration stopped systemd before the image build'
assert_contains "$TEST_LOG" 'systemctl disable shrinkray.service'
assert_not_matches "$TEST_LOG" 'systemctl (stop|disable|restart) (docker|coolify|jellyfin)'

printf 'Testing failed Docker health restores systemd...\n'
reset_test
set +e
TEST_SERVICE_ACTIVE=1 TEST_HEALTH_OK=0 TEST_CONTAINER_HEALTH=unhealthy \
TEST_START_STAYS_UNHEALTHY=1 \
  run_script env \
    SHRINKRAY_MIGRATION_TEST_MODE=1 \
    SHRINKRAY_SYSTEM_ROOT="$SYSTEM_ROOT" \
    SHRINKRAY_MIGRATION_BACKUP_ROOT="${TEMP_DIR}/backup-failure" \
    "$ROOT_DIR/scripts/migrate-systemd-to-docker.sh" >"${TEMP_DIR}/failure.out" 2>&1
failure_status=$?
set -e
[ "$failure_status" -ne 0 ] || fail 'failed migration returned success'
assert_contains "$TEST_LOG" 'docker compose stop shrinkray'
assert_contains "$TEST_LOG" 'systemctl daemon-reload'
assert_contains "$TEST_LOG" 'systemctl enable shrinkray.service'
assert_contains "$TEST_LOG" 'systemctl restart shrinkray.service'
assert_contains "${TEMP_DIR}/failure.out" 'rolling back'

printf 'Testing management restart targets only Shrinkray...\n'
reset_test
run_script "$ROOT_DIR/scripts/shrinkray-docker.sh" restart >/dev/null
assert_contains "$TEST_LOG" 'docker compose restart shrinkray'
assert_not_matches "$TEST_LOG" 'docker compose restart (docker|coolify|jellyfin)'

printf 'Testing doctor is read-only...\n'
reset_test
serve_ok="$(printf 'https://hostname.tailnet.ts.net:8443/\n|-- / proxy http://127.0.0.1:8787')"
TEST_SERVE_STATUS="$serve_ok" run_script \
  "$ROOT_DIR/scripts/shrinkray-docker.sh" doctor >/dev/null
assert_not_matches "$TEST_LOG" 'docker compose (up|stop|restart|rm|down)|tailscale serve --https|systemctl (stop|restart)'

printf 'Testing repair preserves port 443...\n'
reset_test
serve_unrelated_443="$(printf 'https://hostname.tailnet.ts.net/\n|-- / proxy http://127.0.0.1:9000\n\n%s' "$serve_ok")"
TEST_SERVE_STATUS="$serve_unrelated_443" run_script \
  "$ROOT_DIR/scripts/shrinkray-docker.sh" repair >/dev/null
[ ! -e "${TEST_STATE_DIR}/port-443-changed" ] || fail 'repair changed port 443'

printf 'Testing Tailscale configuration refuses port 443...\n'
sed 's/SHRINKRAY_TAILSCALE_HTTPS_PORT=8443/SHRINKRAY_TAILSCALE_HTTPS_PORT=443/' \
  "$ENV_FILE" >"${TEMP_DIR}/port-443.env"
reset_test
set +e
ENV_FILE="${TEMP_DIR}/port-443.env" run_script \
  "$ROOT_DIR/scripts/configure-tailscale.sh" >/dev/null 2>&1
port_443_status=$?
set -e
[ "$port_443_status" -ne 0 ] || fail 'Tailscale script accepted port 443'
[ ! -e "${TEST_STATE_DIR}/port-443-changed" ] || fail 'Tailscale script changed port 443'

printf 'Testing unrelated Serve routes are preserved...\n'
reset_test
unrelated="$(printf 'https://hostname.tailnet.ts.net:9443/\n|-- / proxy http://127.0.0.1:9000')"
TEST_SERVE_STATUS="$unrelated" run_script \
  "$ROOT_DIR/scripts/configure-tailscale.sh" >/dev/null
assert_contains "$TEST_LOG" 'tailscale serve --https=8443 --bg --yes http://127.0.0.1:8787'
assert_not_matches "$TEST_LOG" 'tailscale serve (reset|--https=9443.*off|funnel)'

printf 'Testing watchdog restarts only Shrinkray...\n'
reset_test
TEST_CONTAINER_HEALTH=unhealthy TEST_HEALTH_OK=0 TEST_SERVE_STATUS="$serve_ok" \
  run_script "$ROOT_DIR/scripts/shrinkray-watchdog.sh" >/dev/null
assert_contains "$TEST_LOG" 'docker compose restart shrinkray'
assert_not_matches "$TEST_LOG" 'docker compose restart (docker|coolify|jellyfin)'

printf 'Testing watchdog restores only the 8443 route...\n'
reset_test
TEST_SERVE_STATUS="$unrelated" run_script \
  "$ROOT_DIR/scripts/shrinkray-watchdog.sh" >/dev/null
assert_contains "$TEST_LOG" 'tailscale serve --https=8443 --bg --yes http://127.0.0.1:8787'
assert_not_matches "$TEST_LOG" 'tailscale serve (--https=443|reset|funnel)'

printf 'Testing private hostnames are absent...\n'
while IFS= read -r hostname; do
  case "$hostname" in
    hostname.tailnet.ts.net|test-host.example.ts.net) ;;
    *) fail "unexpected private hostname in repository: ${hostname}" ;;
  esac
done < <(
  rg -o --no-filename '[A-Za-z0-9.-]+\.ts\.net' "$ROOT_DIR" \
    -g '!*.git*' -g '!dist/**' | sort -u
)

printf 'Testing persistence uses bind mounts and recreation keeps volumes...\n'
assert_contains "$ROOT_DIR/compose.yaml" "\${SHRINKRAY_STATE_DIR:-/var/lib/shrinkray}:/var/lib/shrinkray"
assert_contains "$ROOT_DIR/compose.yaml" "\${SHRINKRAY_MOVIES_PATH:-/media/movies}:/media/movies"
assert_contains "$ROOT_DIR/compose.yaml" "\${SHRINKRAY_TV_PATH:-/media/tv}:/media/tv"
assert_not_matches "$ROOT_DIR/scripts/shrinkray-docker.sh" 'compose (down|rm).*(-v|--volumes)'
assert_contains "$ROOT_DIR/.gitignore" '.env'

printf 'Docker operations safety tests passed.\n'
