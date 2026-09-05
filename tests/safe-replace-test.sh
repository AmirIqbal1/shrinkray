#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)"
TEST_DIR="$(mktemp -d)"

cleanup() {
  rm -rf -- "$TEST_DIR"
}
trap cleanup EXIT HUP INT TERM

SHRINKRAY_SOURCE_ONLY=1 source "${ROOT_DIR}/shrinkray"
trap cleanup EXIT HUP INT TERM

fail() {
  printf 'safe replace test: %s\n' "$*" >&2
  exit 1
}

ffprobe() {
  local arg
  for arg in "$@"; do
    if [ "$arg" = "-select_streams" ]; then
      printf 'video\n'
      return 0
    fi
  done
  printf '%s\n' "${TEST_OUTPUT_DURATION:-100}"
}

duration_within_tolerance 100 102 || fail "two-second duration tolerance was rejected"
! duration_within_tolerance 100 102.01 || fail "duration beyond two-second tolerance was accepted"
duration_within_tolerance 2000 2010 || fail "0.5 percent duration tolerance was rejected"
! duration_within_tolerance 2000 2010.01 || fail "duration beyond 0.5 percent tolerance was accepted"

make_files() {
  local directory="$1"
  mkdir -p -- "$directory"
  truncate -s 100 -- "${directory}/movie.mkv"
  chmod 0640 -- "${directory}/movie.mkv"
  printf 'new-output' >"${directory}/replacement.mkv"
}

same_container_success() (
  local directory="${TEST_DIR}/same-success"
  make_files "$directory"
  ACTIVE_TEMP="${directory}/replacement.mkv"
  ACTIVE_BACKUP="${directory}/.movie.mkv.shrinkray-backup-test"
  replace_same_container "${directory}/movie.mkv" "$ACTIVE_TEMP" 100 100 "$(stat -c '%d:%i:%s' -- "${directory}/movie.mkv")" || fail "same-container replacement failed"
  [ "$(stat -c %s -- "${directory}/movie.mkv")" -eq 10 ] || fail "validated output was not placed at the source path"
  [ ! -e "$ACTIVE_BACKUP" ] || fail "successful transaction retained its backup"
)
same_container_success

mp4_same_container_success() (
  local directory="${TEST_DIR}/mp4-same-success"
  mkdir -p -- "$directory"
  truncate -s 100 -- "${directory}/movie.mp4"
  printf 'new-output' >"${directory}/replacement.mp4"
  ACTIVE_TEMP="${directory}/replacement.mp4"
  ACTIVE_BACKUP="${directory}/.movie.mp4.shrinkray-backup-test"
  replace_same_container "${directory}/movie.mp4" "$ACTIVE_TEMP" 100 100 "$(stat -c '%d:%i:%s' -- "${directory}/movie.mp4")" || fail "MP4 same-container replacement failed"
  [ "$(stat -c %s -- "${directory}/movie.mp4")" -eq 10 ] || fail "MP4 replacement was not placed at the source path"
)
mp4_same_container_success

hard_link_failure_keeps_source() (
  local directory="${TEST_DIR}/link-failure"
  make_files "$directory"
  local before
  before="$(stat -c '%d:%i:%s' -- "${directory}/movie.mkv")"
  ACTIVE_TEMP="${directory}/replacement.mkv"
  ACTIVE_BACKUP="${directory}/.movie.mkv.shrinkray-backup-test"
  ln() { return 1; }
  ! replace_same_container "${directory}/movie.mkv" "$ACTIVE_TEMP" 100 100 "$before" || fail "replacement ignored hard-link failure"
  [ "$(stat -c '%d:%i:%s' -- "${directory}/movie.mkv")" = "$before" ] || fail "hard-link failure changed the source"
)
hard_link_failure_keeps_source

rename_failure_keeps_source() (
  local directory="${TEST_DIR}/rename-failure"
  make_files "$directory"
  local before
  before="$(stat -c '%d:%i:%s' -- "${directory}/movie.mkv")"
  ACTIVE_TEMP="${directory}/replacement.mkv"
  ACTIVE_BACKUP="${directory}/.movie.mkv.shrinkray-backup-test"
  mv() {
    same_file_identity "${directory}/movie.mkv" "$ACTIVE_BACKUP" || fail "atomic rename began before the hard-link backup was verified"
    return 1
  }
  ! replace_same_container "${directory}/movie.mkv" "$ACTIVE_TEMP" 100 100 "$before" || fail "replacement ignored final rename failure"
  [ "$(stat -c '%d:%i:%s' -- "${directory}/movie.mkv")" = "$before" ] || fail "rename failure changed the source"
  [ ! -e "$ACTIVE_BACKUP" ] || fail "redundant backup remained after source was proven intact"
)
rename_failure_keeps_source

verification_failure_restores_source() (
  local directory="${TEST_DIR}/verify-failure"
  make_files "$directory"
  local before
  before="$(stat -c '%d:%i:%s' -- "${directory}/movie.mkv")"
  ACTIVE_TEMP="${directory}/replacement.mkv"
  ACTIVE_BACKUP="${directory}/.movie.mkv.shrinkray-backup-test"
  # shellcheck disable=SC2317 # Called indirectly by the sourced transaction function.
  ffprobe() { return 1; }
  ! replace_same_container "${directory}/movie.mkv" "$ACTIVE_TEMP" 100 100 "$before" || fail "replacement ignored final verification failure"
  [ "$(stat -c '%d:%i:%s' -- "${directory}/movie.mkv")" = "$before" ] || fail "final verification failure did not restore the original"
  [ ! -e "$ACTIVE_BACKUP" ] || fail "successful rollback retained the backup link"
)
verification_failure_restores_source

rollback_failure_retains_backup() (
  local directory="${TEST_DIR}/rollback-failure"
  make_files "$directory"
  ACTIVE_TEMP="${directory}/replacement.mkv"
  ACTIVE_BACKUP="${directory}/.movie.mkv.shrinkray-backup-test"
  local before
  before="$(stat -c '%d:%i:%s' -- "${directory}/movie.mkv")"
  local moves=0
  mv() {
    moves=$((moves + 1))
    if [ "$moves" -eq 1 ]; then
      /usr/bin/mv "$@"
    else
      return 1
    fi
  }
  ffprobe() { return 1; }
  ! replace_same_container "${directory}/movie.mkv" "$ACTIVE_TEMP" 100 100 "$before" || fail "replacement ignored rollback failure"
  [ -f "$ACTIVE_BACKUP" ] || fail "rollback failure deleted the recoverable original backup"
  [ "$(stat -c %s -- "$ACTIVE_BACKUP")" -eq 100 ] || fail "retained rollback backup is not the original"
)
rollback_failure_retains_backup

different_container_success() (
  local directory="${TEST_DIR}/different-success"
  mkdir -p -- "$directory"
  truncate -s 100 -- "${directory}/movie.mp4"
  printf 'new-output' >"${directory}/replacement.mkv"
  ACTIVE_TEMP="${directory}/replacement.mkv"
  replace_different_container "${directory}/movie.mp4" "$ACTIVE_TEMP" "${directory}/movie.mkv" 100 100 "$(stat -c '%d:%i:%s' -- "${directory}/movie.mp4")" || fail "different-container replacement failed"
  [ ! -e "${directory}/movie.mp4" ] || fail "different-container replacement retained the old source"
  [ -f "${directory}/movie.mkv" ] || fail "different-container replacement did not use the selected extension"
)
different_container_success

mkv_to_mp4_success() (
  local directory="${TEST_DIR}/mkv-to-mp4"
  mkdir -p -- "$directory"
  truncate -s 100 -- "${directory}/movie.mkv"
  printf 'new-output' >"${directory}/replacement.mp4"
  ACTIVE_TEMP="${directory}/replacement.mp4"
  replace_different_container "${directory}/movie.mkv" "$ACTIVE_TEMP" "${directory}/movie.mp4" 100 100 "$(stat -c '%d:%i:%s' -- "${directory}/movie.mkv")" || fail "MKV to MP4 replacement failed"
  if [ -e "${directory}/movie.mkv" ] || [ ! -f "${directory}/movie.mp4" ]; then
    fail "MKV to MP4 replacement used incorrect filenames"
  fi
)
mkv_to_mp4_success

different_container_delete_failure_keeps_both() (
  local directory="${TEST_DIR}/delete-failure"
  mkdir -p -- "$directory"
  truncate -s 100 -- "${directory}/movie.mp4"
  printf 'new-output' >"${directory}/replacement.mkv"
  ACTIVE_TEMP="${directory}/replacement.mkv"
  local source="${directory}/movie.mp4"
  rm() {
    if [ "${*: -1}" = "$source" ]; then
      return 1
    fi
    /usr/bin/rm "$@"
  }
  ! replace_different_container "$source" "$ACTIVE_TEMP" "${directory}/movie.mkv" 100 100 "$(stat -c '%d:%i:%s' -- "$source")" || fail "replacement ignored source deletion failure"
  if [ ! -f "$source" ] || [ ! -f "${directory}/movie.mkv" ]; then
    fail "source deletion failure did not retain both files"
  fi
)
different_container_delete_failure_keeps_both

different_container_existing_target_is_untouched() (
  local directory="${TEST_DIR}/existing-target"
  mkdir -p -- "$directory"
  truncate -s 100 -- "${directory}/movie.mp4"
  printf 'new-output' >"${directory}/replacement.mkv"
  printf 'unrelated' >"${directory}/movie.mkv"
  ACTIVE_TEMP="${directory}/replacement.mkv"
  ! replace_different_container "${directory}/movie.mp4" "$ACTIVE_TEMP" "${directory}/movie.mkv" 100 100 "$(stat -c '%d:%i:%s' -- "${directory}/movie.mp4")" || fail "replacement overwrote an existing target"
  [ "$(cat "${directory}/movie.mkv")" = unrelated ] || fail "existing target contents changed"
  [ -f "${directory}/movie.mp4" ] || fail "existing target conflict removed the source"
)
different_container_existing_target_is_untouched

output_not_smaller_is_rejected() (
  local directory="${TEST_DIR}/not-smaller"
  mkdir -p -- "$directory"
  truncate -s 100 -- "${directory}/movie.mkv"
  truncate -s 100 -- "${directory}/replacement.mkv"
  ! validate_replacement_output "${directory}/replacement.mkv" 100 100 || fail "equal-sized output passed replacement validation"
  [ -f "${directory}/movie.mkv" ] || fail "size validation removed the source"
)
output_not_smaller_is_rejected

invalid_and_mismatched_duration_are_rejected() (
  local directory="${TEST_DIR}/duration-failure"
  mkdir -p -- "$directory"
  truncate -s 100 -- "${directory}/movie.mkv"
  printf 'new-output' >"${directory}/replacement.mkv"
  TEST_OUTPUT_DURATION=not-a-duration
  ! validate_replacement_output "${directory}/replacement.mkv" 100 100 || fail "unreadable duration passed replacement validation"
  TEST_OUTPUT_DURATION=103
  ! validate_replacement_output "${directory}/replacement.mkv" 100 100 || fail "duration outside tolerance passed replacement validation"
  [ -f "${directory}/movie.mkv" ] || fail "duration validation removed the source"
)
invalid_and_mismatched_duration_are_rejected

source_survives_encode_and_preplacement_validation() (
  local directory="${TEST_DIR}/process-safety"
  mkdir -p -- "$directory"
  truncate -s 100 -- "${directory}/movie.mkv"
  chmod 0640 -- "${directory}/movie.mkv"
  REPLACE_ORIGINAL=true
  TRANSACTION_ID="process-safety"
  SELECTED_ENCODER=software
  QUALITY=fast
  CONTAINER=mkv
  KEEP_ALL_AUDIO=false
  DRY_RUN=false
  FORCE=false
  HAS_X265=true
  OUTPUT_FORMAT=matroska
  FFMPEG_PROGRESS_ARGS=(-stats)
  SUBTITLE_CODEC_ARGS=(-c:s copy)
  check_disk_space_preflight() { return 0; }
  run_software_hevc_encode() {
    if [ ! -f "$1" ] || [ "$(stat -c %s -- "$1")" -ne 100 ]; then
      fail "source was missing during encoding"
    fi
    printf 'new-output' >"$2"
  }
  eval "$(declare -f validate_replacement_output | sed '1s/validate_replacement_output/original_validate_replacement_output/')"
  validate_replacement_output() {
    if [[ "$1" == *shrinkray-replace* ]]; then
      if [ ! -f "${directory}/movie.mkv" ] || [ "$(stat -c %s -- "${directory}/movie.mkv")" -ne 100 ]; then
        fail "source was missing during validation"
      fi
    fi
    original_validate_replacement_output "$@"
  }
  process_file "${directory}/movie.mkv" 1 "${directory}/movie.mkv" hevc || fail "full replacement flow failed"
  [ "$(stat -c %s -- "${directory}/movie.mkv")" -eq 10 ] || fail "full replacement flow did not install the validated output"
  [ "$(stat -c %a -- "${directory}/movie.mkv")" = 640 ] || fail "replacement did not preserve source mode bits"
  [ ! -x "${directory}/movie.mkv" ] || fail "replacement unexpectedly became executable"
)
source_survives_encode_and_preplacement_validation

printf 'Safe replace tests passed.\n'
