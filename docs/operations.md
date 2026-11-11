# Operation: daily and monthly updates

> **Status.** Implemented: the file layout with month files, `manifest.json`,
> `jwd month` with the GitHub Actions workflow, `jwd monthly` with
> `scripts/jwd-monthly.sh`, and the released image that runs both. Planned, not implemented yet: `jwd refetch`,
> `jwd corrections` (JMA's announced corrections), `jwd latest`, the
> `status` column and the `corrections` output (marked "planned" below).
> When a planned part is implemented, update this document (and README /
> dataset.md) to describe what was actually built, and remove its "planned"
> marks.

The published dataset is updated in two places:

| | GitHub Actions | machine with the crawl state |
|---|---|---|
| when | every day, 05:17 JST | the 2nd of each month, ~05:00 JST |
| command | `jwd-daily.sh` (`jwd month` + download/upload) in the image, from `.github/workflows/month.yml` | `jwd-monthly.sh` (`jwd monthly` + upload) in the image, from `scripts/jwd-docker.sh` (cron) |
| state | none (temporary) | `state.duckdb` + `chunks/` (~6 GB) |
| downloads from the Hub | `manifest.json`, `stations.parquet`, the current month files (~5 MB) | nothing (the state is the source) |
| fetches | the last 3 days of the current month, up to 3 missing days before them | the month before last to yesterday; 1/24 of the older station-years |
| writes | the current month files, `manifest.json` | everything: year files, month files (and deletes merged ones), stations, homogeneity breaks, card, manifest |
| requests / time | ~64 / ~8 min | ~200 + ~2,500 / 5–6 h (measured plan, 2026-11) |
| probes new stations | no | yes |

Their files only meet at the start of a month. The monthly run also
writes the current month file (from its own fetch of the same days), so
whichever upload comes last, the file holds fetched values. Both rebuild
`manifest.json` from the files: the monthly run from all files, the daily
job from its month files plus the entries of the downloaded manifest for
files it did not download (entries more than two months before the
current month are dropped as stale). The monthly upload (hours after the
daily job of the 2nd) leaves a complete manifest.

Both run the same released image (see [releases](#releases-and-the-image)),
so a change reaches them only with a new release.

The crawl machine needs no daily run any more (`jwd update` remains as a
tool). Users who need values newer than the dataset can add them locally
(see [latest values](#latest-values-for-users-planned)).

## JMA's preliminary and final values

JMA publishes 速報値 (preliminary values) and 確定値 (final values) on the
past weather data pages ([データの更新時刻][update]): updates run daily
around 01:00, 03:00 and 14:00 JST. For surface and AMeDAS data no fixed day
is given on which a month becomes final (the upper-air page says "around
the 20th of the next month"; surface data has no such statement).

Corrections are applied at the next update without notice, unless "the
corrected period is about a month or longer, or the corrected data is about
two months old or older and is an all-time record"; those are announced
(データ修正のお知らせ, `correctHistory.json` of obsdl). Observed examples:
data lost in a network outage restored about a week later; periods several
years back corrected.

Observed in this dataset (2026-11-09, first `update` after the full
backfill, refetching 31 days): of the hourly rows fetched 1–3 days after
the observation, about 3,700 per day (about 155 stations, almost all
official stations) changed; older days changed by 1–22 rows a day. Daily
values: 1–14 rows a day over the whole month.

So recent values change a lot within days, settle within weeks without
notice, and old values change only rarely and (for long periods) with
notice. The updates follow this:

- the last days of the current month are fetched again every day;
- the month before is fetched again by the monthly run, after it ended;
- months older than that are in the year files and treated as final;
- older changes come from the announced corrections and a slow rolling
  refetch.

## Files

```
stations/stations.parquet
daily/year=YYYY/data.parquet        months before the recent months
daily/year=YYYY/YYYY-MM.parquet     one recent month
hourly/...                          the same layout
homogeneity_breaks/homogeneity_breaks.parquet
manifest.json
README.md
```

The recent months are the month of the latest date and the month before;
`jwd export` writes them as month files and everything older into the year
files. When the latest date moves into a new month, the next export moves
the oldest month file into its year file and deletes the month file. A
month is never in both. Readers glob `daily/*/*.parquet` and see one table.

Example, December 2026:

```
hourly/year=2026/data.parquet       2026-01 .. 2026-10   monthly run
hourly/year=2026/2026-11.parquet    November             monthly run (fetched again after 11-30)
hourly/year=2026/2026-12.parquet    December             jwd month, every day
```

On the first day of a month there can be three month files (the
`jwd month` run of 12-02 starts December before the monthly run moved
October). That is valid: the files do not overlap.

`manifest.json` tells what the files cover, so that the daily job and
users can check the dataset without downloading data:

```json
{
  "updated_at": "2026-12-10T20:25:03Z",
  "daily": {
    "latest_date": "2026-12-09",
    "year_files_through": "2026-10-31",
    "month_files": [
      {"month": "2026-11", "path": "daily/year=2026/2026-11.parquet",
       "first_date": "2026-11-01", "last_date": "2026-11-30", "rows": 38610, "missing_days": []},
      {"month": "2026-12", "path": "daily/year=2026/2026-12.parquet",
       "first_date": "2026-12-01", "last_date": "2026-12-09", "rows": 11583, "missing_days": []}
    ]
  },
  "hourly": { "...": "..." }
}
```

`missing_days` are days without any row, from the first day of the month
to `last_date` (to the end of the month for a month before the latest).
`export` and `month` rewrite the manifest from the files.

## Daily update of the current month

`jwd month` updates the month files of the month of yesterday (JST) in a
dataset directory. It needs only `stations/stations.parquet` and, if they
exist, the current month files; its crawl state is a temporary directory.

1. Load the stations (with their observed categories and years) and the
   current month files into the temporary state.
2. Plan the days to fetch:
   - the last `-refetch-days` (default 3) days of the month up to
     yesterday, every run (recent values are revised within days, see
     above);
   - days of the month before them without any row: the latest
     `-max-gap-days` (default 3) of them, e.g. after a failed run.
3. Fetch them for the stations with recent data (as `update`; each request
   starts one day earlier for homogeneity breaks) and merge: rows that did
   not change keep their `updated_at`.
4. Write the month files and `manifest.json`.

If more days are missing than `-max-gap-days`, the latest are filled, the
rest are listed in the manifest and the command exits with code 7; the
monthly run fills them. Requests never go before the first day of the
month: on 12-01 the job updates November (yesterday is 11-30), from 12-02
on it updates only December. Stations are not probed (new stations come
with the monthly run), and the homogeneity breaks of the current month are
added by the monthly run.

Cost per run (measured 2026-11-11, 3 days, both frequencies, 1,287
stations): 64 requests (32 daily, 32 hourly; the requests are limited by
50 stations each and by the element sets, not by size), 8 minutes with
`-interval 6s -min-interval 5s`, of which about 40 s load the month files.
On a standard GitHub runner (4 vCPUs, 16 GB, free for public repositories)
the CPU, memory and disk needed are small (the month files are about 0.5
MB daily and 4.5 MB hourly at the end of a month); the time is spent
waiting for JMA.

### GitHub Actions

`.github/workflows/month.yml` runs daily at 05:17 JST (after JMA's 01:00
and 03:00 updates; not on the hour, when scheduled runs are delayed most).
It pulls the image `JWD_IMAGE` (a released version, set in the workflow)
and runs `scripts/jwd-daily.sh` in it:

1. `hf download` only `manifest.json`, `stations/stations.parquet` and the
   two month files of the current month (absent on the 1st).
2. `jwd month`; exit code 7 still uploads, then fails the job so that it
   shows up.
3. `hf upload` only `manifest.json` and the two month files.

Settings: repository variable `HF_DATASET_REPO`, secret `HF_TOKEN` with
write access to the dataset. A run takes about 10 minutes. In a private
repository it uses the included Actions minutes (a standard runner there
has 2 vCPUs, which is enough).

The same script runs outside GitHub with `.env` (or the environment)
providing `HF_DATASET_REPO`, `HF_TOKEN` and `JWD_EXPORT_DIR` (a work
directory).

Notes for a public repository:

- The job runs only in this repository (`if: github.repository == …`), so
  that forks do not send scheduled requests to JMA.
- GitHub runners share IP addresses. JMA's rate limiting may answer 429
  sooner than at home, or block an address. The job uses `-interval 6s
  -min-interval 5s`; a block (exit code 4) fails the job. Do not work
  around a restriction (see [dataset.md](dataset.md#access-restrictions)).
- Scheduled runs can be delayed or skipped when GitHub is busy; the gap
  filling covers a few skipped days.
- GitHub disables scheduled workflows of a public repository after 60 days
  without activity in the repository. Uploads to the Hub do not count;
  keep the repository active (or re-enable the workflow).
- `HF_TOKEN` is not available to pull requests from forks; the workflow
  has no pull request trigger.

## Value status (planned)

- New column `status` in `daily` and `hourly`: `preliminary` or `final`.
- `final` means: in a year file, i.e. fetched again after the end of its
  month by the monthly run. It is this dataset's rule, not a guarantee
  that JMA considers the value 確定値; the dataset card says so.
- After a few monthly runs, measure from `updated_at` how many days after
  the observation values still change, and adjust the rule if needed.

## Monthly update

On the machine with the crawl state, on the 2nd of each month around 05:00
JST (after JMA's 01:00/03:00 updates), from cron:

```sh
scripts/jwd-docker.sh jwd-monthly.sh
```

`scripts/jwd-docker.sh` runs a command of the image `JWD_IMAGE` with the
state directory `JWD_DATA_DIR` and the dataset directory `JWD_EXPORT_DIR`
mounted (settings from `.env`, see README). Without Docker,
`scripts/jwd-monthly.sh` runs the same with a local `bin/jwd`.

It does not run on a GitHub-hosted runner: it takes 5–6 hours (a job may
run at most 6), and the state (~6 GB) would have to be restored and kept
between monthly runs (the Actions cache drops entries unused for 7 days).

`jwd-monthly.sh` runs `jwd monthly -out <dataset-dir>`:

1. `stations` (61 requests; `-skip-stations` to skip) and a probe of
   stations without coverage.
2. Refetch from the first day of the month before last to yesterday for
   every active station, daily and hourly. On 12-02: 10-01..12-01, so
   October and November are fetched again after they ended (and days the
   daily job could not fill are filled).
3. `export`: October goes into the year file; November and December are
   month files.
4. Refetch the station-years fetched longest ago: 1/`-refresh-cycle-months`
   (default 24) of all station-years up to the current year, i.e. the whole
   archive in two years, to catch corrections that JMA does not announce.
   Station-years fetched in the last `-refresh-age` (30 days) are skipped.
5. `export` again.

If step 4 fails (e.g. rate limited), the data fetched so far is exported
anyway and the error is returned; the next run continues with the
station-years still fetched longest ago. A failed step 1 or 2 stops before
the export; run it again (it is safe to repeat).

The script then uploads the dataset directory with
`hf upload … --delete "*/year=*/????-??.parquet"`, which removes the month
files that went into a year file; unchanged files are not uploaded again.
It uploads even after a failure when something was exported, and exits
with jwd's exit code.

Plan measured on 2026-11-11 (`jwd monthly -dry-run`, yesterday 11-10):
195 requests for 09-01..11-10 and, with the age limit lifted, 2,509
requests for the refresh (3,248 of 77,943 daily and 3,114 of 74,731 hourly
station-years). At about 7 s per request that is 5–6 hours.

Planned additions to the monthly run: fetch `correctHistory.json` and
store entries not seen before in a state DB table `corrections` with state
`pending` (see below).

## Announced corrections (planned, done by an AI agent)

The entries of `correctHistory.json` are free text in Japanese, e.g.
「2026年9月21日〜24日の通信障害で集信できなかった千葉県のアメダスを復元」 or
「郡家の湿度を 2026/1/1〜4/13 欠測に」. Turning them into stations, periods
and elements needs judgement, so an AI agent (or a person) does it; the
program provides the tools:

- `jwd corrections list`: pending entries.
- `jwd refetch -frequency daily|hourly -stations ID,… -from YYYY-MM-DD -to YYYY-MM-DD`:
  refetch exactly that range (same code path as backfill/update).
- `jwd corrections mark <id> -applied|-ignored -note "…"`: record the
  outcome (stations, period, changed rows, reason).

Procedure for the agent, for each pending entry:

1. Read the entry. Resolve station names to IDs with the `stations` table
   (name, prefecture, kind); expand prefecture-wide entries ("千葉県のアメダス")
   to all stations of that kind in the prefecture.
2. Determine the period and the frequency (daily, hourly or both). If only
   some elements are named, refetch all elements of the station anyway
   (a request costs about the same).
3. Run `jwd refetch` and check the reported number of changed rows. Zero
   changes can be correct (already picked up by an earlier refetch) but is
   worth a note.
4. Mark the entry `applied` with a note, or `ignored` with the reason
   (e.g. concerns data not in this dataset, such as upper-air data).
5. If unsure (unknown station name, ambiguous period), leave it `pending`
   and report it instead of guessing.

The applied corrections are exported as `corrections/corrections.parquet`
(entry, date, text, stations, period, changed rows) so that users can see
what was changed and why.

## Upload

- Daily (`jwd-daily.sh`): only `manifest.json` and the current month files.
- Monthly (`jwd-monthly.sh`): the whole dataset directory; files
  whose content did not change are skipped by the Hub client, month files
  merged into a year file are deleted.
- The commit message carries the latest date and, once implemented, the
  IDs of the corrections applied.

## Releases and the image

The `Dockerfile` builds an image with `jwd`, the `hf` CLI and `scripts/`
(on `PATH`; `JWD_DATA_DIR=/data`, `JWD_EXPORT_DIR=/dataset`). A tag
`vX.Y.Z` publishes it as `ghcr.io/hotchpotch/japan-weather-dataset:vX.Y.Z`;
both updates name the version they run (`JWD_IMAGE` in
`.github/workflows/month.yml` and in `.env` on the crawl machine). See
[release.md](release.md) for the steps, image access and pinning.

## Latest values for users (planned)

`jwd latest` lets a user add values newer than the dataset to a local copy,
without the crawler state:

1. Download `manifest.json`, `stations`, `homogeneity_breaks` and the files
   that will change from the Hub (`-all` downloads everything).
2. Fetch from `latest_date` + 1 day to yesterday (the same code path as
   `jwd month`, without the one-month limit).
3. Write the new days into the month files.

Load on JMA grows with the number of users, so defaults are conservative:
daily only (about 30 requests for all stations; `-hourly` adds about 400
requests, about an hour), `-stations` to narrow the stations, an interval
of 6 s with a lower bound of 4 s. README asks users to
fetch only what they need.

## Order of work

1. This document; month files, `manifest.json`, `jwd month`, the
   workflow; `jwd monthly` and its script (done).
2. `refetch`, `corrections`; run the first monthly update and measure
   requests and changed rows.
3. `latest`.

[update]: https://ds.data.jma.go.jp/obd/stats/data/mdrr/man/update_k.html
