#!/usr/bin/env bash
# Download or update a local copy of the japan-weather dataset from the
# Hugging Face Hub, only the parts asked for.
#
#   sync.sh [-d DIR] [-r REPO] [PART ...]
#
# PART: daily | hourly | daily:YEAR | daily:FROM-TO | hourly:YEAR | hourly:FROM-TO
# (default: daily). The metadata (manifest, stations, homogeneity breaks,
# dataset card) is always downloaded. Run it again to update the copy: only
# changed files are fetched, and month files that the Hub merged into a year
# file (the manifest no longer lists them) are removed, so no rows appear twice.
#
# DIR defaults to JW_DIR, else ${XDG_CACHE_HOME:-~/.cache}/japan-weather/data
# (on every OS); REPO to hotchpotch/japan-weather (or JW_REPO). A private repository needs `hf auth login` or HF_TOKEN.
set -euo pipefail

dir=${JW_DIR:-${XDG_CACHE_HOME:-$HOME/.cache}/japan-weather/data}
repo=${JW_REPO:-hotchpotch/japan-weather}
while getopts d:r:h opt; do
  case $opt in
    d) dir=$OPTARG ;;
    r) repo=$OPTARG ;;
    *) sed -n '2,15p' "$0"; exit 2 ;;
  esac
done
shift $((OPTIND - 1))
[ $# -eq 0 ] && set -- daily

if command -v hf >/dev/null; then
  hf=(hf)
else
  hf=(uvx --from huggingface_hub hf)
fi
# Python reads manifest.json: python3, else the Python of uv.
if command -v python3 >/dev/null; then
  py=(python3)
elif command -v uv >/dev/null; then
  py=(uv run --no-project --quiet python)
else
  echo "sync.sh: needs python3 or uv (https://docs.astral.sh/uv/)" >&2
  exit 2
fi

includes=()
for part in "$@"; do
  freq=${part%%:*}
  case $freq in
    daily | hourly) ;;
    *) echo "sync.sh: unknown part '$part' (daily, hourly, daily:2020-2025, ...)" >&2; exit 2 ;;
  esac
  if [ "$part" = "$freq" ]; then
    includes+=(--include "$freq/*")
    continue
  fi
  years=${part#*:}
  from=${years%-*}
  to=${years#*-}
  if ! [[ $from =~ ^[0-9]{4}$ && $to =~ ^[0-9]{4}$ ]] || [ "$from" -gt "$to" ]; then
    echo "sync.sh: bad years in '$part'" >&2
    exit 2
  fi
  for ((y = from; y <= to; y++)); do
    includes+=(--include "$freq/year=$y/*")
  done
done

# Both downloads read the same commit, so that an update of the Hub between
# them cannot leave a month both in its month file and in its year file.
rev=$("${hf[@]}" datasets info "$repo" --expand sha | "${py[@]}" -c 'import json,sys; print(json.load(sys.stdin)["sha"])')

mkdir -p "$dir"
echo "sync.sh: metadata from $repo@${rev:0:12} into $dir" >&2
"${hf[@]}" download "$repo" --repo-type dataset --revision "$rev" --local-dir "$dir" \
  --include manifest.json --include README.md \
  --include 'stations/*' --include 'homogeneity_breaks/*' >/dev/null

# Month files that are no longer on the Hub (merged into their year file).
listed=$("${py[@]}" -c 'import json,sys
m=json.load(open(sys.argv[1]))
for f in ("daily","hourly"):
    for e in m.get(f,{}).get("month_files",[]): print(e["path"])' "$dir/manifest.json")
shopt -s nullglob
for f in "$dir"/daily/year=*/????-??.parquet "$dir"/hourly/year=*/????-??.parquet; do
  rel=${f#"$dir"/}
  if ! grep -qxF "$rel" <<<"$listed"; then
    echo "sync.sh: removing $rel (merged into its year file)" >&2
    rm -f -- "$f"
  fi
done

echo "sync.sh: $*" >&2
"${hf[@]}" download "$repo" --repo-type dataset --revision "$rev" --local-dir "$dir" "${includes[@]}" >/dev/null

"${py[@]}" -c 'import json,sys
m=json.load(open(sys.argv[1]))
for f in ("daily","hourly"):
    x=m.get(f,{})
    print("%s: latest %s, settled through %s" % (f, x.get("latest_date"), x.get("year_files_through")))
print("updated", m.get("updated_at"))' "$dir/manifest.json"
