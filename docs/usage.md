# Using the japan-weather dataset

<!-- The entry point for AI agents: users give its raw URL with a question.
     Links are absolute so that the raw file works on its own. -->

> Daily (from 1872) and hourly (from 1885) surface observations of all JMA stations — about 160 official stations (気象台・測候所) and 1,500 AMeDAS stations — from JMA's 「過去の気象データ・ダウンロード」, as Parquet on the Hugging Face Hub (`hotchpotch/japan-weather`), updated every day. Query it with DuckDB. Not published by JMA: a processed copy under the 公共データ利用規約（第1.0版） (PDL 1.0, compatible with CC BY 4.0).

日本語の紹介ページ: https://hotchpotch.github.io/japan-weather-dataset/

How to use it (for AI agents that can run commands):

1. Read the agent skill (SKILL.md) below first. It is the main guide: workflow, data status, rules and the source credit to use when publishing results. Its relative links (`references/…`, `scripts/…`) are listed here as absolute URLs.
2. Download only the parts you need into `~/.cache/japan-weather/data` with `sync.sh` (needs `hf` or `uvx`, and `python3` or `uv`), for example:

   ```sh
   curl -fsSL -o /tmp/jw-sync.sh https://raw.githubusercontent.com/hotchpotch/japan-weather-dataset/main/.agents/skills/japan-weather/scripts/sync.sh
   bash /tmp/jw-sync.sh daily                 # metadata + all daily values (~215 MB)
   bash /tmp/jw-sync.sh daily hourly:2024-2026  # plus hourly values of some years (all hourly ~1.9 GB)
   ```

3. Query the Parquet files with DuckDB, keep values with quality 5 or 8, and read the pitfalls before comparing decades (homogeneity breaks, station sets, the AMeDAS 2003 step of daily max/min temperature).
4. Do not crawl JMA for data that is in the dataset.
5. When publishing results, credit the source and name who processed it: 出典：気象庁ホームページ「過去の気象データ・ダウンロード」（https://www.data.jma.go.jp/risk/obsdl/）をもとに`{加工者の名前}`が加工して作成. Do not present results as made by JMA or the government.

To keep the skill in a project instead: `npx skills add hotchpotch/japan-weather-dataset --skill japan-weather`.

## Agent skill

- [SKILL.md](https://raw.githubusercontent.com/hotchpotch/japan-weather-dataset/main/.agents/skills/japan-weather/SKILL.md): what the dataset answers, the workflow, data status and rules. Start here.
- [references/schema.md](https://raw.githubusercontent.com/hotchpotch/japan-weather-dataset/main/.agents/skills/japan-weather/references/schema.md): files, columns, quality flags, keys, the hour convention, `manifest.json`.
- [references/sql-recipes.md](https://raw.githubusercontent.com/hotchpotch/japan-weather-dataset/main/.agents/skills/japan-weather/references/sql-recipes.md): DuckDB recipes: setup, freshness of the copy, stations, daily values, snow by winter, hourly values, homogeneity breaks.
- [references/pitfalls.md](https://raw.githubusercontent.com/hotchpotch/japan-weather-dataset/main/.agents/skills/japan-weather/references/pitfalls.md): what breaks long-term comparisons and how to handle it.
- [references/download.md](https://raw.githubusercontent.com/hotchpotch/japan-weather-dataset/main/.agents/skills/japan-weather/references/download.md): `hf download` commands, sizes, authentication.
- [scripts/sync.sh](https://raw.githubusercontent.com/hotchpotch/japan-weather-dataset/main/.agents/skills/japan-weather/scripts/sync.sh): download or update a local copy, only the parts asked for.

## Dataset

- [Dataset card](https://huggingface.co/datasets/hotchpotch/japan-weather/raw/main/README.md): summary, license and credit, columns (Markdown). Web page: https://huggingface.co/datasets/hotchpotch/japan-weather
- [docs/dataset.md](https://raw.githubusercontent.com/hotchpotch/japan-weather-dataset/main/docs/dataset.md): the dataset definition: elements, keys and time, homogeneity, files, update strategy.
- [docs/local-copy.md](https://raw.githubusercontent.com/hotchpotch/japan-weather-dataset/main/docs/local-copy.md): how fresh the Hub is, keeping a local copy up to date, fetching days newer than the Hub.

## Optional

- [docs/source.md](https://raw.githubusercontent.com/hotchpotch/japan-weather-dataset/main/docs/source.md): how JMA's obsdl service works and its limits.
- [docs/operations.md](https://raw.githubusercontent.com/hotchpotch/japan-weather-dataset/main/docs/operations.md): the daily and monthly updates, JMA's preliminary and final values.
- [docs/crawler.md](https://raw.githubusercontent.com/hotchpotch/japan-weather-dataset/main/docs/crawler.md): the `jwd` crawler (Go), building and running it.
- Example analyses made with this dataset (Japanese pages, each with its method and limits at the end):
  - [日本は本当に温暖化しているのか](https://hotchpotch.github.io/japan-weather-dataset/analyses/warming/): warming at 15 low-urbanization stations since 1898 and at 805 stations since 1980.
  - [酷暑日は増えたのか](https://hotchpotch.github.io/japan-weather-dataset/analyses/kokusho/): days of 40℃ or more since 1927, with the AMeDAS 2003 step corrected.
  - [雪は、どこで減ったのか](https://hotchpotch.github.io/japan-weather-dataset/analyses/snow/): the winter maximum snow depth by midwinter temperature.
  - [避暑地は、まだ避暑地なのか](https://hotchpotch.github.io/japan-weather-dataset/analyses/hisho/): hot days and warm nights at 18 summer resorts, 1976–95 against 2016–25.
  - [晴れなかった9月](https://hotchpotch.github.io/japan-weather-dataset/analyses/sept/): sunshine and precipitation of September 2026 in Kantō since 1961.
