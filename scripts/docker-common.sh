#!/usr/bin/env bash
# Shared, side-effect-free helpers for Shrinkray's Docker operations.

if [ "${SHRINKRAY_DOCKER_COMMON_LOADED:-0}" = 1 ]; then
  return 0
fi
SHRINKRAY_DOCKER_COMMON_LOADED=1

SHRINKRAY_SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)"
SHRINKRAY_REPO_DIR="${SHRINKRAY_REPO_DIR:-$(cd -- "${SHRINKRAY_SCRIPT_DIR}/.." && pwd -P)}"
SHRINKRAY_ENV_FILE="${SHRINKRAY_ENV_FILE:-${SHRINKRAY_REPO_DIR}/.env}"

docker_common_die() {
  printf 'shrinkray-docker: %s\n' "$*" >&2
  return 1
}

load_shrinkray_env() {
  local line key value

  [ -f "$SHRINKRAY_ENV_FILE" ] || {
    docker_common_die "missing environment file: ${SHRINKRAY_ENV_FILE}"
    return 1
  }
  [ ! -L "$SHRINKRAY_ENV_FILE" ] || {
    docker_common_die "refusing symlinked environment file: ${SHRINKRAY_ENV_FILE}"
    return 1
  }

  while IFS= read -r line || [ -n "$line" ]; do
    line="${line%$'\r'}"
    case "$line" in
      ''|'#'*) continue ;;
      *=*) ;;
      *)
        docker_common_die "invalid line in ${SHRINKRAY_ENV_FILE}"
        return 1
        ;;
    esac
    key="${line%%=*}"
    value="${line#*=}"
    case "$key" in
      SHRINKRAY_PORT|SHRINKRAY_UID|SHRINKRAY_GID|SHRINKRAY_MEDIA_GID|\
      SHRINKRAY_STATE_DIR|SHRINKRAY_MOVIES_PATH|SHRINKRAY_TV_PATH|\
      SHRINKRAY_TAILSCALE_HTTPS_PORT|SHRINKRAY_HWACCEL|SHRINKRAY_DRM_RENDER_DEVICE) ;;
      *)
        docker_common_die "unsupported setting in ${SHRINKRAY_ENV_FILE}: ${key}"
        return 1
        ;;
    esac
    case "$value" in
      \"*\") value="${value#\"}"; value="${value%\"}" ;;
      \'*\') value="${value#\'}"; value="${value%\'}" ;;
    esac
    printf -v "$key" '%s' "$value"
  done < "$SHRINKRAY_ENV_FILE"

  : "${SHRINKRAY_PORT:=8787}"
  : "${SHRINKRAY_TAILSCALE_HTTPS_PORT:=8443}"
  : "${SHRINKRAY_HWACCEL:=auto}"
  export SHRINKRAY_PORT SHRINKRAY_UID SHRINKRAY_GID SHRINKRAY_MEDIA_GID
  export SHRINKRAY_STATE_DIR SHRINKRAY_MOVIES_PATH SHRINKRAY_TV_PATH
  export SHRINKRAY_TAILSCALE_HTTPS_PORT
  export SHRINKRAY_HWACCEL SHRINKRAY_DRM_RENDER_DEVICE

  case "$SHRINKRAY_HWACCEL" in
    auto|none|dri|nvidia) ;;
    *) docker_common_die 'SHRINKRAY_HWACCEL must be auto, none, dri, or nvidia'; return 1 ;;
  esac

  local numeric
  for numeric in SHRINKRAY_PORT SHRINKRAY_UID SHRINKRAY_GID \
    SHRINKRAY_MEDIA_GID SHRINKRAY_TAILSCALE_HTTPS_PORT; do
    value="${!numeric:-}"
    [[ "$value" =~ ^[0-9]+$ ]] || {
      docker_common_die "${numeric} must be numeric"
      return 1
    }
  done
  if [ "$SHRINKRAY_PORT" -lt 1 ] || [ "$SHRINKRAY_PORT" -gt 65535 ]; then
    docker_common_die 'SHRINKRAY_PORT must be between 1 and 65535'
    return 1
  fi
  if [ "$SHRINKRAY_TAILSCALE_HTTPS_PORT" -lt 1 ] ||
    [ "$SHRINKRAY_TAILSCALE_HTTPS_PORT" -gt 65535 ]; then
    docker_common_die 'SHRINKRAY_TAILSCALE_HTTPS_PORT must be between 1 and 65535'
    return 1
  fi

  SHRINKRAY_BACKEND="http://127.0.0.1:${SHRINKRAY_PORT}"
  SHRINKRAY_HEALTH_URL="${SHRINKRAY_BACKEND}/api/health"
  export SHRINKRAY_BACKEND SHRINKRAY_HEALTH_URL
}

run_compose() {
  (
    cd -- "$SHRINKRAY_REPO_DIR" || exit
    select_hardware_compose
    docker compose "${SHRINKRAY_COMPOSE_HARDWARE_ARGS[@]}" "$@"
  )
}

find_render_device() {
  local device
  if [ -n "${SHRINKRAY_DRM_RENDER_DEVICE:-}" ]; then
    [ -c "$SHRINKRAY_DRM_RENDER_DEVICE" ] || return 1
    case "$SHRINKRAY_DRM_RENDER_DEVICE" in /dev/dri/renderD*) printf '%s\n' "$SHRINKRAY_DRM_RENDER_DEVICE"; return 0 ;; esac
    return 1
  fi
  for device in /dev/dri/renderD*; do
    [ -c "$device" ] || continue
    printf '%s\n' "$device"
    return 0
  done
  return 1
}

nvidia_runtime_installed() {
  docker info --format '{{json .Runtimes}}' 2>/dev/null | grep -q '"nvidia"'
}

nvidia_container_support_available() {
  nvidia_runtime_installed || return 1
  command -v nvidia-smi >/dev/null 2>&1 || return 1
  nvidia-smi -L >/dev/null 2>&1
}

select_hardware_compose() {
  local device mode
  SHRINKRAY_COMPOSE_HARDWARE_ARGS=()
  mode="${SHRINKRAY_HWACCEL:-auto}"
  if [ "$mode" = none ]; then
    return 0
  fi
  device="$(find_render_device 2>/dev/null || true)"
  if { [ "$mode" = auto ] || [ "$mode" = dri ]; } && [ -n "$device" ]; then
    SHRINKRAY_DRM_RENDER_DEVICE="$device"
    SHRINKRAY_DRM_RENDER_GID="$(stat -c '%g' -- "$device")"
    export SHRINKRAY_DRM_RENDER_DEVICE SHRINKRAY_DRM_RENDER_GID
    SHRINKRAY_COMPOSE_HARDWARE_ARGS=(-f compose.yaml -f compose.hwaccel.yaml)
    return 0
  fi
  if [ "$mode" = dri ]; then
    docker_common_die 'no valid DRM render device was found; set SHRINKRAY_DRM_RENDER_DEVICE to /dev/dri/renderD*'
    return 1
  fi
  if { [ "$mode" = auto ] || [ "$mode" = nvidia ]; } && nvidia_container_support_available; then
    SHRINKRAY_COMPOSE_HARDWARE_ARGS=(-f compose.yaml -f compose.nvidia.yaml)
    return 0
  fi
  if [ "$mode" = nvidia ]; then
    docker_common_die 'usable NVIDIA GPU/container runtime support is unavailable'
    return 1
  fi
}

shrinkray_container_id() {
  run_compose ps --all --quiet shrinkray 2>/dev/null | head -n 1
}

api_health_ok() {
  local response
  response="$(curl --fail --silent --show-error "$SHRINKRAY_HEALTH_URL" 2>/dev/null)" ||
    return 1
  printf '%s' "$response" |
    grep -Eq '"status"[[:space:]]*:[[:space:]]*"ok"'
}

configured_user_can_access_directory() {
  local directory="$1" current_uid
  current_uid="$(id -u)"
  if [ "$current_uid" = "$SHRINKRAY_UID" ]; then
    [ -d "$directory" ] && [ -r "$directory" ] && [ -w "$directory" ] &&
      [ -x "$directory" ]
  elif [ "$current_uid" = 0 ] && command -v setpriv >/dev/null 2>&1; then
    setpriv \
      --reuid "$SHRINKRAY_UID" \
      --regid "$SHRINKRAY_GID" \
      --groups "$SHRINKRAY_MEDIA_GID" \
      -- test -d "$directory" &&
      setpriv \
        --reuid "$SHRINKRAY_UID" \
        --regid "$SHRINKRAY_GID" \
        --groups "$SHRINKRAY_MEDIA_GID" \
        -- test -r "$directory" -a -w "$directory" -a -x "$directory"
  else
    return 1
  fi
}

wait_for_shrinkray_health() {
  local attempts="${SHRINKRAY_HEALTH_ATTEMPTS:-90}" container_id health attempt
  for ((attempt = 1; attempt <= attempts; attempt++)); do
    container_id="$(shrinkray_container_id)"
    if [ -n "$container_id" ]; then
      health="$(docker inspect --format '{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}' "$container_id" 2>/dev/null || true)"
      if [ "$health" = healthy ] && api_health_ok; then
        return 0
      fi
    fi
    sleep "${SHRINKRAY_HEALTH_SLEEP:-1}"
  done
  return 1
}

ss_port_lines() {
  local status="$1" port="$2"
  printf '%s\n' "$status" |
    awk -v suffix=":${port}" '$4 ~ (suffix "$")'
}

serve_listener_exists() {
  local status="$1" wanted_port="$2"
  printf '%s\n' "$status" | awk -v wanted="$wanted_port" '
    function listener_port(line, host) {
      host=line
      sub(/^[[:space:]]*https:\/\//, "", host)
      sub(/[[:space:]\/].*$/, "", host)
      if (host ~ /:[0-9]+$/) { sub(/^.*:/, "", host); return host }
      return 443
    }
    /^[[:space:]]*https:\/\// && listener_port($0) == wanted { found=1 }
    END { exit(found ? 0 : 1) }
  '
}

serve_backend_at_port() {
  local status="$1" wanted_port="$2" wanted_backend="$3"
  printf '%s\n' "$status" | awk -v wanted="$wanted_port" -v backend="$wanted_backend" '
    function listener_port(line, host) {
      host=line
      sub(/^[[:space:]]*https:\/\//, "", host)
      sub(/[[:space:]\/].*$/, "", host)
      if (host ~ /:[0-9]+$/) { sub(/^.*:/, "", host); return host }
      return 443
    }
    /^[[:space:]]*https:\/\// { port=listener_port($0); next }
    port == wanted && index($0, backend) { found=1 }
    END { exit(found ? 0 : 1) }
  '
}

serve_port_is_only_backend() {
  local status="$1" wanted_port="$2" wanted_backend="$3"
  printf '%s\n' "$status" | awk -v wanted="$wanted_port" -v backend="$wanted_backend" '
    function listener_port(line, host) {
      host=line
      sub(/^[[:space:]]*https:\/\//, "", host)
      sub(/[[:space:]\/].*$/, "", host)
      if (host ~ /:[0-9]+$/) { sub(/^.*:/, "", host); return host }
      return 443
    }
    /^[[:space:]]*https:\/\// { port=listener_port($0); next }
    port == wanted && /^[[:space:]]*\|--/ {
      routes++
      if (index($0, backend)) matches++
    }
    END { exit(routes == 1 && matches == 1 ? 0 : 1) }
  '
}

dns_name_from_json() {
  local json="$1" name
  name="$(
    printf '%s' "$json" |
      grep -Eom1 '"DNSName"[[:space:]]*:[[:space:]]*"[^"]+"' |
      sed -E 's/^[^:]+:[[:space:]]*"([^"]+)"$/\1/'
  )" || true
  printf '%s' "${name%.}"
}
