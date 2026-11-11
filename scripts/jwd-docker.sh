#!/usr/bin/env bash
# Runs a command of the jwd image on this machine with the crawl state and
# the dataset (or work) directory mounted (docs/operations.md):
#
#   scripts/jwd-docker.sh jwd-monthly.sh          # what monthly.yml runs
#   scripts/jwd-docker.sh jwd-daily.sh            # what month.yml runs
#   scripts/jwd-docker.sh jwd status
#
# Settings from the environment or .env in the repository root: JWD_IMAGE
# (e.g. ghcr.io/hotchpotch/japan-weather-dataset:v0.1.2), JWD_DATA_DIR and
# JWD_EXPORT_DIR (host directories, mounted at /data and /dataset),
# HF_DATASET_REPO and HF_TOKEN (passed on). The container runs as the
# calling user.
set -u

root=$(cd "$(dirname "$0")/.." && pwd)
if [ -f "$root/.env" ]; then
  set -a
  # shellcheck disable=SC1091
  . "$root/.env"
  set +a
fi
image=${JWD_IMAGE:?JWD_IMAGE is not set (e.g. ghcr.io/hotchpotch/japan-weather-dataset:v0.1.2)}
data=${JWD_DATA_DIR:?JWD_DATA_DIR is not set}
out=${JWD_EXPORT_DIR:?JWD_EXPORT_DIR is not set}
if [ $# -eq 0 ]; then
  echo "usage: $0 <command> [args]  (e.g. jwd-monthly.sh, jwd status)" >&2
  exit 2
fi
mkdir -p "$data" "$out"

env=(-e JWD_DATA_DIR=/data -e JWD_EXPORT_DIR=/dataset)
for k in HF_DATASET_REPO HF_TOKEN JWD_USER_AGENT; do
  if [ -n "${!k:-}" ]; then env+=(-e "$k"); fi
done
tty=()
if [ -t 0 ] && [ -t 1 ]; then tty=(-it); fi

# --init forwards Ctrl+C / SIGTERM to jwd, which stops cleanly.
exec docker run --rm --init "${tty[@]}" --user "$(id -u):$(id -g)" \
  -v "$data:/data" -v "$out:/dataset" "${env[@]}" "$image" "$@"
