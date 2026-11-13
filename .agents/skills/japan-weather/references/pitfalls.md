# Pitfalls and analysis rules

Lessons from analyses of this dataset (warming, extremes, snow). Most
wrong results come from the points below, not from the statistics.

## Values

- **Quality:** use `*_quality IN (5, 8)`. 4 (insufficient data) and 2
  (questionable) bias extremes and totals; 1 is missing, 0 means the element
  is not observed there.
- **No phenomenon:** precipitation, snow and sunshine are null with
  `*_no_phenomenon = true` at official stations when nothing happened. Count
  those as 0 (`coalesce(value, 0)` after the quality filter). AMeDAS has no
  such flag and reports 0.
- **Complete periods:** a sum or count over a month or year is only valid
  with (nearly) all days. Rules that worked: a month with ≥ 80 % of its days;
  a year with ≥ 330 days (or 12 valid months); a winter with ≥ 110 of the
  121 Dec–Mar days. State the rule in the result.
- **Gaps in the source:** even settled years have hours or days that JMA
  marks missing (1) or insufficient (4), e.g. Tokyo sunshine
  2023-09-09..11 (61 hours missing). They are missing at JMA too, not in the
  copy only; do not try to fetch them again. Say which station-months a rule
  excluded and whether counting quality 4 changes the result. Examples:
  [docs/source.md](https://github.com/hotchpotch/japan-weather-dataset/blob/main/docs/source.md#gaps-in-the-source).
- **Same stations:** the network changes (AMeDAS starts in 1976, stations
  open and close, `ended_on`). Comparing periods with a changing set mixes
  climate change with network change. Choose stations that are complete in
  every period compared, and say how many.

## Homogeneity: steps that are not climate

Check `homogeneity_breaks` for the stations and element before any trend.
Known steps across many stations:

| element | step | consequence |
|---|---|---|
| AMeDAS `temperature_max` / `temperature_min` (daily) | 2003-01-01 at all AMeDAS stations: max about +0.33 °C, min about −0.12 °C (method of daily max/min); more steps in 2008–09 (instruments) | counts of 猛暑日 / 真夏日 before 2003 are too low, 熱帯夜 too high. 猛暑日 grew ~8× (corrected), not ~11× (raw). Correct before comparing, or compare only after 2003 |
| `temperature_mean` | few breaks (145); computed from hourly values | the safest temperature trend element |
| `precipitation_max_1h` (daily) | breaks around 2008 at most stations (2,460 breaks; from hourly to 10-minute updates) | not usable for long-term trends as it is |
| `snowfall_total` | 2005 at about 55 official stations; snowfall per cm of depth +13–15 % after | snowfall trends across 2005 are biased upward; prefer `snow_depth_max` (13 breaks) |
| `sunshine_duration` (AMeDAS) | since 2021-03 estimated from satellite data | not comparable with earlier AMeDAS sunshine |
| station relocations | e.g. 東京 s47662 moved on 2014-12-02 (mean temperature step −0.74 °C) | positions in `stations` are current only |

A simple correction that worked: for each break, compare the station's
monthly anomaly minus a reference series (mean of well-homogeneous stations)
over 60 months before and after (≥ 24 each); add the difference to the data
before the break. Treat per-station corrected trends as approximate; base
national conclusions on many stations.

## National averages

- JMA's national temperature series uses 15 stations with little
  urbanization: 網走 s47409, 根室 s47420, 寿都 s47421, 山形 s47588,
  石巻 s47592, 伏木 s47606, 飯田 s47637, 銚子 s47648, 境 s47742,
  浜田 s47755, 彦根 s47761, 宮崎 s47830, 多度津 s47890, 名瀬 s47909,
  石垣島 s47918. Their annual anomalies against 1991–2020 reproduce JMA's
  published values within 0.01–0.03 °C (+1.43 °C/100 years, 1898–2025).
- Exclude 昭和 (s89532, Antarctica, `prefecture_code` 99) and 南鳥島
  (s47991) from "Japan" aggregates; consider 小笠原 separately.
- Large cities warm faster (urban heat island adds about 1.3–1.6 °C/100 years
  to Tokyo, Osaka, Nagoya and others). Do not use them alone for climate trends.
- `prefecture_name` splits Hokkaido into 14 areas (codes 11–24); group by
  `prefecture_code BETWEEN '11' AND '24'` for Hokkaido as a whole.

## Time

- Hourly `hour` 24 belongs to `date` (midnight at its end). For a night or
  a custom day, shift `datetime_jst` (e.g. `- INTERVAL 8 HOUR` maps 19:00
  of D .. 07:00 of D+1 to D).
- The daily minimum covers the calendar day, so "daily min ≥ 25 °C" only
  approximates 熱帯夜 (the night minimum); use hourly values for accuracy.
- Snow seasons: count winters from July to June
  (`year(date - INTERVAL 6 MONTH)`).
- Recent days (after `year_files_through` in `manifest.json`) are
  preliminary and change; values a few days old change most. For a
  reproducible result, write down `manifest.json`'s `updated_at` and
  `latest_date`, or end the analysis at `year_files_through`.

## Interpretation

- Observations show what changed, not why. Attribution (greenhouse gases vs
  natural variability) needs models; say so rather than claim it.
- Report the period, stations, filters and corrections with every number;
  check trends with a robust method (Sen's slope, Mann–Kendall) and mind
  autocorrelation.
- Snow responds to temperature and precipitation together: warm snowy areas
  near 0 °C lose snow fast with warming, cold areas (Hokkaido inland) much
  less. Do not generalize one region to all of Japan.
