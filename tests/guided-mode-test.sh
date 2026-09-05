#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)"
TEMP_DIR="$(mktemp -d)"
INPUT_FILE="${TEMP_DIR}/guided input.mkv"
OUTPUT_FILE="${TEMP_DIR}/guided input.shrunk.mkv"

cleanup() {
  rm -rf -- "$TEMP_DIR"
}
trap cleanup EXIT HUP INT TERM

command -v ffmpeg >/dev/null 2>&1 || {
  printf 'guided test: ffmpeg is required\n' >&2
  exit 1
}
command -v ffprobe >/dev/null 2>&1 || {
  printf 'guided test: ffprobe is required\n' >&2
  exit 1
}

assert_output_contains() {
  local expected="$1"
  case "$GUIDED_OUTPUT" in
    *"$expected"*) ;;
    *)
      printf 'guided test: expected output to contain: %s\n' "$expected" >&2
      printf '%s\n' "$GUIDED_OUTPUT" >&2
      exit 1
      ;;
  esac
}

printf 'Generating a short synthetic input video for guided mode...\n'
ffmpeg -y -hide_banner -loglevel error \
  -f lavfi -i "testsrc2=size=320x240:rate=24:duration=3" \
  -f lavfi -i "sine=frequency=1000:sample_rate=48000:duration=3" \
  -map 0:v:0 -map 1:a:0 \
  -c:v mpeg4 -q:v 2 -c:a pcm_s16le \
  "$INPUT_FILE"

printf 'Running guided mode with default choices...\n'
GUIDED_OUTPUT="$(
  printf '"%s"\n\n\n\n\n\n' "$INPUT_FILE" |
    bash "${ROOT_DIR}/shrinkray" guided --dry-run 2>&1
)"

assert_output_contains "Shrinkray guided mode"
assert_output_contains "Filename: guided input.mkv"
assert_output_contains "Original size:"
assert_output_contains "Target size:"
assert_output_contains "Container: MKV"
assert_output_contains "Keep original — create a separate .shrunk file (recommended)"
assert_output_contains "Replace original: false"
assert_output_contains "Dry run complete"

[ ! -e "$OUTPUT_FILE" ] || {
  printf 'guided test: dry run created an output movie\n' >&2
  exit 1
}
[ ! -e "${OUTPUT_FILE}.part" ] || {
  printf 'guided test: dry run created a .part file\n' >&2
  exit 1
}

printf 'Running guided replacement mode with explicit confirmation...\n'
GUIDED_OUTPUT="$(
  printf '"%s"\n\n\n\n2\nYES\n\n' "$INPUT_FILE" |
    bash "${ROOT_DIR}/shrinkray" guided --dry-run 2>&1
)"
assert_output_contains "The original movie will only be removed after the new encode has completed"
assert_output_contains "and passed validation."
assert_output_contains "Type YES to confirm replacement"
assert_output_contains "Replace original: true"
assert_output_contains "Output path: ${INPUT_FILE}"
assert_output_contains "Dry run complete"

printf 'Checking that replacement requires affirmative confirmation...\n'
GUIDED_OUTPUT="$(printf '"%s"\n\n\n\n2\nno\n' "$INPUT_FILE" | bash "${ROOT_DIR}/shrinkray" guided --dry-run 2>&1)"
assert_output_contains "Replacement was not confirmed; guided mode cancelled."
[ ! -e "$OUTPUT_FILE" ] || { printf 'guided test: rejected replacement created output\n' >&2; exit 1; }

printf 'Guided mode test passed.\n'
