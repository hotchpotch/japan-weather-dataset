# Using a local copy of the dataset

The dataset is on the Hugging Face Hub
([hotchpotch/japan-weather](https://huggingface.co/datasets/hotchpotch/japan-weather)).
Most uses need only a local copy of the parts they read, refreshed from the
Hub. Fetching from JMA yourself is needed only for days newer than the Hub
or for a dataset of your own.

## How fresh the Hub is

| part | updated | by |
|---|---|---|
| current month files (`*/year=YYYY/YYYY-MM.parquet`) | every day, up to yesterday (starts 05:17 JST, takes about 10 min) | `month.yml` (`jwd month`) |
| previous month | fetched again on the 2nd of the next month, then merged into its year file | `monthly.yml` (`jwd monthly`) |
| older years (`*/year=YYYY/data.parquet`) | not refetched; JMA's announced corrections are planned | |
| stations, homogeneity breaks | on the 2nd of each month | `monthly.yml` |

`manifest.json` says what the files cover: `latest_date` (last day),
`year_files_through` (last day in the year files; later days are
preliminary and may still change) and the month files with `missing_days`.
See [operations.md](operations.md) for the details, and the
[Actions runs](https://github.com/hotchpotch/japan-weather-dataset/actions)
for the state of the updates.

## Download and refresh

[`sync.sh`](../.agents/skills/japan-weather/scripts/sync.sh) (part of the
[agent skill](../.agents/skills/japan-weather/SKILL.md)) downloads the
metadata and the parts asked for with the `hf` CLI (or `uvx`), and removes
month files that the Hub has merged into their year file, which
`hf download` alone would leave behind (their rows would then appear twice):

```sh
sync.sh daily                     # all daily values (~215 MB)
sync.sh daily hourly:2020-2026
```

The copy goes to `~/.cache/japan-weather/data` (`$XDG_CACHE_HOME` instead of
`~/.cache` when set, `JW_DIR` or `-d` for another directory). Run it again
to refresh: unchanged files are skipped. The same with `hf`
directly is in
[download.md](../.agents/skills/japan-weather/references/download.md).
A private repository needs `hf auth login` or `HF_TOKEN`.

## Days newer than the Hub (`jwd month`)

`jwd month` fetches the last 3 days up to yesterday (and up to 3 missing
days before them) of the current month from JMA into the month files of a
local copy — the same command as the daily update, without uploading. It
needs `stations/stations.parquet` and the current month files, and rewrites
`manifest.json`; `sync.sh` downloads all of them.

Refresh from the Hub first (`sync.sh` replaces local month files with the
Hub's version), then run `jwd month`. Fetch only what you need:
`-frequency daily` is about 32 requests (4 minutes); hourly adds as many.
Run it at most once a day, and never around a rate limit or block (exit
codes 3, 4, 5): stop and wait.

### With Go

Go 1.26+ with cgo (a C compiler); DuckDB is linked statically.

```sh
git clone https://github.com/hotchpotch/japan-weather-dataset
cd japan-weather-dataset
go build -o bin/jwd ./cmd/jwd

JW=~/.cache/japan-weather
.agents/skills/japan-weather/scripts/sync.sh daily
./bin/jwd -data-dir $JW/jwd month -dataset $JW/data -frequency daily -dry-run
./bin/jwd -data-dir $JW/jwd month -dataset $JW/data -frequency daily
```

`-dry-run` prints the requests without sending them. `-data-dir` keeps the
access status (interval, last rate limit) between runs.

### With Docker

The released image has `jwd`, the `hf` CLI and the scripts
([release.md](release.md)); pick the version from the
[releases](https://github.com/hotchpotch/japan-weather-dataset/releases).
The image is private while the repository is (`docker login ghcr.io` with a
token that can read packages); build it yourself otherwise:
`docker build -t jwd .` in a clone.

```sh
IMAGE=ghcr.io/hotchpotch/japan-weather-dataset:v0.1.2   # or jwd
JW=~/.cache/japan-weather
mkdir -p $JW/data $JW/jwd
docker run --rm --init --user "$(id -u):$(id -g)" \
  -v $JW/data:/dataset -v $JW/jwd:/data \
  "$IMAGE" jwd month -frequency daily -dry-run
```

In the image `/dataset` is the dataset directory and `/data` the state
directory (`JWD_EXPORT_DIR`, `JWD_DATA_DIR`). `sync.sh` is not in the image;
download with it on the host, or run `hf download` in the image
(`docker run ... "$IMAGE" hf download hotchpotch/japan-weather --repo-type dataset --local-dir /dataset --include ...`,
passing `-e HF_TOKEN` for a private repository).

### Limits

- Only the month of yesterday is updated. When the copy misses more than
  3 days of it, `jwd month` fills the latest 3 and exits with code 7;
  refresh from the Hub again.
- Months before are updated by the monthly run on the Hub; `jwd monthly`
  can do the same on a copy with the year files of the recent years
  ([operations.md](operations.md#monthly-update)), at about 260 requests.
- `jwd latest` (planned) will add days newer than the Hub without the
  one-month limit.

## A dataset of your own

Building the whole dataset from JMA takes days of requests (`jwd stations`,
`probe`, `backfill`, `export`; see the [README](../README.md#usage)). Use the
Hub copy unless you need something it does not have. A fork that publishes
its own dataset runs the workflows with its own `HF_DATASET_REPO` and
`HF_TOKEN` ([operations.md](operations.md)); the scheduled jobs run only in
this repository unless the repository check in the workflows is changed.
