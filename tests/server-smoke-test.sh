#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)"
TEMP_DIR="$(mktemp -d)"
MOVIE_ROOT="${TEMP_DIR}/movies"
STATE_DIR="${TEMP_DIR}/state"
SERVER_BIN="${TEMP_DIR}/shrinkray-server"
FAKE_SHRINKRAY="${ROOT_DIR}/tests/fixtures/fake-shrinkray"
SERVER_LOG="${TEMP_DIR}/server.log"
SERVER_PID=""
BASE_URL=""

cleanup() {
  if [ -n "$SERVER_PID" ] && kill -0 "$SERVER_PID" 2>/dev/null; then
    kill "$SERVER_PID"
    wait "$SERVER_PID" 2>/dev/null || true
  fi
  rm -rf -- "$TEMP_DIR"
}
trap cleanup EXIT HUP INT TERM

command -v go >/dev/null 2>&1 || {
  printf 'server smoke test: go is required\n' >&2
  exit 1
}
command -v ffmpeg >/dev/null 2>&1 || {
  printf 'server smoke test: ffmpeg is required\n' >&2
  exit 1
}
command -v ffprobe >/dev/null 2>&1 || {
  printf 'server smoke test: ffprobe is required\n' >&2
  exit 1
}
command -v curl >/dev/null 2>&1 || {
  printf 'server smoke test: curl is required\n' >&2
  exit 1
}

mkdir -p -- "$MOVIE_ROOT"
printf 'Building shrinkray-server...\n'
go -C "$ROOT_DIR" build -o "$SERVER_BIN" ./cmd/shrinkray-server

printf 'Generating a short movie for the dashboard...\n'
ffmpeg -y -hide_banner -loglevel error \
  -f lavfi -i "testsrc2=size=160x120:rate=12:duration=1" \
  -f lavfi -i "sine=frequency=800:sample_rate=44100:duration=1" \
  -map 0:v:0 -map 1:a:0 -c:v mpeg4 -c:a aac \
  "${MOVIE_ROOT}/dashboard-test.mkv"

"$SERVER_BIN" \
  --root "$MOVIE_ROOT" \
  --listen "127.0.0.1:0" \
  --shrinkray-bin "$FAKE_SHRINKRAY" \
  --state-dir "$STATE_DIR" >"$SERVER_LOG" 2>&1 &
SERVER_PID="$!"

for _ in $(seq 1 100); do
  if [ -z "$BASE_URL" ]; then
    BASE_URL="$(sed -n 's/.*listening on http:\/\/\(127\.0\.0\.1:[0-9][0-9]*\).*/http:\/\/\1/p' "$SERVER_LOG" | tail -n 1)"
  fi
  if [ -n "$BASE_URL" ] && curl -fsS "${BASE_URL}/api/health" >"${TEMP_DIR}/health.json" 2>/dev/null; then
    break
  fi
  if ! kill -0 "$SERVER_PID" 2>/dev/null; then
    printf 'server smoke test: server exited during startup\n' >&2
    sed -n '1,160p' "$SERVER_LOG" >&2
    exit 1
  fi
  sleep 0.05
done

grep -q '"status":"ok"' "${TEMP_DIR}/health.json" || {
  printf 'server smoke test: health endpoint did not report ok\n' >&2
  exit 1
}

curl -fsS "${BASE_URL}/api/files?path=" >"${TEMP_DIR}/files.json"
grep -q 'dashboard-test.mkv' "${TEMP_DIR}/files.json" || {
  printf 'server smoke test: movie was missing from file list\n' >&2
  exit 1
}

curl -fsS "${BASE_URL}/api/probe?path=dashboard-test.mkv" >"${TEMP_DIR}/probe.json"
grep -q '"width":160' "${TEMP_DIR}/probe.json" || {
  printf 'server smoke test: ffprobe details were missing\n' >&2
  exit 1
}

curl -fsS -X POST -H 'Content-Type: application/json' \
  --data '{"path":"dashboard-test.mkv","preset":"balanced","container":"mkv","keep_all_audio":false}' \
  "${BASE_URL}/api/jobs" >"${TEMP_DIR}/created.json"
grep -q '"state":"queued"' "${TEMP_DIR}/created.json" || {
  printf 'server smoke test: submitted job was not queued\n' >&2
  exit 1
}

SAW_RUNNING=false
SAW_COMPLETED=false
for _ in $(seq 1 100); do
  curl -fsS "${BASE_URL}/api/jobs" >"${TEMP_DIR}/jobs.json"
  grep -q '"state":"running"' "${TEMP_DIR}/jobs.json" && SAW_RUNNING=true
  if grep -q '"state":"completed"' "${TEMP_DIR}/jobs.json"; then
    SAW_COMPLETED=true
    break
  fi
  sleep 0.05
done

[ "$SAW_RUNNING" = true ] || {
  printf 'server smoke test: job never entered running state\n' >&2
  sed -n '1,200p' "${TEMP_DIR}/jobs.json" >&2
  exit 1
}
[ "$SAW_COMPLETED" = true ] || {
  printf 'server smoke test: job did not complete\n' >&2
  sed -n '1,200p' "${TEMP_DIR}/jobs.json" >&2
  exit 1
}
[ -s "${MOVIE_ROOT}/dashboard-test.shrunk.mkv" ] || {
  printf 'server smoke test: fake shrinkray output is missing\n' >&2
  exit 1
}

printf 'Server smoke test passed.\n'
