# Getting the data

Dataset: <https://huggingface.co/datasets/hotchpotch/japan-weather>
(repo id `hotchpotch/japan-weather`, type `dataset`).

## Tools

- `hf` CLI (from `huggingface_hub`). Without installing:
  `uvx --from huggingface_hub hf ...` (tested with 2.4.0). Or
  `pip install -U huggingface_hub`.
- DuckDB for queries: the `duckdb` CLI, or Python `duckdb`
  (`uvx --with duckdb python ...`). Polars and pandas (`pyarrow`) read the same files.

## Authentication

A public dataset needs no token. If a download answers 401 or 404, the
repository is private or gated: run `hf auth login` (or set `HF_TOKEN`) with
a token that can read it. Never print or commit tokens.

## Where the copy lives

`~/.cache/japan-weather/data` on every OS (`$XDG_CACHE_HOME/japan-weather/data`
when `XDG_CACHE_HOME` is set), like `hf` uses `~/.cache/huggingface`: one
copy for all projects, outside any repository, and safe to delete (download
it again). Set `JW_DIR` for another place, e.g. `~/Library/Caches/japan-weather/data`
on macOS or `%LOCALAPPDATA%\japan-weather\data` on Windows (`sync.sh` is a
bash script: on Windows run it in WSL or Git Bash).

## Download only what the task needs

`hf download` with `--local-dir` keeps the dataset layout. Run it again to
bring the copy up to date: it skips files that did not change and fetches
only those that changed on the Hub (a second run takes seconds).

```sh
REPO=hotchpotch/japan-weather
DIR=${JW_DIR:-${XDG_CACHE_HOME:-$HOME/.cache}/japan-weather/data}   # as scripts/sync.sh

# Metadata (always; ~0.1 MB): station list, breaks, manifest, dataset card
hf download $REPO --repo-type dataset --local-dir $DIR \
  --include manifest.json --include README.md \
  --include 'stations/*' --include 'homogeneity_breaks/*'

# All daily values (~215 MB)
hf download $REPO --repo-type dataset --local-dir $DIR --include 'daily/*'

# Daily values of some years (year files and month files of those years)
hf download $REPO --repo-type dataset --local-dir $DIR \
  --include 'daily/year=2024/*' --include 'daily/year=2025/*' --include 'daily/year=2026/*'

# Hourly values: by year (~35-42 MB per year since the 1990s; all years ~1.9 GB)
hf download $REPO --repo-type dataset --local-dir $DIR \
  --include 'hourly/year=2025/*' --include 'hourly/year=2026/*'

# Only the recent, still changing months (see manifest.json for their names)
hf download $REPO --repo-type dataset --local-dir $DIR --include '*/year=2026/2026-*.parquet'
```

Add `--dry-run` to see what would be downloaded. The copy gets a
`.cache/huggingface/` directory with download metadata; leave it (it makes
the next download incremental) and keep the directory out of git.

Note for an incremental copy: when a month ends, the monthly update moves
the oldest month file into the year file and **deletes the month file on the
Hub**. `hf download` does not delete local files, so a stale month file can
remain next to an updated year file and duplicate rows. After the monthly
update (the 1st of a month, 20:00 JST), delete the local month files that
`manifest.json` no longer lists (or the whole year directory) and download
again:

```sh
jq -r '.daily.month_files[].path, .hourly.month_files[].path' $DIR/manifest.json   # files that should exist
ls $DIR/daily/year=*/????-??.parquet $DIR/hourly/year=*/????-??.parquet 2>/dev/null
```

## Reading straight from the Hub

DuckDB reads `hf://` paths without a download; good for a look at a small
part, slow for repeated queries:

```sql
SELECT count(*), max(date)
FROM 'hf://datasets/hotchpotch/japan-weather/daily/year=2026/*.parquet';
```

For a private or gated repository:
`CREATE SECRET hf (TYPE huggingface, PROVIDER credential_chain);` first.

The `datasets` library works too (`load_dataset("hotchpotch/japan-weather",
"daily")`; configs `daily`, `hourly`, `stations`, `homogeneity_breaks`), but
it loads whole configs; prefer `hf download` with includes.

## Values newer than the Hub

The Hub copy is updated once a day, up to yesterday (the job starts at
05:17 JST and takes about 10 minutes; GitHub can delay it). For
the newest days, or a copy that you update yourself, see
[docs/local-copy.md](https://github.com/hotchpotch/japan-weather-dataset/blob/main/docs/local-copy.md).
