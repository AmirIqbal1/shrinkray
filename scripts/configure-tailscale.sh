#!/usr/bin/env bash
# Configure only Shrinkray's private, non-443 Tailscale Serve listener.
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)"
# shellcheck source=scripts/docker-common.sh
. "${SCRIPT_DIR}/docker-common.sh"

load_shrinkray_env

[ "$SHRINKRAY_TAILSCALE_HTTPS_PORT" != 443 ] || {
  printf 'configure-tailscale: refusing HTTPS port 443; it is reserved for the host proxy.\n' >&2
  exit 1
}
[ "$SHRINKRAY_TAILSCALE_HTTPS_PORT" != "$SHRINKRAY_PORT" ] || {
  printf 'configure-tailscale: HTTPS and backend ports must differ.\n' >&2
  exit 1
}

for command_name in curl ss tailscale; do
  command -v "$command_name" >/dev/null 2>&1 || {
    printf 'configure-tailscale: %s is required.\n' "$command_name" >&2
    exit 1
  }
done

printf 'Waiting for local Shrinkray health...\n'
for ((attempt = 1; attempt <= ${SHRINKRAY_HEALTH_ATTEMPTS:-90}; attempt++)); do
  api_health_ok && break
  sleep "${SHRINKRAY_HEALTH_SLEEP:-1}"
done
api_health_ok || {
  printf 'configure-tailscale: local health check failed: %s\n' "$SHRINKRAY_HEALTH_URL" >&2
  exit 1
}

tailscale_status="$(tailscale status)"
status_json="$(tailscale status --json)"
serve_status="$(tailscale serve status)"
tailscale serve status --json >/dev/null
ss_status="$(ss -ltnp)"

[ -n "$tailscale_status" ] || {
  printf 'configure-tailscale: Tailscale is not connected.\n' >&2
  exit 1
}
printf '%s' "$status_json" |
  grep -Eq '"BackendState"[[:space:]]*:[[:space:]]*"Running"' || {
  printf 'configure-tailscale: Tailscale backend is not running.\n' >&2
  exit 1
}

if serve_backend_at_port "$serve_status" "$SHRINKRAY_TAILSCALE_HTTPS_PORT" "$SHRINKRAY_BACKEND"; then
  printf 'Tailscale Serve already routes HTTPS port %s to Shrinkray; unchanged.\n' \
    "$SHRINKRAY_TAILSCALE_HTTPS_PORT"
elif serve_listener_exists "$serve_status" "$SHRINKRAY_TAILSCALE_HTTPS_PORT"; then
  printf 'configure-tailscale: HTTPS port %s belongs to an unrelated Serve route; refusing.\n' \
    "$SHRINKRAY_TAILSCALE_HTTPS_PORT" >&2
  exit 1
elif [ -n "$(ss_port_lines "$ss_status" "$SHRINKRAY_TAILSCALE_HTTPS_PORT")" ]; then
  printf 'configure-tailscale: HTTPS port %s is already listening; refusing.\n' \
    "$SHRINKRAY_TAILSCALE_HTTPS_PORT" >&2
  exit 1
else
  tailscale serve \
    --https="$SHRINKRAY_TAILSCALE_HTTPS_PORT" \
    --bg \
    --yes \
    "$SHRINKRAY_BACKEND"
  serve_status="$(tailscale serve status)"
  tailscale serve status --json >/dev/null
  serve_backend_at_port "$serve_status" "$SHRINKRAY_TAILSCALE_HTTPS_PORT" "$SHRINKRAY_BACKEND" || {
    printf 'configure-tailscale: configured route did not pass verification.\n' >&2
    exit 1
  }
fi

dns_name="$(dns_name_from_json "$status_json")"
if [[ "$dns_name" == *.ts.net ]]; then
  printf 'Private Shrinkray URL: https://%s:%s/\n' \
    "$dns_name" "$SHRINKRAY_TAILSCALE_HTTPS_PORT"
else
  printf 'Tailscale route is ready on private HTTPS port %s.\n' \
    "$SHRINKRAY_TAILSCALE_HTTPS_PORT"
fi
