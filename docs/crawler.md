# The `jwd` crawler

`jwd` collects the observations from JMA's 「過去の気象データ・ダウンロード」
service and exports the Parquet dataset. Most users do not need it: the
dataset on the Hugging Face Hub is updated every day
([local-copy.md](local-copy.md)).

- One static binary (Go + embedded DuckDB).
- Resumable full backfill, then updates that run on GitHub Actions without
  the crawl state: daily for the current month, and monthly to fetch the
  last two months again after they ended and move the oldest into its year
  file ([operations.md](operations.md)).
- Polite to the source: one request at a time, a pause of 6 s after each
  response that adapts between 3 s and 60 s (doubled on HTTP 429, lowered
  slowly while there are none), requests packed up to the server's size
  limit, optional cap on requests per 24 hours.
- Access restrictions (rate limiting, blocking, maintenance) are detected,
  logged, recorded in `<data-dir>/access_status.json` and reported by exit
  code, and the crawler stops instead of retrying blindly
  ([dataset.md](dataset.md#access-restrictions)).

## Build

```sh
go build -o bin/jwd ./cmd/jwd
```

Go 1.26+ with cgo (the DuckDB library is linked statically; no system
DuckDB needed).

## Usage

```sh
export JWD_DATA_DIR=/data/jwd

jwd stations                      # station lists of all areas (~60 requests)
jwd probe                         # years with data per station (~150 requests)
jwd backfill -frequency daily     # all daily values, newest years first
jwd backfill -frequency hourly    # all hourly values
jwd export -out /data/dataset     # Parquet files + dataset card
jwd status
```

Every command can be stopped and restarted; finished station-years are
skipped. For the long backfill, `scripts/jwd-until-done.sh ./bin/jwd …`
restarts the command after a growing cool-down when it stops on network
errors, persistent rate limiting or the own quota, and stops on a block or
maintenance. `backfill -dry-run` prints the plan, `-max-jobs N` limits a run,
`-stations` and `-from-year` / `-to-year` narrow it.

Global flags: `-interval` (initial pause after each response, default 6s),
`-min-interval` (lower bound of the adaptive pause, default 3s), `-budget`
(cells per request, default 100000, max 200000; larger requests time out),
`-max-stations`, `-max-requests-per-24h`, `-max-rate-limit-hits`,
`-keep-raw` (store the raw CSV responses), `-user-agent`.

Exit codes: 0 ok, 1 error, 3 rate limited, 4 access blocked, 5 maintenance,
6 request quota reached, 7 days missing from the month file beyond
`month -max-gap-days`, 8 the monthly refetch removed many rows of an ended
month (nothing written; `monthly -allow-row-loss`), 130 interrupted.

## Updates

The updates run on GitHub Actions (`.github/workflows/daily.yml` daily,
`monthly.yml` on the 1st of each month at 20:00 JST) from a released image
(`ghcr.io/hotchpotch/japan-weather-dataset:vX.Y.Z`, built by
`.github/workflows/release.yml` for a tag; see [release.md](release.md)).
Neither needs the crawl state: they download what they need from the Hub,
run `jwd month` / `jwd monthly` on it and upload what changed
([operations.md](operations.md)).

To run them elsewhere, put the settings into `.env` in the repository root
(not committed) or the environment:

```sh
JWD_IMAGE=ghcr.io/hotchpotch/japan-weather-dataset:latest
JWD_DATA_DIR=/data/jwd          # crawl state (backfill, update, export)
JWD_EXPORT_DIR=/data/work       # work directory of the scripts
HF_DATASET_REPO=<user>/<dataset>
HF_TOKEN=...                    # write access to that dataset only
```

```sh
scripts/jwd-docker.sh jwd-daily.sh     # current month: last 3 days again + up to 3 missing days
scripts/jwd-docker.sh jwd-monthly.sh   # the last two months again, oldest month into its year file
scripts/jwd-docker.sh jwd-monthly.sh -dry-run
```

`scripts/jwd-docker.sh jwd status` and other `jwd` commands run in the image
the same way. Without Docker, `scripts/jwd-daily.sh` and
`scripts/jwd-monthly.sh` use `./bin/jwd` and the `hf` CLI (or `uvx`).
