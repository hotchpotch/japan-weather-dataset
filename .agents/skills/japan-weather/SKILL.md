---
name: japan-weather
description: Query and analyze Japanese weather observations (JMA official stations and AMeDAS, daily values from 1872 and hourly values from 1885, updated daily) from the japan-weather Parquet dataset on Hugging Face with DuckDB. Use for questions and research about Japan's temperature, heat days, rain, snow, wind, sunshine or climate trends at stations or regions (気象庁・アメダス・過去の気象データ・気温・降水量・積雪・温暖化), and for downloading or updating a local copy of the dataset.
license: MIT
---

# japan-weather

Daily and hourly surface observations of all JMA stations — about 160
official stations (気象台・測候所) and 1,500 AMeDAS stations, 1,287 active —
from JMA's 「過去の気象データ・ダウンロード」, as Parquet on the Hugging Face
Hub: <https://huggingface.co/datasets/hotchpotch/japan-weather>. Built by
<https://github.com/hotchpotch/japan-weather-dataset>.

## What it can answer

- Station records and time series: temperature (mean/max/min), precipitation,
  snow depth and snowfall, wind and gusts, sunshine, solar radiation,
  humidity, pressure, cloud cover, weather summaries.
- Extremes and counts: 猛暑日 / 真夏日 / 熱帯夜 / 冬日, heavy-rain days,
  annual maxima, records, the heaviest day of an event across all stations.
- Climate change: trends since the 1890s (official stations) or 1976
  (AMeDAS), anomalies against 1991–2020, regional differences, seasons.
- Hourly detail: diurnal cycles, night minimums, timing of rain or wind
  peaks, custom day boundaries.

Not in the dataset: forecasts, radar, upper air, sea observations, JMA's
monthly statistics and normals (compute them from daily values).

## Workflow

1. **Get only the needed parts** into the local copy, then query locally.
   The copy is in `~/.cache/japan-weather/data` (`$XDG_CACHE_HOME` instead
   of `~/.cache` when set; `JW_DIR` overrides it), shared by all projects:

   ```sh
   scripts/sync.sh daily                  # metadata + all daily (~215 MB)
   scripts/sync.sh daily hourly:2024-2026
   ```

   `scripts/` is in this skill's directory. The script uses `hf` (or
   `uvx --from huggingface_hub hf`), fetches only changed files when run
   again, and removes month files the Hub has merged into year files. Plain
   `hf download` commands, sizes and auth: [references/download.md](references/download.md).
2. **Check freshness** in `manifest.json` of the copy (`latest_date`,
   `year_files_through`) and state it in the answer.
3. **Find stations** in `stations` (Japanese names; `s#####` official,
   `a####` AMeDAS). Prefer official stations for long records.
4. **Query with DuckDB**, filter `*_quality IN (5, 8)`, count no phenomenon
   as 0, require complete periods: [references/sql-recipes.md](references/sql-recipes.md).
5. **Before any trend or comparison across decades**, read
   [references/pitfalls.md](references/pitfalls.md) (homogeneity steps,
   station sets, the AMeDAS 2003 max/min step, snowfall 2005, 1-hour
   precipitation 2008) and check `homogeneity_breaks`.
6. **Report** the stations, period, filters, data date and the source
   credit below.

```sql
CREATE VIEW daily AS SELECT * FROM read_parquet('~/.cache/japan-weather/data/daily/*/*.parquet', hive_partitioning = false);
CREATE VIEW stations AS SELECT * FROM '~/.cache/japan-weather/data/stations/stations.parquet';
SELECT date, temperature_max FROM daily
WHERE station_id = 's47662' AND date >= DATE '2025-08-01' AND temperature_max_quality IN (5, 8)
ORDER BY date LIMIT 10;
```

Columns, daily vs hourly, the hour convention and `manifest.json`:
[references/schema.md](references/schema.md).

## Data status

- Daily from 1872-01-01, hourly from 1885-01-01, both to yesterday (JST).
- The Hub is updated every day around 05:30 JST (the last 3 days of the
  current month are fetched again; recent values are preliminary and JMA
  revises them for days). The previous month is fetched again on the 1st of
  the next month (20:00 JST) and once more a month later, when it is merged
  into its year file; older years are treated as final (JMA's announced
  corrections are not applied yet).
- Days after `year_files_through` are in month files and may still change.
- Details and how to fetch days newer than the Hub yourself (`jwd month`
  with Go or Docker):
  [docs/local-copy.md](https://github.com/hotchpotch/japan-weather-dataset/blob/main/docs/local-copy.md),
  [docs/operations.md](https://github.com/hotchpotch/japan-weather-dataset/blob/main/docs/operations.md).

## Rules

- Do not crawl JMA for data that is in the dataset. If newer days are
  essential, use `jwd month` as described in docs/local-copy.md, once, with
  `-dry-run` first; stop on rate limits or blocks (exit codes 3, 4, 5).
- Do not download all hourly values (~1.9 GB) unless the task needs them;
  select years.
- Do not commit downloaded data; never print or commit tokens (`HF_TOKEN`).
- This is a processed copy, not published by JMA. The data is under the
  公共データ利用規約（第1.0版） (PDL 1.0), usable under CC BY 4.0. When
  publishing results (tables, charts, aggregates), follow its terms:
  - Credit the source and state that the user processed it, naming who:
    出典：気象庁ホームページ「過去の気象データ・ダウンロード」
    （https://www.data.jma.go.jp/risk/obsdl/）をもとに（利用者名）が加工して作成.
    Ask the user for the name if unknown. The dataset's processor does not
    have to be named; mentioning the dataset as the route
    (Hugging Face `hotchpotch/japan-weather`) is optional.
  - Do not present results as if JMA or the government made or endorsed
    them (no 「気象庁発表」, 「気象庁作成」, JMA logos or official-looking
    styling).
  - Forecasts and warnings are restricted by the 気象業務法; this dataset
    holds past observations only, so do not present results as forecasts
    or warnings.
