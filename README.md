# japan-weather-dataset

Daily (from 1872) and hourly (from 1885) observations of all JMA (Japan
Meteorological Agency) stations — about 1,700 official and AMeDAS
stations — as a Parquet dataset on the Hugging Face Hub, updated every
day, with an agent skill so that AI agents can analyze it.

- Dataset: [hotchpotch/japan-weather](https://huggingface.co/datasets/hotchpotch/japan-weather)
- For AI agents: [llms.txt](https://hotchpotch.github.io/japan-weather-dataset/llms.txt)

日本語の紹介ページ：[https://hotchpotch.github.io/japan-weather-dataset/](https://hotchpotch.github.io/japan-weather-dataset/)（分析例もあります）

## Use the dataset

Download the parts you need and query them with DuckDB:

```sh
.agents/skills/japan-weather/scripts/sync.sh daily   # ~215 MB into ~/.cache/japan-weather/data
duckdb -c "SELECT date, temperature_max FROM read_parquet('~/.cache/japan-weather/data/daily/*/*.parquet', hive_partitioning = false)
           WHERE station_id = 's47662' AND date >= DATE '2025-08-01' ORDER BY date LIMIT 5"
```

`sync.sh` also takes ranges such as `daily:2020-2026` or `hourly:2025`.
[docs/local-copy.md](docs/local-copy.md) covers keeping a copy up to date.

## Use it with AI agents

The [`japan-weather`](.agents/skills/japan-weather/SKILL.md) skill
([Agent Skills](https://agentskills.io/specification) format) teaches
coding agents the dataset: downloading only what is needed, the columns,
SQL recipes and the pitfalls of long-term comparisons.

- In this repository, it is already available to Codex (`.agents/skills/`)
  and Claude Code (`.claude/skills/`).
- In another project:

  ```sh
  npx skills add hotchpotch/japan-weather-dataset --skill japan-weather
  ```

- Without installing anything, give the agent the
  [llms.txt](https://hotchpotch.github.io/japan-weather-dataset/llms.txt)
  URL with your question.

## Build the dataset yourself

The `jwd` crawler (Go) collects the data from JMA's
「過去の気象データ・ダウンロード」, politely (one request at a time, an
adaptive pause, stopping on rate limits or blocks), and exports the
Parquet files. The daily and monthly updates run on GitHub Actions.

```sh
go build -o bin/jwd ./cmd/jwd
```

- [docs/crawler.md](docs/crawler.md): commands, flags, exit codes, running the updates.
- [docs/source.md](docs/source.md): how the source service works.
- [docs/dataset.md](docs/dataset.md): layout, columns, update strategy.
- [docs/operations.md](docs/operations.md): the daily and monthly updates.
- [docs/release.md](docs/release.md): releases and the image.

## License

- Data: from the JMA website under the 公共データ利用規約（第1.0版）
  (PDL 1.0, compatible with CC BY 4.0). When publishing results, credit
  the source and who processed it, and do not present them as made by JMA
  or the government, for example:

  > 出典：気象庁ホームページ「過去の気象データ・ダウンロード」（https://www.data.jma.go.jp/risk/obsdl/）をもとに`{加工者の名前}`が加工して作成

  The dataset on the Hub is processed by Yuichi Tateno (@hotchpotch).
- Code: MIT. Test fixtures under `internal/obsdl/testdata` are small
  excerpts of JMA data (PDL 1.0).

This project is not affiliated with JMA.
