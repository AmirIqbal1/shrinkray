#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)"
TEMP_DIR="$(mktemp -d)"
MOVIES_ROOT="${TEMP_DIR}/movies"
TV_ROOT="${TEMP_DIR}/tv"
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

mkdir -p -- "$MOVIES_ROOT" "$TV_ROOT"
printf 'Building shrinkray-server...\n'
go -C "$ROOT_DIR" build -o "$SERVER_BIN" ./cmd/shrinkray-server

printf 'Generating short movies for both dashboard libraries...\n'
ffmpeg -y -hide_banner -loglevel error \
  -f lavfi -i "testsrc2=size=160x120:rate=12:duration=1" \
  -f lavfi -i "sine=frequency=800:sample_rate=44100:duration=1" \
  -map 0:v:0 -map 1:a:0 -c:v mpeg4 -c:a aac \
  "${MOVIES_ROOT}/dashboard-movie.mkv"
ffmpeg -y -hide_banner -loglevel error \
  -f lavfi -i "testsrc2=size=176x144:rate=12:duration=1" \
  -f lavfi -i "sine=frequency=900:sample_rate=44100:duration=1" \
  -map 0:v:0 -map 1:a:0 -c:v mpeg4 -c:a aac \
  "${TV_ROOT}/dashboard-episode.mkv"

"$SERVER_BIN" \
  --root "Movies=${MOVIES_ROOT}" \
  --root "TV=${TV_ROOT}" \
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
grep -q '"version":"0.2.0"' "${TEMP_DIR}/health.json" || {
  printf 'server smoke test: server version was not 0.2.0\n' >&2
  exit 1
}
grep -q '"id":"movies","label":"Movies"' "${TEMP_DIR}/health.json" || {
  printf 'server smoke test: Movies root was missing from health response\n' >&2
  exit 1
}
grep -q '"id":"tv","label":"TV"' "${TEMP_DIR}/health.json" || {
  printf 'server smoke test: TV root was missing from health response\n' >&2
  exit 1
}
if grep -Fq "$MOVIES_ROOT" "${TEMP_DIR}/health.json" || grep -Fq "$TV_ROOT" "${TEMP_DIR}/health.json"; then
  printf 'server smoke test: health response exposed an absolute root path\n' >&2
  exit 1
fi

curl -fsS "${BASE_URL}/api/files?root=movies&path=" >"${TEMP_DIR}/movies-files.json"
grep -q 'dashboard-movie.mkv' "${TEMP_DIR}/movies-files.json" || {
  printf 'server smoke test: movie was missing from Movies list\n' >&2
  exit 1
}
curl -fsS "${BASE_URL}/api/files?root=tv&path=" >"${TEMP_DIR}/tv-files.json"
grep -q 'dashboard-episode.mkv' "${TEMP_DIR}/tv-files.json" || {
  printf 'server smoke test: episode was missing from TV list\n' >&2
  exit 1
}

curl -fsS "${BASE_URL}/api/probe?root=movies&path=dashboard-movie.mkv" >"${TEMP_DIR}/probe.json"
grep -q '"width":160' "${TEMP_DIR}/probe.json" || {
  printf 'server smoke test: ffprobe details were missing\n' >&2
  exit 1
}

curl -fsS -X POST -H 'Content-Type: application/json' \
  --data '{"root_id":"movies","path":"dashboard-movie.mkv","preset":"balanced","container":"mkv","keep_all_audio":false}' \
  "${BASE_URL}/api/jobs" >"${TEMP_DIR}/created-movie.json"
curl -fsS -X POST -H 'Content-Type: application/json' \
  --data '{"root_id":"tv","path":"dashboard-episode.mkv","preset":"smaller","container":"mkv","keep_all_audio":false}' \
  "${BASE_URL}/api/jobs" >"${TEMP_DIR}/created-tv.json"
if ! grep -q '"state":"queued"' "${TEMP_DIR}/created-movie.json" || ! grep -q '"state":"queued"' "${TEMP_DIR}/created-tv.json"; then
  printf 'server smoke test: submitted jobs were not queued\n' >&2
  exit 1
fi

SAW_SEQUENTIAL=false
SAW_BOTH_COMPLETED=false
for _ in $(seq 1 100); do
  curl -fsS "${BASE_URL}/api/jobs" >"${TEMP_DIR}/jobs.json"
	if grep -q '"state":"running"' "${TEMP_DIR}/jobs.json" && grep -q '"state":"queued"' "${TEMP_DIR}/jobs.json"; then
		SAW_SEQUENTIAL=true
	fi
  if grep -q '"state":"completed".*"state":"completed"' "${TEMP_DIR}/jobs.json"; then
    SAW_BOTH_COMPLETED=true
    break
  fi
  sleep 0.05
done

[ "$SAW_SEQUENTIAL" = true ] || {
  printf 'server smoke test: jobs did not run sequentially through one queue\n' >&2
  sed -n '1,200p' "${TEMP_DIR}/jobs.json" >&2
  exit 1
}
[ "$SAW_BOTH_COMPLETED" = true ] || {
  printf 'server smoke test: both jobs did not complete\n' >&2
  sed -n '1,200p' "${TEMP_DIR}/jobs.json" >&2
  exit 1
}
[ -s "${MOVIES_ROOT}/dashboard-movie.shrunk.mkv" ] || {
  printf 'server smoke test: fake Movies output is missing\n' >&2
  exit 1
}
[ -s "${TV_ROOT}/dashboard-episode.shrunk.mkv" ] || {
  printf 'server smoke test: fake TV output is missing\n' >&2
  exit 1
}

printf 'Server smoke test passed.\n'
