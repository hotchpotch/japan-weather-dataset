# japan-weather-dataset

`jwd` collects the daily and hourly observations of every JMA (Japan
Meteorological Agency) station — official stations (気象台等) and AMeDAS —
from JMA's 「過去の気象データ・ダウンロード」 service and turns them into a
Parquet dataset that can be published on Hugging Face and queried with SQL.

- One static binary (Go + embedded DuckDB).
- Resumable full backfill, then a daily update that also picks up JMA's
  later corrections.
- Polite to the source: one request at a time, ≥ 3 s apart, adaptive
  slow-down on HTTP 429, requests packed up to the server's size limit,
  optional cap on requests per 24 hours.
- Access restrictions (rate limiting, blocking, maintenance) are detected,
  logged, recorded in `<data-dir>/access_status.json` and reported by exit
  code, and the crawler stops instead of retrying blindly
  ([details](docs/dataset.md#access-restrictions)).

See [docs/source.md](docs/source.md) for how the source service works and
[docs/dataset.md](docs/dataset.md) for the dataset layout and the update strategy.

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
skipped. `backfill -dry-run` prints the plan, `-max-jobs N` limits a run,
`-stations` and `-from-year` / `-to-year` narrow it.

Daily operation (after ~01:00 JST, when yesterday's values are available):

```sh
jwd stations                                   # weekly is enough
jwd update -lookback-days 31 -refresh-jobs 100 # recent days + rolling refresh
jwd export -out /data/dataset                  # rewrites changed years only
hf upload <user>/<dataset> /data/dataset . --repo-type dataset
```

Global flags: `-interval` (default 3s), `-budget` (cells per request,
default 180000, max 200000), `-max-stations`, `-max-requests-per-24h`,
`-max-rate-limit-hits`, `-keep-raw` (store the raw CSV responses),
`-user-agent`.

Exit codes: 0 ok, 1 error, 3 rate limited, 4 access blocked, 5 maintenance,
6 request quota reached, 130 interrupted.

## Data license

The data comes from the JMA website and is available under the
公共データ利用規約（第1.0版） (PDL 1.0, compatible with CC BY 4.0). When
publishing data built with this tool, write the source and that it was
processed, for example:

> 出典：気象庁ホームページ「過去の気象データ・ダウンロード」（https://www.data.jma.go.jp/risk/obsdl/）を加工して作成

and do not present it as if JMA or the government published it. The
generated dataset card includes this notice.

This project is not affiliated with JMA.

## License

The code is MIT licensed. Test fixtures under `internal/obsdl/testdata`
are small excerpts of JMA data (PDL 1.0, source as above).
