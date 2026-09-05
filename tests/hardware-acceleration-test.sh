#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)"
TEMP_DIR="$(mktemp -d)"
FAKE_BIN="${TEMP_DIR}/bin"
DRI_DIR="${TEMP_DIR}/dri"
COMMAND_LOG="${TEMP_DIR}/commands.log"
COUNTER="${TEMP_DIR}/disk-counter"
FALLBACK_LEAK="${TEMP_DIR}/fallback-leak"
INPUT="${TEMP_DIR}/movie.mkv"
PART="${TEMP_DIR}/movie.shrunk.mkv.part"

cleanup() { rm -rf -- "$TEMP_DIR"; }
trap cleanup EXIT HUP INT TERM
mkdir -p -- "$FAKE_BIN" "$DRI_DIR"
ln -s -- "${ROOT_DIR}/tests/fixtures/hardware-ffmpeg-fake" "${FAKE_BIN}/ffmpeg"
ln -s -- "${ROOT_DIR}/tests/fixtures/disk-space-ffprobe-fake" "${FAKE_BIN}/ffprobe"
ln -s -- "${ROOT_DIR}/tests/fixtures/disk-space-stat-fake" "${FAKE_BIN}/stat"
: >"${DRI_DIR}/renderD128"
: >"${DRI_DIR}/renderD129"
printf 'original movie\n' >"$INPUT"

run_capabilities() {
  PATH="${FAKE_BIN}:${PATH}" \
  SHRINKRAY_DRI_DIR="$DRI_DIR" \
  SHRINKRAY_ALLOW_REGULAR_RENDER_DEVICES=1 \
  SHRINKRAY_TEST_LISTED="$1" \
  SHRINKRAY_TEST_WORKING="$2" \
  SHRINKRAY_TEST_QSV_DEVICE="${3-}" \
    bash "${ROOT_DIR}/shrinkray" capabilities --json
}

capabilities="$(run_capabilities qsv,vaapi,nvenc qsv,vaapi,nvenc "${DRI_DIR}/renderD129")"
[[ "$capabilities" == *'"auto_selected":"qsv"'* ]] || { printf 'hardware test: QSV was not first priority: %s\n' "$capabilities" >&2; exit 1; }
[[ "$capabilities" == *'"qsv":true'* ]] || { printf 'hardware test: second QSV render device was not probed\n' >&2; exit 1; }

capabilities="$(run_capabilities qsv,vaapi,nvenc vaapi,nvenc)"
[[ "$capabilities" == *'"auto_selected":"vaapi"'* ]] || { printf 'hardware test: VAAPI was not selected after QSV failure: %s\n' "$capabilities" >&2; exit 1; }
capabilities="$(run_capabilities nvenc nvenc)"
[[ "$capabilities" == *'"auto_selected":"nvenc"'* ]] || { printf 'hardware test: NVENC was not selected: %s\n' "$capabilities" >&2; exit 1; }
capabilities="$(run_capabilities qsv '')"
[[ "$capabilities" == *'"qsv":false'* && "$capabilities" == *'"auto_selected":"software"'* ]] || { printf 'hardware test: failed runtime probe was trusted: %s\n' "$capabilities" >&2; exit 1; }

for backend in qsv vaapi nvenc; do
  if PATH="${FAKE_BIN}:${PATH}" SHRINKRAY_DRI_DIR="$DRI_DIR" SHRINKRAY_ALLOW_REGULAR_RENDER_DEVICES=1 \
    SHRINKRAY_TEST_LISTED="$backend" SHRINKRAY_TEST_WORKING='' \
    bash "${ROOT_DIR}/shrinkray" "$INPUT" --size 1 --encoder "$backend" -y >"${TEMP_DIR}/${backend}.log" 2>&1; then
    printf 'hardware test: unavailable explicit %s unexpectedly succeeded\n' "$backend" >&2
    exit 1
  fi
  grep -qi "$backend\|NVIDIA NVENC\|Intel QSV" "${TEMP_DIR}/${backend}.log"
done

: >"$COMMAND_LOG"
PATH="${FAKE_BIN}:${PATH}" SHRINKRAY_DRI_DIR="$DRI_DIR" SHRINKRAY_ALLOW_REGULAR_RENDER_DEVICES=1 \
SHRINKRAY_TEST_LISTED='' SHRINKRAY_TEST_WORKING='' SHRINKRAY_TEST_COMMAND_LOG="$COMMAND_LOG" \
SHRINKRAY_TEST_DISK_COUNTER="$COUNTER" SHRINKRAY_TEST_DISK_MODE=enough \
  bash "${ROOT_DIR}/shrinkray" "$INPUT" --size 1 --encoder software --machine-progress -y >"${TEMP_DIR}/software.log" 2>&1
[ "$(grep -c '^software ' "$COMMAND_LOG")" -eq 2 ] || { printf 'hardware test: software HEVC was not two-pass\n' >&2; exit 1; }

rm -f -- "$COMMAND_LOG" "$COUNTER"
PATH="${FAKE_BIN}:${PATH}" SHRINKRAY_DRI_DIR="$DRI_DIR" SHRINKRAY_ALLOW_REGULAR_RENDER_DEVICES=1 \
SHRINKRAY_TEST_LISTED=qsv SHRINKRAY_TEST_WORKING=qsv SHRINKRAY_TEST_QSV_DEVICE="${DRI_DIR}/renderD128" \
SHRINKRAY_TEST_COMMAND_LOG="$COMMAND_LOG" SHRINKRAY_TEST_DISK_COUNTER="$COUNTER" SHRINKRAY_TEST_DISK_MODE=enough \
  bash "${ROOT_DIR}/shrinkray" "$INPUT" --size 1 --encoder qsv --machine-progress -y >"${TEMP_DIR}/hardware.log" 2>&1
grep -q 'Encoding with HEVC hardware — Intel QSV' "${TEMP_DIR}/hardware.log"
if grep -q 'pass [12] of 2' "${TEMP_DIR}/hardware.log"; then
  printf 'hardware test: single-pass hardware encode claimed software passes\n' >&2
  exit 1
fi
grep -q '^progress=end$' "${TEMP_DIR}/hardware.log"
[ "$(grep -c '^qsv ' "$COMMAND_LOG")" -eq 1 ] || { printf 'hardware test: QSV encode was not single-pass\n' >&2; exit 1; }

rm -f -- "$COMMAND_LOG" "$COUNTER" "$FALLBACK_LEAK"
PATH="${FAKE_BIN}:${PATH}" SHRINKRAY_DRI_DIR="$DRI_DIR" SHRINKRAY_ALLOW_REGULAR_RENDER_DEVICES=1 \
SHRINKRAY_TEST_LISTED=qsv SHRINKRAY_TEST_WORKING=qsv SHRINKRAY_TEST_QSV_DEVICE="${DRI_DIR}/renderD128" \
SHRINKRAY_TEST_FAIL_HARDWARE=true SHRINKRAY_TEST_COMMAND_LOG="$COMMAND_LOG" \
SHRINKRAY_TEST_PART_PATH="$PART" SHRINKRAY_TEST_FALLBACK_LEAK="$FALLBACK_LEAK" \
SHRINKRAY_TEST_DISK_COUNTER="$COUNTER" SHRINKRAY_TEST_DISK_MODE=enough \
  bash "${ROOT_DIR}/shrinkray" "$INPUT" --size 1 --encoder auto --machine-progress -y >"${TEMP_DIR}/fallback.log" 2>&1
grep -q 'Encoding with HEVC hardware — Intel QSV' "${TEMP_DIR}/fallback.log"
grep -q 'Hardware encoder unavailable; retrying with software HEVC.' "${TEMP_DIR}/fallback.log"
grep -q '^progress=end$' "${TEMP_DIR}/fallback.log"
[ ! -e "$FALLBACK_LEAK" ] || { printf 'hardware test: partial output was not removed before fallback\n' >&2; exit 1; }
grep -q '^original movie$' "$INPUT"

REPLACE_INPUT="${TEMP_DIR}/replace.mkv"
printf 'original movie\n%090d' 0 >"$REPLACE_INPUT"
rm -f -- "$COMMAND_LOG" "$COUNTER" "$FALLBACK_LEAK"
PATH="${FAKE_BIN}:${PATH}" SHRINKRAY_DRI_DIR="$DRI_DIR" SHRINKRAY_ALLOW_REGULAR_RENDER_DEVICES=1 \
SHRINKRAY_TEST_LISTED=qsv SHRINKRAY_TEST_WORKING=qsv SHRINKRAY_TEST_QSV_DEVICE="${DRI_DIR}/renderD128" \
SHRINKRAY_TEST_COMMAND_LOG="$COMMAND_LOG" SHRINKRAY_TEST_DISK_COUNTER="$COUNTER" SHRINKRAY_TEST_DISK_MODE=enough \
  bash "${ROOT_DIR}/shrinkray" "$REPLACE_INPUT" --size 1 --encoder qsv --replace-original --machine-progress >"${TEMP_DIR}/replace-hardware.log" 2>&1
grep -q '^encoded output$' "$REPLACE_INPUT"
[ -z "$(find "$TEMP_DIR" -maxdepth 1 -name '.replace.mkv.shrinkray-*' -print -quit)" ] || {
  printf 'hardware test: successful replacement retained transaction artifacts\n' >&2
  exit 1
}

printf 'original movie\n%090d' 0 >"$REPLACE_INPUT"
rm -f -- "$COMMAND_LOG" "$COUNTER" "$FALLBACK_LEAK"
PATH="${FAKE_BIN}:${PATH}" SHRINKRAY_DRI_DIR="$DRI_DIR" SHRINKRAY_ALLOW_REGULAR_RENDER_DEVICES=1 \
SHRINKRAY_TEST_LISTED=qsv SHRINKRAY_TEST_WORKING=qsv SHRINKRAY_TEST_QSV_DEVICE="${DRI_DIR}/renderD128" \
SHRINKRAY_TEST_FAIL_HARDWARE=true SHRINKRAY_TEST_EXPECT_OUTPUT_CLEAN=true \
SHRINKRAY_TEST_SOURCE_PATH="$REPLACE_INPUT" SHRINKRAY_TEST_FALLBACK_LEAK="$FALLBACK_LEAK" \
SHRINKRAY_TEST_COMMAND_LOG="$COMMAND_LOG" SHRINKRAY_TEST_DISK_COUNTER="$COUNTER" SHRINKRAY_TEST_DISK_MODE=enough \
  bash "${ROOT_DIR}/shrinkray" "$REPLACE_INPUT" --size 1 --encoder auto --replace-original --machine-progress >"${TEMP_DIR}/replace-fallback.log" 2>&1
[ ! -e "$FALLBACK_LEAK" ] || { printf 'hardware test: source or temp changed before software fallback\n' >&2; exit 1; }
grep -q '^encoded output$' "$REPLACE_INPUT"

printf 'Hardware acceleration test passed.\n'
