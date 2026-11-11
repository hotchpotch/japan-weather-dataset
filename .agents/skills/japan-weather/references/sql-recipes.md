# SQL recipes (DuckDB)

All queries assume the views below over the local copy in
`~/.cache/japan-weather/data` (see [download.md](download.md); use the
actual directory when `XDG_CACHE_HOME` or `JW_DIR` is set; DuckDB expands `~`). Every query here was run against the
dataset; change station ids, dates and thresholds as needed.

## Setup

```sql
SET enable_progress_bar = false;
CREATE OR REPLACE VIEW stations AS SELECT * FROM '~/.cache/japan-weather/data/stations/stations.parquet';
CREATE OR REPLACE VIEW daily    AS SELECT * FROM read_parquet('~/.cache/japan-weather/data/daily/*/*.parquet', hive_partitioning = false);
CREATE OR REPLACE VIEW hourly   AS SELECT * FROM read_parquet('~/.cache/japan-weather/data/hourly/*/*.parquet', hive_partitioning = false);
CREATE OR REPLACE VIEW breaks   AS SELECT * FROM '~/.cache/japan-weather/data/homogeneity_breaks/homogeneity_breaks.parquet';
```

`hive_partitioning = false` drops the `year` column that DuckDB would add
from the directory names. With only some years downloaded, the views see only
those years. Keep a filter on `date` in every query on `hourly`: the
Parquet files are sorted by station and date, so DuckDB skips row groups.

To read straight from the Hub instead (no download; slower, every query
transfers data), use `hf://datasets/hotchpotch/japan-weather/...` as the
path. For a private or gated repository, create a secret first:
`CREATE SECRET hf (TYPE huggingface, PROVIDER credential_chain);` (uses
`HF_TOKEN` or the token of `hf auth login`).

## Freshness of the copy

```sql
SELECT daily.latest_date        AS daily_latest,
       daily.year_files_through AS daily_settled_through,
       hourly.latest_date       AS hourly_latest,
       updated_at
FROM read_json('~/.cache/japan-weather/data/manifest.json');
```

## Stations

```sql
-- By name (Japanese) or prefecture
SELECT station_id, station_type, name, prefecture_name, latitude, longitude,
       elevation_m, daily_first_year, ended_on, observed_categories
FROM stations
WHERE name LIKE '%札幌%' OR prefecture_name = '沖縄'
ORDER BY station_type DESC, station_id;
```

```sql
-- Nearest active stations that observe temperature to a point (Kyoto Station)
SELECT station_id, name, station_type, elevation_m,
       round(6371 * 2 * asin(sqrt(
         pow(sin(radians(latitude - 34.985) / 2), 2) +
         cos(radians(34.985)) * cos(radians(latitude)) *
         pow(sin(radians(longitude - 135.758) / 2), 2))), 1) AS km
FROM stations
WHERE ended_on IS NULL AND observed_categories LIKE '%temperature%'
ORDER BY km
LIMIT 5;
```

## Daily values

```sql
-- One station, one month, usable values only
SELECT date, temperature_max, temperature_min, precipitation_total
FROM daily
WHERE station_id = 's47662'                    -- 東京
  AND date BETWEEN DATE '2025-08-01' AND DATE '2025-08-31'
  AND temperature_max_quality IN (5, 8)
ORDER BY date;
```

```sql
-- Precipitation with "no phenomenon" as 0 and missing values excluded
SELECT date,
       CASE WHEN precipitation_total_quality IN (5, 8)
            THEN coalesce(precipitation_total, 0) END AS precip_mm
FROM daily
WHERE station_id = 's47662' AND date BETWEEN DATE '2024-06-01' AND DATE '2024-06-10'
ORDER BY date;
```

```sql
-- Monthly mean temperature; a month counts only with >= 80 % of its days
SELECT date_trunc('month', date) AS month,
       round(avg(temperature_mean), 2) AS t_mean,
       count(*) AS days
FROM daily
WHERE station_id = 's47662' AND temperature_mean_quality IN (5, 8)
  AND date >= DATE '2020-01-01'
GROUP BY ALL
HAVING count(*) >= 0.8 * day(last_day(min(date)))
ORDER BY month;
```

```sql
-- Monthly anomaly against the 1991-2020 mean of the same month
WITH m AS (
  SELECT year(date) AS y, month(date) AS mo, avg(temperature_mean) AS t, count(*) AS n
  FROM daily
  WHERE station_id = 's47662' AND temperature_mean_quality IN (5, 8)
  GROUP BY ALL
  HAVING count(*) >= 25
), normal AS (
  SELECT mo, avg(t) AS t_norm FROM m WHERE y BETWEEN 1991 AND 2020 GROUP BY mo
)
SELECT y, mo, round(t - t_norm, 2) AS anomaly
FROM m JOIN normal USING (mo)
WHERE y >= 2024
ORDER BY y, mo;
```

```sql
-- Days >= 35 °C (猛暑日) per year at stations with complete years
SELECT station_id, year(date) AS y,
       count(*) FILTER (WHERE temperature_max >= 35) AS hot_days,
       count(*) AS valid_days
FROM daily
WHERE station_id IN ('s47662', 's47772', 's47636')   -- 東京・大阪・名古屋
  AND temperature_max_quality IN (5, 8)
  AND date BETWEEN DATE '2015-01-01' AND DATE '2025-12-31'
GROUP BY ALL
HAVING count(*) >= 330
ORDER BY station_id, y;
```

```sql
-- Heaviest daily precipitation on one day across all stations
SELECT s.name, s.prefecture_name, d.precipitation_total
FROM daily d JOIN stations s USING (station_id)
WHERE d.date = DATE '2019-10-12' AND d.precipitation_total_quality IN (5, 8)
ORDER BY d.precipitation_total DESC
LIMIT 10;
```

```sql
-- Weather summary text (official stations only)
SELECT date, weather_day, weather_night
FROM daily
WHERE station_id = 's47662' AND date BETWEEN DATE '2024-02-01' AND DATE '2024-02-29'
  AND (weather_day LIKE '%雪%' OR weather_night LIKE '%雪%')
ORDER BY date;
```

## Snow (winter years)

A winter is July to June (winter 2025 = 2025-07-01 .. 2026-06-30).

```sql
-- Maximum snow depth and snow-cover days per winter, winters with >= 90 % of Dec-Mar days
SELECT station_id,
       year(date - INTERVAL 6 MONTH) AS winter,
       max(coalesce(snow_depth_max, 0)) AS max_depth_cm,
       count(*) FILTER (WHERE snow_depth_max >= 1) AS snow_cover_days,
       count(*) FILTER (WHERE month(date) IN (12, 1, 2, 3)) AS djfm_days
FROM daily
WHERE station_id IN ('s47604', 's47605')          -- 新潟・金沢
  AND snow_depth_max_quality IN (5, 8)
  AND date >= DATE '2015-07-01'
GROUP BY ALL
HAVING count(*) FILTER (WHERE month(date) IN (12, 1, 2, 3)) >= 110
ORDER BY station_id, winter;
```

## Hourly values

```sql
-- Hours of one day; hour 24 is midnight at the end of the day
SELECT datetime_jst, hour, temperature, precipitation, wind_speed, wind_speed_direction
FROM hourly
WHERE station_id = 's47662' AND date = DATE '2025-08-05'
ORDER BY hour;
```

```sql
-- Tropical nights from hourly values: minimum of 19:00 .. 07:00 JST >= 25 °C
-- (the daily minimum covers 00:00-24:00, so it only approximates this)
WITH h AS (
  SELECT station_id, temperature,
         CAST(datetime_jst - INTERVAL 8 HOUR AS DATE) AS night_of   -- 19:00 day D .. 07:00 day D+1 -> D
  FROM hourly
  WHERE station_id = 's47662'
    AND date BETWEEN DATE '2025-07-01' AND DATE '2025-09-01'
    AND temperature_quality IN (5, 8)
    AND (hour(datetime_jst) >= 19 OR hour(datetime_jst) <= 7)
)
SELECT night_of, min(temperature) AS night_min
FROM h
GROUP BY ALL
HAVING count(*) >= 12 AND min(temperature) >= 25
ORDER BY night_of;
```

```sql
-- Hourly to daily: daily maximum 1-hour precipitation in a heavy-rain event
SELECT station_id, date, max(precipitation) AS max_1h, sum(precipitation) AS total
FROM hourly
WHERE date BETWEEN DATE '2020-07-03' AND DATE '2020-07-04'
  AND station_id IN (SELECT station_id FROM stations WHERE prefecture_name = '熊本')
  AND precipitation_quality IN (5, 8)
GROUP BY ALL
ORDER BY max_1h DESC
LIMIT 10;
```

## Homogeneity breaks

```sql
-- Breaks of the mean temperature of a station (relocations, instruments)
SELECT date, element
FROM breaks
WHERE station_id = 's47662' AND frequency = 'daily' AND element = 'temperature_mean'
ORDER BY date;
```

```sql
-- Use only the homogeneous part after the last break
WITH last_break AS (
  SELECT coalesce(max(date), DATE '1800-01-01') AS d
  FROM breaks
  WHERE station_id = 's47662' AND frequency = 'daily' AND element = 'temperature_mean'
)
SELECT year(date) AS y, round(avg(temperature_mean), 2) AS t_mean, count(*) AS n
FROM daily, last_break
WHERE station_id = 's47662' AND date >= last_break.d AND temperature_mean_quality IN (5, 8)
GROUP BY ALL
HAVING count(*) >= 330
ORDER BY y;
```
