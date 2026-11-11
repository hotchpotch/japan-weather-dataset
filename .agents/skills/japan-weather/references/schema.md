# Tables, columns and conventions

The authoritative column list is in the dataset card
(`README.md` of the dataset, also in a local copy) and in
[docs/dataset.md](https://github.com/hotchpotch/japan-weather-dataset/blob/main/docs/dataset.md).
This file is a summary for writing queries.

## Tables

| table | files | key | rows (2026-11) |
|---|---|---|---|
| `stations` | `stations/stations.parquet` | `station_id` | 1,678 (159 official, 1,519 AMeDAS; 1,287 active) |
| `daily` | `daily/year=YYYY/*.parquet` | (`station_id`, `date`) | 26.5 M, 1872-01-01 .. |
| `hourly` | `hourly/year=YYYY/*.parquet` | (`station_id`, `date`, `hour`) | 601 M, 1885-01-01 .. |
| `homogeneity_breaks` | `homogeneity_breaks/homogeneity_breaks.parquet` | (`station_id`, `frequency`, `element`, `date`, `hour`) | ~13,500 daily |

Rows exist only within a station's observation period; a row where no
element was observed is omitted. Elements a station does not observe are
null with quality 0 (or null).

## daily vs hourly

| | `daily` (日別値) | `hourly` (時別値) |
|---|---|---|
| period | official stations from 1872 (a few; most from 1880–1950s), AMeDAS from 1976 | official stations from 1885 (most later), AMeDAS from 1976 |
| size | ~215 MB in all; ~4–5 MB per recent year | ~1.9 GB in all; ~35–42 MB per year since the 1990s |
| elements | 23: mean/max/min temperature, precipitation total / max 1 h / max 10 min, sunshine, solar radiation, max snow depth, snowfall total, mean/max/gust wind, most frequent direction, vapor pressure, mean/min humidity, local/sea-level pressure (mean, min), cloud cover, weather summary text (day / night) | 15: temperature, precipitation, snowfall, snow depth, sunshine, wind speed and direction, solar radiation, local/sea-level pressure, humidity, vapor pressure, dew point, weather code, cloud cover, visibility |
| use for | long-term trends, extremes by day (猛暑日, heavy rain days), snow seasons, anything across many stations and decades | diurnal cycle, nights (accurate 熱帯夜), short heavy rain, time of events, aggregating to custom day boundaries |
| time | `date` = JST calendar day | `hour` 1..24 = the hour *ending* at `hour`:00 JST; hour 24 is midnight at the end of `date`; `datetime_jst` = end of the hour (naive timestamp) |

Prefer `daily` when it has the element: it is about 25 times smaller and
JMA's daily statistics (max, min, totals) already follow JMA's rules. Use
`hourly` for what daily values cannot answer, and download only the years
needed.

## stations

| column | meaning |
|---|---|
| `station_id` | `s` + 5 digits (official station, 気象台・測候所等) or `a` + 4 digits (AMeDAS) |
| `station_type` | `official` or `amedas` |
| `name`, `name_kana` | station name (Japanese, katakana) |
| `prefecture_code`, `prefecture_name` | JMA's area: prefectures, but Hokkaido is split into 14 areas (codes 11–24, e.g. 石狩, 上川), and 99 = 南極 (Syowa) |
| `latitude`, `longitude`, `elevation_m` | current (or last) position; past relocations are not reflected |
| `kansoku` | JMA's observation flags (6 digits) |
| `observed_categories` | `rain,wind,temperature,sunshine,snow,other` subset; `other` = humidity, pressure, etc. |
| `ended_on` | last day of observation; null = active |
| `listed` | true when in JMA's current station list |
| `daily_first_year` … `hourly_last_year` | years with data |

## Value columns

Every element `<col>` has:

- `<col>_quality` (UTINYINT): 8 normal, 5 quasi-normal (some source data
  missing within the allowed range), 4 insufficient data, 2 questionable,
  1 missing, 0 not an observed element. **Use 5 and 8.**
- `<col>_no_phenomenon` (BOOLEAN; precipitation, snow, sunshine): true when
  the phenomenon did not occur (value null). Official stations only; null for
  AMeDAS, where "none" is a 0 value. Treat no phenomenon as 0.
- wind maxima and hourly wind: `<col>_direction` (1 = NNE … 16 = N, 0 = calm)
  and `<col>_direction_quality`.
- `cloud_cover_modifier` (hourly): `+` / `-` after the value (`0+`, `10-`).
- `weather_day` / `weather_night` (daily, official stations): JMA's weather
  summary text in Japanese (06–18 / 18–06 JST). `weather` (hourly): JMA's
  present weather code.

`updated_at` (UTC): when the dataset first fetched the row with its current
values. A later value than the first fetch means JMA changed the row.

## homogeneity_breaks

Dates where JMA's homogeneity number (均質番号) of an element changes —
relocations, new instruments, changed methods. Values on both sides of a
break are not directly comparable. Columns: `station_id`, `frequency`
(`daily`/`hourly`), `element` (column name), `date`, `hour` (0 for daily).
Counts of daily breaks by element: `precipitation_max_1h` 2,460,
`sunshine_duration` 2,312, `temperature_max`/`_min` 1,670 each,
`temperature_mean` 145, `snowfall_total` 229, `snow_depth_max` 13. See
[pitfalls.md](pitfalls.md) for what that means for trends.

## manifest.json

```json
{
  "updated_at": "2026-11-11T01:11:50Z",
  "daily":  {"latest_date": "2026-11-10", "year_files_through": "2026-09-30",
             "month_files": [{"month": "2026-10", "path": "daily/year=2026/2026-10.parquet",
                              "first_date": "2026-10-01", "last_date": "2026-10-31",
                              "rows": 39897, "missing_days": []}, "..."]},
  "hourly": {"...": "same"}
}
```

- `latest_date`: last day in the dataset.
- `year_files_through`: last day in the year files; days after it are in
  month files and may still be revised by JMA.
- `missing_days`: days of a month file without any row.
