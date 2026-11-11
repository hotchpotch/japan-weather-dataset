#!/usr/bin/env bash
# The daily update of the current month files (docs/operations.md): download
# the metadata and the current month files from the Hugging Face Hub, run
# jwd month, upload those files again. No crawl state is needed.
#
#   scripts/jwd-daily.sh [jwd month flags]
#
# Settings from the environment or .env in the repository root:
# HF_DATASET_REPO (required), HF_TOKEN (otherwise the token of
# `hf auth login`), JWD_EXPORT_DIR (work directory, default ./dataset; it
# only needs to hold the downloaded files). Exits with jwd's exit code;
# with 7 (days missing beyond -max-gap-days) the files are uploaded first.
set -u

root=$(cd "$(dirname "$0")/.." && pwd)
if [ -f "$root/.env" ]; then
  set -a
  # shellcheck disable=SC1091
  . "$root/.env"
  set +a
fi
repo=${HF_DATASET_REPO:?HF_DATASET_REPO is not set}
out=${JWD_EXPORT_DIR:-dataset}
if [ -x "$root/bin/jwd" ]; then jwd=$root/bin/jwd; else jwd=jwd; fi
if command -v hf >/dev/null; then
  hf=(hf)
else
  hf=(uvx --exclude-newer 2026-11-12T00:00:00Z --from huggingface_hub==2.4.0 hf)
fi

log() { echo "$(date -u '+%Y-%m-%dT%H:%M:%SZ') jwd-daily: $*" >&2; }

# Adds a line to the job summary of GitHub Actions (after jwd's own).
summary() { if [ -n "${GITHUB_STEP_SUMMARY:-}" ]; then echo "$*" >>"$GITHUB_STEP_SUMMARY"; fi; }

# upload runs hf upload with the given arguments and reports the commit in
# the job summary.
upload() {
  local res url
  if ! res=$("${hf[@]}" upload "$@"); then
    summary "**Upload to $repo failed.**"
    return 1
  fi
  echo "$res"
  url=$(grep -o 'https://huggingface.co/[^ ]*/commit/[0-9a-f]*' <<<"$res" | tail -n 1)
  summary "Uploaded to $repo: ${url:-$res}"
}

# The month of yesterday in JST: on the 1st this is the month before, whose
# file is still current until the monthly run.
month=$(TZ=Asia/Tokyo date -d yesterday +%Y-%m)
year=${month%-*}
files=(manifest.json "daily/year=$year/$month.parquet" "hourly/year=$year/$month.parquet")

log "downloading the metadata and $month from $repo"
mkdir -p "$out"
# The month files do not exist yet on the 1st.
"${hf[@]}" download "$repo" --repo-type dataset --local-dir "$out" \
  --include manifest.json --include stations/stations.parquet \
  --include "${files[1]}" --include "${files[2]}" >/dev/null || exit 1

"$jwd" -interval 6s -min-interval 5s month -dataset "$out" "$@"
code=$?
log "jwd month exit=$code"
if [ "$code" -ne 0 ] && [ "$code" -ne 7 ]; then
  exit "$code"
fi

latest=$(jq -r '.daily.latest_date // "-"' "$out/manifest.json")
log "uploading $month up to $latest"
include=()
for f in "${files[@]}"; do include+=(--include "$f"); done
upload "$repo" "$out" . --repo-type dataset "${include[@]}" \
  --commit-message "Update $month up to $latest" || exit 1

if [ "$code" -eq 7 ]; then
  jq '{daily: .daily.month_files, hourly: .hourly.month_files}' "$out/manifest.json"
  msg="days are missing from the month files; the monthly run has to fill them"
  if [ -n "${GITHUB_ACTIONS:-}" ]; then echo "::error::$msg"; else log "$msg"; fi
fi
exit "$code"
