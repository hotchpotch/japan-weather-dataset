# Operation: daily and monthly updates

> **Status.** Implemented: the file layout with month files, `manifest.json`,
> `jwd month` and `jwd monthly` with their GitHub Actions workflows, and the
> released image that runs both. Planned, not implemented yet: `jwd refetch`,
> `jwd corrections` (JMA's announced corrections), `jwd latest`, the
> `status` column and the `corrections` output (marked "planned" below).
> When a planned part is implemented, update this document (and README /
> dataset.md) to describe what was actually built, and remove its "planned"
> marks.

The published dataset is updated by two GitHub Actions workflows, both
without the crawl state: they download the files they need from the Hub,
work on them in a temporary state and upload what they wrote.

| | daily | monthly |
|---|---|---|
| when | every day, 05:17 JST | the 1st of each month, 20:00 JST |
| workflow / script | `daily.yml` / `jwd-daily.sh` (`jwd month`) | `monthly.yml` / `jwd-monthly.sh` (`jwd monthly`) |
| downloads from the Hub | `manifest.json`, `stations.parquet`, the current month files (~5 MB) | the metadata, all month files, the year files of the recent years (~50 MB) |
| fetches | the last 3 days of the current month, up to 3 missing days before them | the station list; the two months before the current one to yesterday |
| writes | the current month files, `manifest.json` | the year files of the recent years, month files (deletes the merged one), stations, homogeneity breaks, card, manifest |
| requests / time | ~64 / ~9 min (measured) | ~260 / ~40 min (planned from 195 + 61 requests) |
| probes new stations | no | yes |

Both rebuild `manifest.json` from the files they have: the monthly run
from the recent years and all month files, the daily job from its month
files plus the entries of the downloaded manifest for files it did not
download (entries more than two months before the current month are
dropped as stale). They share a concurrency group, so they never run at
the same time; the monthly run on the evening of the 1st starts long after
the daily one of the morning and leaves a complete manifest.

Both run the latest released image (`:latest`, see
[releases](#releases-and-the-image)), so a change reaches them only with a
new release, and with the next run after it.

The crawl state (`jwd backfill`, `jwd update`, `jwd export` on a machine)
is needed only to build the dataset from scratch. Users who need values
newer than the dataset can add them locally (see
[latest values](#latest-values-for-users-planned)).

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
- older changes come from the announced corrections (planned).

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

The recent months are the month of the day after the latest date and the
month before; `jwd export` writes them as month files (a month without rows
gets none) and everything older into the year files. When the latest date
reaches the end of a month, the next export moves the oldest month file into
its year file and deletes the month file. A
month is never in both. Readers glob `daily/*/*.parquet` and see one table.

Example, December 2026:

```
hourly/year=2026/data.parquet       2026-01 .. 2026-10   monthly run
hourly/year=2026/2026-11.parquet    November             monthly run (fetched again on 12-01)
hourly/year=2026/2026-12.parquet    December             jwd month, every day
```

Counting from the day after the latest date makes the monthly run of 12-01
(latest date 11-30) move October already, so November is the only month
file until the daily job of 12-02 starts December. If the monthly run fails,
the daily jobs go on and there are three month files until it is repeated.
That is valid: the files do not overlap.

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

`.github/workflows/daily.yml` runs daily at 05:17 JST (after JMA's 01:00
and 03:00 updates; not on the hour, when scheduled runs are delayed most).
GitHub starts scheduled runs late, often by several hours, so the Hub is
usually updated in the morning or around noon JST; users should read
`latest_date` in `manifest.json` rather than rely on a time.
It pulls the image `JWD_IMAGE` (`:latest`, the latest release)
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

Logs: jwd logs every request while the job runs (shown live on the run's
page; `gh run view --log` only after the job ends): `job progress=i/N
elapsed=… eta=…` before a request (the estimate is the average time of a
request so far times the requests left) and `stored rows=…` after it, and
`finished jobs` with the totals. At the end, `jwd month` and `jwd monthly`
append a table to the job summary of the run (the file named by
`GITHUB_STEP_SUMMARY`, mounted into the container by both workflows): the
result and exit code, the elapsed time, the data requests and their rows,
the access limits met (rate limits, blocks) and the latest day, year files
and month files (with missing days) of `manifest.json`. The scripts add
the commit of the upload, or that it failed.

The same script runs outside GitHub with `.env` (or the environment)
providing `HF_DATASET_REPO`, `HF_TOKEN` and `JWD_EXPORT_DIR` (a work
directory).

Notes for a public repository:

- The job runs only in this repository (`if: github.repository == …`), so
  that forks do not send scheduled requests to JMA.
- GitHub runners share IP addresses. JMA's rate limiting may answer 429
  sooner than at home, or block an address. The job uses `-interval 6s
  -min-interval 5s`. Do not work around a restriction (see
  [dataset.md](dataset.md#access-restrictions)).
- A block (exit code 4) fails the job and disables `daily.yml` and
  `monthly.yml` (the jobs have `actions: write` for this), so that no
  scheduled run sends requests until the maintainer has checked the
  service and the JMA notices. Then enable both again by hand
  (`gh workflow enable daily.yml`, `gh workflow enable monthly.yml`),
  possibly with a longer `-interval`. A rate limit the client gave up on
  (exit code 3) or maintenance (exit code 5) only fails the job; the next
  scheduled run tries again.
- Scheduled runs can be delayed or skipped when GitHub is busy; the gap
  filling covers a few skipped days.
- GitHub disables scheduled workflows of a public repository after 60 days
  without activity in the repository. Uploads to the Hub do not count.
  `.github/workflows/keepalive.yml` enables `daily.yml`, `monthly.yml` and
  itself again on the 1st and 15th of each month, but only when they are
  active or were disabled by GitHub for inactivity; a workflow disabled by
  hand or after a block stays disabled. That enabling resets the period is
  not documented by GitHub; keepalive tools rely on it. If the workflows
  are disabled for inactivity anyway, `gh workflow enable daily.yml`
  re-enables one by hand.
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

`.github/workflows/monthly.yml` runs on the 1st of each month at 20:00 JST
(the last month has just ended; far from the daily job at 05:17;
`workflow_dispatch` with `dry_run` prints the plan). It pulls the image `JWD_IMAGE` and runs `scripts/jwd-monthly.sh`
in it:

1. `hf download` `manifest.json`, `stations/stations.parquet`,
   `homogeneity_breaks/homogeneity_breaks.parquet`, all month files and the
   year files of the years `jwd monthly` rewrites: from the year of the
   month two months before the current one to the year of yesterday (on
   12-01: 2026; on 01-01: 2026; on 02-01: 2026 and 2027).
2. `jwd monthly -dataset <dir>`:
   1. load the stations, the homogeneity breaks and the year and month files
      of those years into a temporary state (about 30 s for 2026);
   2. `stations` (61 requests; `-skip-stations` to skip) and a probe of
      new stations;
   3. refetch from the first day of the month two months before the
      current one to yesterday for every active station, daily and hourly. On 12-01: 10-01..11-30, so
      October and November are fetched again after they ended (and days
      the daily job could not fill are filled); rows that did not change
      keep their `updated_at`;
   4. check the ended months: the refetch replaces the stored rows of its
      range, so an incomplete answer from JMA would remove rows, and the
      oldest month would be settled in its year file without being fetched
      again. When a station loses at least two days of rows of an ended
      month (2 daily or 48 hourly rows) and more than a tenth of them, the
      run stops with exit code 8 and writes nothing (nothing is uploaded).
      Check the source, then run it again, or pass `-allow-row-loss` when
      JMA really withdrew the data;
   5. export: October goes into the year file of 2026; November is a
      month file (December too when the run is repeated later in the
      month and it has rows); stations, homogeneity breaks, the card and
      `manifest.json` are rewritten.
3. `hf upload` the directory with `--delete "*/year=*/????-??.parquet"`,
   which removes the month file that went into a year file. Files of other
   years are not in the directory and stay as they are.

`jwd monthly` refuses to run when a year file listed by `manifest.json`
(`year_files_through`) is missing, or a month file is older than the years
it reads: the export would lose rows. A failure before the export leaves
the directory as it was and nothing is uploaded; the run can be repeated
(by hand with `workflow_dispatch`).

Plan measured on 2026-11-11 (`jwd monthly -dry-run` on the 2026 files
downloaded from the Hub, yesterday 11-10): 195 requests for 09-01..11-10,
1,287 stations; plus 61 requests for the station list. At about 7 s per
request that is about 30 minutes, well within the 6 hours of a
GitHub-hosted job; the downloads are about 50 MB.

Station-years older than the month before last are not fetched again by
the updates. Changes JMA makes to them are expected to come with the
announced corrections (see below). Until those are implemented, a full
refetch needs the crawl state (`jwd update` with `-refresh-jobs`).

Planned additions to the monthly run: fetch `correctHistory.json` and
record entries not seen before as `pending` (see below).

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
- Monthly (`jwd-monthly.sh`): its work directory (the recent years, all
  month files and the metadata); files whose content did not change are
  skipped by the Hub client, month files merged into a year file are
  deleted.
- The commit message carries the latest date and, once implemented, the
  IDs of the corrections applied.

## Monitoring

Both workflows report to [Healthchecks.io](https://healthchecks.io/), which
alerts the maintainer (by e-mail) when a run failed or did not happen,
e.g. a schedule GitHub skipped or a workflow that was disabled.

| check | secret | schedule on Healthchecks.io | alerts |
|---|---|---|---|
| daily | `HEALTHCHECKS_DAILY_URL` | period 1 day, grace 12 hours | no success for 36 hours, or a failed run |
| monthly | `HEALTHCHECKS_MONTHLY_URL` | cron `0 20 1 * *` (Asia/Tokyo), grace 1 day | no success by the 2nd 20:00 JST, or a failed run |

The daily period does not follow the cron of `daily.yml`, because GitHub
starts the run several hours late. A monthly run started by hand counts
as the run of that month.

Each job pings `<url>/start` first and `<url>/<exit code>` last (`/0` on
success, the exit code of `jwd-daily.sh` or `jwd-monthly.sh` on a failure,
`/fail` when an earlier step failed), with the run's URL as the body. A
dry run of the monthly workflow only sends `<url>/log`, which is recorded
without changing the check. A secret is the check's ping URL (without a
trailing slash). Without it (e.g. in a fork) the steps do nothing, and a
ping that fails after retries only adds a warning: the monitoring never
fails an update. A block (exit code 4) is reported as a failure, and as
the workflows are then disabled, the check stays down until they run
again. The workflow files are read from `main`, so a change here needs no
release.

## Releases and the image

The `Dockerfile` builds an image with `jwd`, the `hf` CLI and `scripts/`
(on `PATH`; `JWD_DATA_DIR=/data`, `JWD_EXPORT_DIR=/dataset`). A tag
`vX.Y.Z` publishes it as `ghcr.io/hotchpotch/japan-weather-dataset:vX.Y.Z`
and moves `:latest` to it; both workflows run `:latest` (`JWD_IMAGE` in
`daily.yml` and `monthly.yml`). See
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

1. This document; month files, `manifest.json`, `jwd month`, `jwd monthly`
   without the crawl state, their workflows (done).
2. `refetch`, `corrections`; run the first monthly update and measure
   requests and changed rows.
3. `latest`.

[update]: https://ds.data.jma.go.jp/obd/stats/data/mdrr/man/update_k.html
