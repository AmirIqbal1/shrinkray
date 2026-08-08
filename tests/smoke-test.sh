#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)"
TEMP_DIR="$(mktemp -d)"
INPUT_FILE="${TEMP_DIR}/synthetic-input.mkv"
OUTPUT_FILE="${TEMP_DIR}/synthetic-output.mkv"
MACHINE_OUTPUT_FILE="${TEMP_DIR}/synthetic-machine-output.mkv"
MACHINE_PROGRESS_LOG="${TEMP_DIR}/machine-progress.log"

cleanup() {
  rm -rf -- "$TEMP_DIR"
}
trap cleanup EXIT HUP INT TERM

command -v ffmpeg >/dev/null 2>&1 || {
  printf 'smoke test: ffmpeg is required\n' >&2
  exit 1
}
command -v ffprobe >/dev/null 2>&1 || {
  printf 'smoke test: ffprobe is required\n' >&2
  exit 1
}

printf 'Generating a short synthetic input video...\n'
ffmpeg -y -hide_banner -loglevel error \
  -f lavfi -i "testsrc2=size=320x240:rate=24:duration=3" \
  -f lavfi -i "sine=frequency=1000:sample_rate=48000:duration=3" \
  -map 0:v:0 -map 1:a:0 \
  -c:v mpeg4 -q:v 2 -c:a pcm_s16le \
  "$INPUT_FILE"

printf 'Running shrinkray...\n'
bash "${ROOT_DIR}/shrinkray" "$INPUT_FILE" \
  --size 1 --quality fast --output "$OUTPUT_FILE" -y

[ -s "$OUTPUT_FILE" ] || {
  printf 'smoke test: shrinkray did not create a non-empty output\n' >&2
  exit 1
}
[ ! -e "${OUTPUT_FILE}.part" ] || {
  printf 'smoke test: temporary .part output was not cleaned up\n' >&2
  exit 1
}

STREAM_TYPE="$(ffprobe -v error -select_streams v:0 \
  -show_entries stream=codec_type -of csv=p=0 "$OUTPUT_FILE")"
[ "$STREAM_TYPE" = "video" ] || {
  printf 'smoke test: ffprobe did not find a valid video stream\n' >&2
  exit 1
}

printf 'Smoke test passed.\n'

printf 'Running shrinkray with machine progress...\n'
bash "${ROOT_DIR}/shrinkray" "$INPUT_FILE" \
  --size 1 --quality fast --output "$MACHINE_OUTPUT_FILE" --machine-progress -y \
  >"$MACHINE_PROGRESS_LOG"

[ -s "$MACHINE_OUTPUT_FILE" ] || {
  printf 'smoke test: machine-progress encode did not create an output\n' >&2
  exit 1
}
grep -q '^out_time_us=' "$MACHINE_PROGRESS_LOG" || {
  printf 'smoke test: machine progress did not include out_time_us\n' >&2
  exit 1
}
grep -q '^speed=' "$MACHINE_PROGRESS_LOG" || {
  printf 'smoke test: machine progress did not include speed\n' >&2
  exit 1
}
grep -q '^progress=end$' "$MACHINE_PROGRESS_LOG" || {
  printf 'smoke test: machine progress did not terminate records with progress=end\n' >&2
  exit 1
}

printf 'Machine-progress smoke test passed.\n'

bash "${ROOT_DIR}/tests/disk-space-test.sh"
