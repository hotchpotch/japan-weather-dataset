# japan-weather-dataset

`jwd` collects the daily and hourly observations of every JMA (Japan
Meteorological Agency) station — official stations (気象台等) and AMeDAS —
from JMA's 「過去の気象データ・ダウンロード」 service and turns them into a
Parquet dataset that can be published on Hugging Face and queried with SQL.

- One static binary (Go + embedded DuckDB).
- Resumable full backfill, then updates that run on GitHub Actions without
  the crawl state: daily for the current month, and monthly to fetch the
  last two months again after they ended and move the oldest into its year
  file.
- Polite to the source: one request at a time, a pause of 6 s after each
  response that adapts between 3 s and 60 s (doubled on HTTP 429, lowered
  slowly while there are none), requests packed up to the server's size limit,
  optional cap on requests per 24 hours.
- Access restrictions (rate limiting, blocking, maintenance) are detected,
  logged, recorded in `<data-dir>/access_status.json` and reported by exit
  code, and the crawler stops instead of retrying blindly
  ([details](docs/dataset.md#access-restrictions)).

An introduction page in Japanese and an `llms.txt` for AI agents are in
[site/](site/README.md) (for GitHub Pages).

See [docs/source.md](docs/source.md) for how the source service works and
[docs/dataset.md](docs/dataset.md) for the dataset layout and the update strategy.
[docs/operations.md](docs/operations.md) describes the operation: the daily
update of the current month (`jwd month`) and the monthly run (`jwd monthly`),
both on GitHub Actions from a released Docker image, and the planned parts
(JMA's corrections, `jwd latest`).

## Using the dataset

The dataset is on the Hugging Face Hub:
[hotchpotch/japan-weather](https://huggingface.co/datasets/hotchpotch/japan-weather)
(daily values from 1872 and hourly values from 1885 to yesterday, updated
every day). Download the parts you need and query them with DuckDB:

```sh
.agents/skills/japan-weather/scripts/sync.sh daily   # into ~/.cache/japan-weather/data; ~215 MB; or daily:2020-2026, hourly:2025
duckdb -c "SELECT date, temperature_max FROM read_parquet('~/.cache/japan-weather/data/daily/*/*.parquet', hive_partitioning = false)
           WHERE station_id = 's47662' AND date >= DATE '2025-08-01' ORDER BY date LIMIT 5"
```

[docs/local-copy.md](docs/local-copy.md) describes how fresh the Hub is,
how to keep a local copy up to date and how to fetch days newer than the
Hub yourself (`jwd month`, with Go or Docker).

### With AI agents (skills)

The [`japan-weather`](.agents/skills/japan-weather/SKILL.md) skill teaches
coding agents the dataset: what it covers, daily vs hourly, how to download
only the needed parts, the columns, SQL recipes and the pitfalls of
long-term comparisons (quality flags, homogeneity breaks, the AMeDAS 2003
step of daily max/min temperature, …). It follows the
[Agent Skills](https://agentskills.io/specification) format.

- **In this repository:** nothing to install. Codex reads
  `.agents/skills/`, Claude Code reads `.claude/skills/` (a symlink to the
  same directory). Ask for an analysis, or invoke the skill explicitly with
  `/japan-weather` (Claude Code) or `$japan-weather` (Codex).
- **In another project** (copies the skill into that project):

  ```sh
  npx skills add hotchpotch/japan-weather-dataset --skill japan-weather
  ```

  ([vercel-labs/skills](https://github.com/vercel-labs/skills); asks for
  the agents to install for, `-g` installs for the user instead of the
  project). In Codex, the built-in installer also works:
  `$skill-installer install https://github.com/hotchpotch/japan-weather-dataset/tree/main/.agents/skills/japan-weather`.
- **By hand:** copy `.agents/skills/japan-weather/` to
  `~/.claude/skills/japan-weather/` (Claude Code) or
  `~/.agents/skills/japan-weather/` (Codex), or into `.claude/skills/` /
  `.agents/skills/` of a project. Restart the agent if the skill does not
  show up.

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

The updates run on GitHub Actions (`.github/workflows/daily.yml` daily,
`monthly.yml` on the 1st of each month at 20:00 JST) from a released image
(`ghcr.io/hotchpotch/japan-weather-dataset:vX.Y.Z`, built by
`.github/workflows/release.yml` for a tag; see
[docs/release.md](docs/release.md)). Neither needs the crawl state: they
download what they need from the Hub, run `jwd month` / `jwd monthly` on
it and upload what changed ([docs/operations.md](docs/operations.md)).

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

Global flags: `-interval` (initial pause after each response, default 6s),
`-min-interval` (lower bound of the adaptive pause, default 3s), `-budget` (cells per request,
default 100000, max 200000; larger requests time out), `-max-stations`, `-max-requests-per-24h`,
`-max-rate-limit-hits`, `-keep-raw` (store the raw CSV responses),
`-user-agent`.

Exit codes: 0 ok, 1 error, 3 rate limited, 4 access blocked, 5 maintenance,
6 request quota reached, 7 days missing from the month file beyond
`month -max-gap-days`, 8 the monthly refetch removed many rows of an ended
month (nothing written; `monthly -allow-row-loss`), 130 interrupted.

## Data license

The data comes from the JMA website and is available under the
公共データ利用規約（第1.0版） (PDL 1.0, compatible with CC BY 4.0). When
publishing data built with this tool, write the source and that it was
processed and by whom (PDL 1.0, 1.1.2), for example:

> 出典：気象庁ホームページ「過去の気象データ・ダウンロード」（https://www.data.jma.go.jp/risk/obsdl/）をもとに（加工者の名前）が加工して作成

and do not present it as if JMA or the government published it. The
dataset published on the Hugging Face Hub is processed by Yuichi Tateno
(@hotchpotch), and its generated card carries that notice.

This project is not affiliated with JMA.

## License

The code is MIT licensed. Test fixtures under `internal/obsdl/testdata`
are small excerpts of JMA data (PDL 1.0, source as above).
