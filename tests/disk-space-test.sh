#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)"
TEMP_DIR="$(mktemp -d)"
FAKE_BIN="${TEMP_DIR}/bin"
TEST_TMP="${TEMP_DIR}/work"
COUNTER="${TEMP_DIR}/disk-counter"
MARKER="${TEMP_DIR}/encode-marker"
LOG="${TEMP_DIR}/shrinkray.log"

cleanup() {
  rm -rf -- "$TEMP_DIR"
}
trap cleanup EXIT HUP INT TERM

mkdir -p -- "$FAKE_BIN" "$TEST_TMP"
ln -s -- "${ROOT_DIR}/tests/fixtures/disk-space-stat-fake" "${FAKE_BIN}/stat"
ln -s -- "${ROOT_DIR}/tests/fixtures/disk-space-ffmpeg-fake" "${FAKE_BIN}/ffmpeg"
ln -s -- "${ROOT_DIR}/tests/fixtures/disk-space-ffprobe-fake" "${FAKE_BIN}/ffprobe"

run_shrinkray() {
  PATH="${FAKE_BIN}:${PATH}" \
  TMPDIR="$TEST_TMP" \
  SHRINKRAY_TEST_DISK_COUNTER="$COUNTER" \
  SHRINKRAY_TEST_ENCODE_MARKER="$MARKER" \
  SHRINKRAY_TEST_DISK_MODE="$1" \
    bash "${ROOT_DIR}/shrinkray" "${@:2}"
}

INPUT="${TEMP_DIR}/original.mkv"
printf 'original remains untouched\n' > "$INPUT"
if run_shrinkray insufficient "$INPUT" --size 1 --codec hevc -y >"$LOG" 2>&1; then
  printf 'disk space test: insufficient preflight unexpectedly succeeded\n' >&2
  exit 1
fi
grep -q 'Not enough free disk space' "$LOG"
grep -q 'Shrinkray will not start this encode' "$LOG"
grep -q 'Available:' "$LOG"
grep -q 'Required:' "$LOG"
grep -q 'Safety reserve:' "$LOG"
[ ! -e "$MARKER" ] || {
  printf 'disk space test: encoder started after failed preflight\n' >&2
  exit 1
}
[ ! -e "${TEMP_DIR}/original.shrunk.mkv.part" ]
[ -z "$(find "$TEST_TMP" -mindepth 1 -print -quit)" ] || {
  printf 'disk space test: preflight failure created a work directory\n' >&2
  exit 1
}

rm -f -- "$COUNTER" "$MARKER" "$LOG"
if run_shrinkray insufficient "$INPUT" --size 1 --codec hevc --replace-original >"$LOG" 2>&1; then
  printf 'disk space test: replacement ignored insufficient preflight\n' >&2
  exit 1
fi
grep -q '^original remains untouched$' "$INPUT"
[ -z "$(find "$TEMP_DIR" -maxdepth 1 -name '.original.mkv.shrinkray-*' -print -quit)" ] || {
  printf 'disk space test: replacement preflight left a transaction artifact\n' >&2
  exit 1
}

rm -f -- "$COUNTER" "$MARKER" "$LOG"
if timeout --signal=TERM 1s env PATH="${FAKE_BIN}:${PATH}" TMPDIR="$TEST_TMP" \
  SHRINKRAY_TEST_DISK_COUNTER="$COUNTER" SHRINKRAY_TEST_ENCODE_MARKER="$MARKER" SHRINKRAY_TEST_DISK_MODE=cancel \
  bash "${ROOT_DIR}/shrinkray" "$INPUT" --size 1 --codec av1 --replace-original >"$LOG" 2>&1; then
  printf 'disk space test: cancelled replacement unexpectedly succeeded\n' >&2
  exit 1
fi
grep -q '^original remains untouched$' "$INPUT"
[ -z "$(find "$TEMP_DIR" -maxdepth 1 -name '.original.mkv.shrinkray-*' -print -quit)" ] || {
  printf 'disk space test: cancelled replacement left a transaction artifact\n' >&2
  exit 1
}

rm -f -- "$COUNTER" "$MARKER" "$LOG"
if run_shrinkray critical "$INPUT" --size 1 --codec av1 -y >"$LOG" 2>&1; then
  printf 'disk space test: critical low-space encode unexpectedly succeeded\n' >&2
  exit 1
fi
grep -q 'Critical low disk space' "$LOG"
grep -q '^original remains untouched$' "$INPUT"
[ ! -e "${TEMP_DIR}/original.shrunk.mkv.part" ]
[ -z "$(find "$TEST_TMP" -mindepth 1 -print -quit)" ] || {
  printf 'disk space test: critical abort left encoder work files\n' >&2
  exit 1
}

rm -f -- "$COUNTER" "$MARKER" "$LOG"
if run_shrinkray critical "$INPUT" --size 1 --codec av1 --replace-original >"$LOG" 2>&1; then
  printf 'disk space test: critical replacement unexpectedly succeeded\n' >&2
  exit 1
fi
grep -q '^original remains untouched$' "$INPUT"
[ -z "$(find "$TEMP_DIR" -maxdepth 1 -name '.original.mkv.shrinkray-*' -print -quit)" ] || {
  printf 'disk space test: critical replacement left a transaction artifact\n' >&2
  exit 1
}

rm -f -- "$COUNTER" "$MARKER" "$LOG"
BATCH="${TEMP_DIR}/batch"
mkdir -p -- "$BATCH"
printf 'first source\n' > "${BATCH}/one.mkv"
printf 'second source\n' > "${BATCH}/two.mkv"
if run_shrinkray batch --batch "$BATCH" --size 1 --codec hevc -y >"$LOG" 2>&1; then
  printf 'disk space test: partially failed batch unexpectedly returned success\n' >&2
  exit 1
fi
[ ! -e "${BATCH}/one.shrunk.mkv" ]
[ -s "${BATCH}/two.shrunk.mkv" ] || {
  printf 'disk space test: safe batch item did not continue after low-space item\n' >&2
  exit 1
}
grep -q 'Batch summary: 1 successful, 1 failed' "$LOG"
grep -q 'Disk space: OK' "$LOG"

rm -f -- "$COUNTER" "$MARKER" "$LOG"
BATCH_REPLACE="${TEMP_DIR}/batch-replace"
mkdir -p -- "$BATCH_REPLACE"
printf 'first original\n%090d' 0 >"${BATCH_REPLACE}/one.mkv"
printf 'second original\n%090d' 0 >"${BATCH_REPLACE}/two.mkv"
if run_shrinkray batch --batch "$BATCH_REPLACE" --size 1 --codec hevc --replace-original >"$LOG" 2>&1; then
  printf 'disk space test: partially failed replacement batch unexpectedly returned success\n' >&2
  exit 1
fi
grep -q '^first original' "${BATCH_REPLACE}/one.mkv"
grep -q '^temporary encoded output$' "${BATCH_REPLACE}/two.mkv"
grep -q 'Batch summary: 1 safely replaced, 1 replacement failed' "$LOG"
[ -z "$(find "$BATCH_REPLACE" -maxdepth 1 -name '.*.shrinkray-*' -print -quit)" ] || {
  printf 'disk space test: batch replacement retained a transaction artifact\n' >&2
  exit 1
}

printf 'Disk-space safety test passed.\n'
