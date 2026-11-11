#!/usr/bin/env bash
# The monthly run (docs/operations.md): download from the Hugging Face Hub
# what jwd monthly needs (the metadata, all month files and the year files
# of the recent years), run it and upload what it wrote, deleting the month
# file that went into its year file. No crawl state is needed.
#
#   scripts/jwd-monthly.sh [jwd monthly flags]
#
# Settings from the environment or .env in the repository root:
# HF_DATASET_REPO (required), HF_TOKEN (otherwise the token of
# `hf auth login`), JWD_EXPORT_DIR (work directory, default ./dataset).
# Nothing is uploaded when jwd fails or with -dry-run. Exits with jwd's exit
# code (see README).
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
dry=false
for a in "$@"; do
  case "$a" in -dry-run | --dry-run | -dry-run=true | --dry-run=true) dry=true ;; esac
done

log() { echo "$(date -u '+%Y-%m-%dT%H:%M:%SZ') jwd-monthly: $*" >&2; }

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

# The years jwd monthly reads (crawl.MonthlyYears): from the year of the
# first day of the month two months before yesterday's month (JST) to
# yesterday's year.
month=$(TZ=Asia/Tokyo date -d yesterday +%Y-%m-01)
from_year=$(date -d "$month -2 months" +%Y)
to_year=${month%%-*}
include=(--include manifest.json --include stations/stations.parquet
  --include homogeneity_breaks/homogeneity_breaks.parquet
  --include "daily/year=*/????-??.parquet" --include "hourly/year=*/????-??.parquet")
for ((y = from_year; y <= to_year; y++)); do
  include+=(--include "daily/year=$y/data.parquet" --include "hourly/year=$y/data.parquet")
done

log "downloading the metadata, the month files and the year files $from_year-$to_year from $repo"
mkdir -p "$out"
"${hf[@]}" download "$repo" --repo-type dataset --local-dir "$out" "${include[@]}" >/dev/null || exit 1

"$jwd" -interval 6s -min-interval 5s monthly -dataset "$out" "$@"
code=$?
log "jwd monthly exit=$code"
if [ "$code" -ne 0 ] || [ "$dry" = true ]; then
  exit "$code"
fi

latest=$(jq -r '.daily.latest_date // "-"' "$out/manifest.json")
log "uploading to $repo (latest $latest)"
# Only the downloaded and written files are in $out; unchanged ones are
# skipped by the Hub client. Month files that are no longer here went into
# a year file and are deleted on the Hub.
upload "$repo" "$out" . --repo-type dataset \
  --exclude ".cache/*" \
  --delete "*/year=*/????-??.parquet" \
  --commit-message "Monthly update up to $latest" || exit 1
log "uploaded"
