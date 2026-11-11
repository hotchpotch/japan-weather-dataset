# The source: JMA obsdl

JMA's 「過去の気象データ・ダウンロード」 (<https://www.data.jma.go.jp/risk/obsdl/>)
serves past observations of all stations as CSV. It has no documented API;
`jwd` sends the same form requests as the web page. This document records
what the page does, as checked on 2026-11-04. If JMA changes the page,
these details may change and `jwd` fails loudly (it checks the CSV header
against the requested stations and elements).

Related services that `jwd` does not use:

- 「過去の気象データ検索」 (`/stats/etrn/`): HTML tables per station and
  month. Values carry symbols instead of quality numbers and need scraping.
- AMeDAS JSON of the bosai site (`www.jma.go.jp/bosai/amedas/`): 10-minute
  values of the last days only (preliminary).
- The commercial GWO/AMD databases (Weather Toy etc.): not open data.

## Endpoints

All are `POST` with `application/x-www-form-urlencoded`, sent with `Origin`
and `Referer` of the page.

| path | parameters | returns |
|---|---|---|
| `top/station` | `pd=00&format=json` | area table (`prefTable`) with area codes (`prid`) |
| `top/station` | `pd=<area>&format=json` | `markers` of the area: `stid`, `stname`, `prid`, `kansoku`, `ended`, `title` (kana, position, elevation, end date) |
| `show/table` | see below | CSV (`Content-Disposition: attachment`) |
| `web/files/correctHistory.json` (GET) | | JMA's history of data corrections |

Areas are JMA forecast areas: Hokkaido is split into 14 sub-prefectures,
`99` is Antarctica (Syowa station, southern latitude). The 2026-11-04 list
has 61 areas and 1,678 stations: 159 official stations (`s` + 5 digits,
156 active) and 1,519 AMeDAS stations (`a` + 4 digits, 1,131 active).

`kansoku` has one digit per category: rain, wind, temperature, sunshine,
snow, other (humidity etc.). `0` means the category is not observed now.

### show/table

| field | value |
|---|---|
| `stationNumList` | JSON array of station ids |
| `aggrgPeriod` | `9` hourly, `1` daily, `8` N-day, `2` 5-day, `4` 10-day, `5` monthly, `6` 3-monthly |
| `elementNumList` | JSON array of `[element, option]` |
| `interAnnualType` | `1` continuous period, `2` the same month/day range of every year |
| `ymdList` | JSON `[startYear, endYear, startMonth, endMonth, startDay, endDay]` |
| `option` | `{"list":["obs"],"nyear":0}` (observed values only; must not be empty) |
| `rmkFlag` | `1` keep values that need care, with quality numbers |
| `disconnectFlag` | `1` keep values across homogeneity changes, with homogeneity numbers |
| `csvFlag` | `1` flags as numbers |
| `ymdLiteral` | `1` dates as `2026/10/1` / `2026/10/1 1:00:00` |
| `downloadFlag=true`, `youbiFlag=0`, `fukenFlag=0`, `kijiFlag=0`, `jikantaiFlag=0`, `jikantaiList=[]` | |

Errors come back as HTTP 200 HTML pages without the attachment header:

- 「処理の許容範囲を超過しました」: request too large.
- 「メニューページに戻り、もう一度やり直してください」: e.g. the period includes
  today. Data is available up to yesterday (JST), including the current month.
- HTTP 429 「ただいまアクセスが集中しています」: rate limited.

## Request size limit

The page computes the size as stations × elements × time steps (days, or
days × 24 for hourly) and allows up to 200,000. The server enforces it:
one official station with 15 hourly elements for one year (131,760) works,
two years (263,520) is rejected.

The response time grows with the size: on 2026-11-04 the server answered
about 2,500 cells per second, and requests of ~177,000 cells failed with
HTTP 504 after about 60 s (a gateway timeout) while ~99,000 cells came back
in about 37 s. `jwd` therefore packs requests up to 100,000 cells and splits
the period of a station whose year alone is larger (hourly official
stations) before sending.

The page also asks users not to put excessive load with automated tools
(「自動化ツール等による過度のアクセスはお控えいただく」). `jwd` sends one request at
a time, pauses at least 3 s after each response (6 s at the start, lowered
step by step while no request is rate limited), doubles the pause after a
429/503 and backs off exponentially on errors; see
[access restrictions](dataset.md#access-restrictions).

## CSV layout

Shift_JIS (CP932). Rows:

1. `ダウンロードした時刻：2026/11/04 15:13:22`
2. blank
3. station names, one per column
4. element names
5. (only when needed) sub-element names, e.g. `風向`
6. attribute names: blank for a value column, `品質情報`, `現象なし情報`, `均質番号`
7. data rows

Columns come in the order of stations, then elements, as requested. The
attribute columns differ per station type: official stations have
`現象なし情報` for precipitation, snow and sunshine; AMeDAS stations do not.
Wind elements have a direction value with its own quality after the speed.

Hourly rows are labelled by the end of the hour; midnight is written as
`2026/10/2 00:00:00` and belongs to 2026-10-01 (hour 24).

Values: numbers; wind directions in Japanese (`北北東`, `静穏`); hourly
weather as a code; daily weather summaries as Japanese text; cloud amount
may end with `+` / `-` (`0+`, `10-`).

### Flags

| 品質情報 | meaning |
|---|---|
| 8 | normal |
| 5 | quasi-normal (some source data missing within ~20 %) |
| 4 | insufficient data |
| 2 | questionable |
| 1 | missing |
| 0 | not an observed / derived element |

現象なし情報 is 1 when the phenomenon did not occur. 均質番号 starts at 1 at
the beginning of *each request* and increments where the observation
environment changed; `jwd` stores the dates of the changes instead of the
numbers (see `homogeneity_breaks`).

## Coverage

- Official stations: daily values from 1872–1875 (Tokyo: temperature from
  1875), hourly values from about 1890 (Tokyo precipitation).
- AMeDAS: from 1974–1978.
- Hourly sunshine of AMeDAS since 2021-03-02 is estimated from satellite data.
- JMA corrects data after publication; examples in `correctHistory.json`
  include data restored a week later after a network outage (2026-09-28)
  and corrections of periods several years back.

## Terms

Content of the JMA website is available under the 公共データ利用規約（第1.0版）
(PDL 1.0), compatible with CC BY 4.0
(<https://www.jma.go.jp/jma/kishou/info/coment.html>). Required: the source
(出典) and, for processed content, a statement that it was processed; do not
make processed content look as if the government created it. The
restrictions of the 気象業務法 concern forecasts and warnings, not past
observations.
