#!/usr/bin/env bash
# The monthly run on the machine with the crawl state (docs/operations.md):
# jwd monthly, then upload the dataset to the Hugging Face Hub.
#
#   JWD_DATA_DIR=/data/jwd scripts/jwd-monthly.sh ./bin/jwd <user>/<dataset> /data/dataset [jwd monthly flags]
#   scripts/jwd-monthly.sh [jwd monthly flags]
#
# Without the three arguments, they default to ./bin/jwd, $HF_DATASET_REPO
# and $JWD_EXPORT_DIR, read from the environment or from .env in the
# repository root (KEY=value lines; see README).
#
# Run it on the 2nd of each month around 05:00 JST (e.g. from cron). The
# upload deletes month files on the Hub that are no longer in the dataset
# directory (they went into a year file). Data that was fetched before a
# failure is uploaded as well; the script then exits with jwd's exit code
# (see README). Needs `hf` (pip install huggingface_hub; without it,
# a pinned huggingface_hub is run with uvx) logged in or HF_TOKEN set.
set -u

root=$(cd "$(dirname "$0")/.." && pwd)
if [ -f "$root/.env" ]; then
  set -a
  # shellcheck disable=SC1091
  . "$root/.env"
  set +a
fi
if [ $# -ge 3 ] && [ "${1#-}" = "$1" ]; then
  jwd=$1 repo=$2 out=$3
  shift 3
else
  jwd=$root/bin/jwd repo=${HF_DATASET_REPO:-} out=${JWD_EXPORT_DIR:-}
fi
if [ -z "$repo" ] || [ -z "$out" ]; then
  echo "usage: $0 <jwd> <hf-repo> <dataset-dir> [jwd monthly flags]" >&2
  echo "   or: $0 [jwd monthly flags] with HF_DATASET_REPO and JWD_EXPORT_DIR set (e.g. in .env)" >&2
  exit 2
fi

if command -v hf >/dev/null; then
  hf=(hf)
else
  hf=(uvx --exclude-newer 2026-11-12T00:00:00Z --from huggingface_hub==2.4.0 hf)
fi

log() { echo "$(date -u '+%Y-%m-%dT%H:%M:%SZ') jwd-monthly: $*" >&2; }

started=$(date +%s)
"$jwd" -interval 6s -min-interval 5s monthly -out "$out" "$@"
code=$?
log "jwd monthly exit=$code"
if [ "$code" -eq 130 ] || [ "$code" -eq 2 ]; then
  exit "$code"
fi
if [ ! -f "$out/manifest.json" ] || [ "$(stat -c %Y "$out/manifest.json")" -lt "$started" ]; then
  log "nothing exported; not uploading"
  exit "$code"
fi

latest=$(sed -n 's/.*"latest_date": "\([0-9-]*\)".*/\1/p' "$out/manifest.json" | head -1)
log "uploading $out to $repo (latest $latest)"
"${hf[@]}" upload "$repo" "$out" . --repo-type dataset \
  --delete "*/year=*/????-??.parquet" \
  --commit-message "Monthly update up to $latest" || exit 1
log "uploaded"
exit "$code"
