# japan-weather-dataset

Daily (from 1872) and hourly (from 1885) observations of all JMA (Japan
Meteorological Agency) stations — about 1,700 official and AMeDAS
stations — as a Parquet dataset on the Hugging Face Hub, updated every
day, with an agent skill so that AI agents can analyze it.

> [!TIP]
> 日本語の紹介ページと分析例：<https://hotchpotch.github.io/japan-weather-dataset/>

- Dataset: [hotchpotch/japan-weather](https://huggingface.co/datasets/hotchpotch/japan-weather)
- For AI agents: [docs/usage.md](docs/usage.md)

## Use the dataset

### 1. Give the URL to an AI agent (recommended)

Put the raw URL of [docs/usage.md](docs/usage.md), the entry point for AI
agents, into the prompt with your question. Nothing to install: agents
that can run commands, such as Claude Code or Codex, read the guide,
download the parts they need and analyze them.

```text
Has the number of days of 35°C or more increased in Japan? Analyze the
observation records. Use this data:
https://raw.githubusercontent.com/hotchpotch/japan-weather-dataset/main/docs/usage.md
```

In Japanese:

```text
猛暑日（最高気温35℃以上の日）は日本で増えている？過去の観測データから分析して。
次のデータを使うこと：
https://raw.githubusercontent.com/hotchpotch/japan-weather-dataset/main/docs/usage.md
```

### 2. Install the agent skill

The [`japan-weather`](.agents/skills/japan-weather/SKILL.md) skill
([Agent Skills](https://agentskills.io/specification) format) teaches
coding agents the dataset: downloading only what is needed, the columns,
SQL recipes and the pitfalls of long-term comparisons. Once installed in a
project, it is used for questions about the weather.

```sh
npx skills add hotchpotch/japan-weather-dataset --skill japan-weather
```

In this repository, it is already available to Codex (`.agents/skills/`)
and Claude Code (`.claude/skills/`).

### 3. Query it with SQL

Download only the parts you need from
[Hugging Face](https://huggingface.co/datasets/hotchpotch/japan-weather)
and read them with DuckDB, pandas or Polars. The example is the daily
maximum temperature in Tokyo (`s47662`):

```sh
.agents/skills/japan-weather/scripts/sync.sh daily   # ~215 MB into ~/.cache/japan-weather/data
duckdb -c "SELECT date, temperature_max FROM read_parquet('~/.cache/japan-weather/data/daily/*/*.parquet', hive_partitioning = false)
           WHERE station_id = 's47662' ORDER BY date DESC LIMIT 7"
```

`sync.sh` also takes ranges such as `daily:2020-2026` or `hourly:2025`.
[docs/local-copy.md](docs/local-copy.md) covers keeping a copy up to date.

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

> [!NOTE]
> This project is not affiliated with JMA or the Government of Japan.
