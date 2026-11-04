# Dataset definition and update strategy

## What is collected

| | frequency | stations | elements |
|---|---|---|---|
| `daily` | 日別値 | all official + AMeDAS stations | 23 |
| `hourly` | 時別値 | all official + AMeDAS stations | 15 |
| `stations` | station list | 1,678 (2026-11) | |
| `homogeneity_breaks` | changes of 均質番号 | | |

Aggregates over longer periods (5-day, 10-day, monthly, 3-monthly) and
normals (平年値) are not collected: they can be computed from daily
values, and JMA's own monthly values follow statistical rules (e.g. the
quality of a month depends on missing days) that users can apply as needed.
They may be added later as separate configs.

### Daily elements

| column | obsdl | 日本語 | unit | official | AMeDAS |
|---|---|---|---|---|---|
| temperature_mean / _max / _min | 201 / 202 / 203 | 平均・最高・最低気温 | °C | ✓ | ✓ |
| precipitation_total | 101 | 降水量の合計 | mm | ✓ | ✓ |
| precipitation_max_1h / _max_10min | 105 (1 / 10) | 1時間・10分間降水量の最大 | mm | ✓ | ✓ (10 min from 2008–2009) |
| sunshine_duration | 401 | 日照時間 | h | ✓ | ✓ (estimated since 2021-03) |
| solar_radiation_total | 610 | 合計全天日射量 | MJ/m² | ✓ | |
| snow_depth_max / snowfall_total | 501 / 503 | 最深積雪・降雪量合計 | cm | ✓ | snow stations |
| wind_speed_mean | 301 | 平均風速 | m/s | ✓ | ✓ |
| wind_speed_max (+ direction) | 302 | 最大風速 | m/s | ✓ | ✓ |
| wind_gust_max (+ direction) | 304 | 最大瞬間風速 | m/s | ✓ | ✓ |
| wind_direction_mode | 305 | 最多風向 | 16-point | ✓ | ✓ |
| vapor_pressure_mean | 604 | 平均蒸気圧 | hPa | ✓ | humidity stations |
| relative_humidity_mean / _min | 605 / 606 | 平均湿度・最小相対湿度 | % | ✓ | humidity stations |
| local_pressure_mean | 601 | 平均現地気圧 | hPa | ✓ | |
| sea_level_pressure_mean / _min | 602 / 603 | 平均・最低海面気圧 | hPa | ✓ | |
| cloud_cover_mean | 607 | 平均雲量 | 0–10 | ✓ | |
| weather_day / weather_night | 701 / 702 | 天気概況（昼・夜） | text | ✓ | |

### Hourly elements

| column | obsdl | 日本語 | unit | official | AMeDAS |
|---|---|---|---|---|---|
| temperature | 201 | 気温 | °C | ✓ | ✓ |
| precipitation | 101 | 降水量（前1時間） | mm | ✓ | ✓ |
| snowfall / snow_depth | 503 / 501 | 降雪の深さ・積雪の深さ | cm | ✓ | snow stations |
| sunshine_duration | 401 | 日照時間（前1時間） | h | ✓ | ✓ |
| wind_speed (+ direction) | 301 | 風向・風速 | m/s | ✓ | ✓ |
| solar_radiation | 610 | 全天日射量 | MJ/m² | ✓ | |
| local_pressure / sea_level_pressure | 601 / 602 | 現地・海面気圧 | hPa | ✓ | |
| relative_humidity / vapor_pressure / dew_point | 605 / 604 / 612 | 相対湿度・蒸気圧・露点温度 | % / hPa / °C | ✓ | humidity stations |
| weather | 703 | 天気 | code | ✓ | |
| cloud_cover | 607 | 雲量 | 0–10 | ✓ | |
| visibility | 704 | 視程 | km | ✓ | |

Every element has `<column>_quality` (品質情報). Precipitation, snow and
sunshine have `<column>_no_phenomenon` (現象なし情報, official stations
only). Wind maxima and hourly wind have `<column>_direction` and
`<column>_direction_quality`. Cloud cover has `<column>_modifier` (`+`/`-`).
Wind directions: 1 = NNE … 16 = N, 0 = calm.

### Keys and time

- `daily`: (`station_id`, `date`).
- `hourly`: (`station_id`, `date`, `hour`), `hour` = 1..24, value of the
  hour ending at `hour`:00 JST; hour 24 = midnight ending `date` (JMA's
  convention, so that a day is hours 1–24). `datetime_jst` is the end of the
  hour as a naive timestamp.
- Rows where every element has quality 0 (not observed) are omitted, so
  rows exist only within a station's observation period.

### Homogeneity

obsdl numbers homogeneity (均質番号) from 1 at the start of each request,
so the numbers cannot be stored as they are when data is fetched in pieces.
Each request starts one day before the stored range; a change of the number
between consecutive rows is stored as a break (`station_id`, `frequency`,
`element`, `date`, `hour`) in `homogeneity_breaks`. The homogeneity number of
a row is 1 + the number of breaks of its station and element at or before
it.

## Files

```
stations/stations.parquet
daily/year=YYYY/data.parquet        sorted by station_id, date
hourly/year=YYYY/data.parquet       sorted by station_id, date, hour
homogeneity_breaks/homogeneity_breaks.parquet
README.md                           dataset card (YAML configs for the Hub)
```

Yearly files keep each file in the order of 10–300 MB, let the Hub's
viewer and SQL console read them, and limit a daily upload to the files of
the changed years (normally only the current year).

Internally `jwd` keeps one Parquet file per (frequency, station, year)
under `chunks/` plus a DuckDB state file. A refetch replaces exactly one
station-year (or the refetched days of it); export concatenates the chunks
of changed years.

## Collection

### 1. Initial backfill

1. `stations`: station list (61 requests).
2. `probe`: for each station, the years with data and the observed
   element categories, by requesting four days of every year
   (`interAnnualType=2`, Jan/Apr/Jul/Oct 15th). Hourly coverage is probed
   separately for official stations (their hourly records start later than
   daily ones). Stations whose whole record is shorter than the gap
   between samples could be missed; a station's planned years are widened
   by one year on both sides.
3. `backfill`: one unit is (station, year). Units with the same element
   set are packed into one request up to the size budget; newest years
   first, so recent data becomes usable early. AMeDAS stations are asked
   only for the categories they observe (from `kansoku` and the probe),
   which avoids sending 15 elements for the ~680 rain-only stations.

The backfill is resumable: a station-year is skipped when its chunk covers
the whole year.

### 2. Daily update

`update` (run once a day after ~01:00 JST):

- refetches the last `-lookback-days` (default 31) up to yesterday for
  every active station (JMA restores or corrects recent data days to weeks
  later), and
- refreshes `-refresh-jobs` requests worth of the chunks fetched longest
  ago (JMA also corrects data of past years; see `correctHistory.json`).
  With ~100 refresh requests a day the whole archive is refetched roughly
  once a year.
- probes stations that appeared in the station list since the last run.

Rate limits differ between the two modes: the backfill is a long, steady
run (tens of thousands of requests over days) and should be run off-peak
with the default interval; the daily update is about 150 requests.

### Failure handling

- Too large (error page) or repeated HTTP 500/502/504: the request is split
  (by stations, then by period) and retried, and the request size limit is
  halved for the rest of the run (down to 1,000 cells); planned requests
  above the limit are split before they are sent. The server answers about
  2,500 cells per second and times out after about 60 s, so it slows down
  when busy; halving finds a size that fits without tuning `-budget`.
- Changes of the CSV layout (element or station order, unknown attribute
  columns) abort the run instead of storing misaligned values.
- Every request is logged in the state DB (`requests` table) with its
  status and SHA-256; `-keep-raw` keeps the responses.

### Access restrictions

JMA may tighten access if traffic grows (the page already asks not to put
excessive load with automated tools, and answers HTTP 429 when busy).
`jwd` separates restrictions from ordinary failures:

| detected as | from | behaviour |
|---|---|---|
| rate limited | HTTP 429 / 503, pages with 「アクセスが集中」「混み合」 | double the interval between requests (up to 60 s) for the rest of the run; wait 1, 2, 4 … up to 30 min (or `Retry-After` if longer) and retry; after `-max-rate-limit-hits` (6) consecutive hits stop with exit code 3 |
| blocked | HTTP 401 / 403 / 451, pages with 「アクセス制限」「アクセスを拒否」, "Request blocked", "Access denied" | no retry; stop with exit code 4 |
| maintenance | page with 「メンテナンス」 | no retry; stop with exit code 5 |
| own quota | `-max-requests-per-24h N` (counted from the request log across runs) | stop before sending with exit code 6 |

How to notice:

- Log lines (stderr, `slog` text): `level=WARN msg="obsdl access limited" event=rate_limited|slowed_down …`,
  `level=INFO msg="obsdl access recovered"`, `level=ERROR msg="obsdl access restricted" event=blocked|gave_up|maintenance …`,
  with HTTP status, consecutive hits, wait, interval and the server's message.
- `<data-dir>/access_status.json`: `state` = `ok` / `rate_limited` /
  `blocked` / `maintenance` / `quota_exhausted`, `since`, the last event and
  the current interval. Written on every event and at the end of each
  network command; meant for monitoring (e.g. alert when not `ok`).
- `access_events` table in the state DB (every event) and `jwd status`.
- Exit codes 3–6 above (1: other errors, 130: interrupted).

After a block, check the service in a browser and the JMA notices before
resuming, and resume with a longer `-interval` and a
`-max-requests-per-24h` limit. Do not work around a restriction.
