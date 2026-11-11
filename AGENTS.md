# Repository guidelines

## Project

`jwd` crawls JMA's 「過去の気象データ・ダウンロード」 (obsdl) and builds a
Parquet dataset of daily and hourly observations, published on the
Hugging Face Hub. Read before changing things:

- [docs/source.md](docs/source.md): how the source service works and its
  limits.
- [docs/dataset.md](docs/dataset.md): layout, columns, update strategy,
  access restrictions.
- [docs/operations.md](docs/operations.md): the daily (GitHub Actions) and
  monthly (crawl machine) updates.
- [docs/release.md](docs/release.md): releases and the image.

## Requests to JMA

- Be polite to the source: one request at a time, the adaptive pause, no
  parallel crawlers. Never work around a rate limit, block or maintenance
  (exit codes 3, 4, 5); stop and report instead.
- Tests use fakes (`fetchFunc`, fake tables) and send no requests. Run real
  commands against JMA only when that is the task, with `-dry-run` first
  where it exists.

## Commands

```sh
go build -o bin/jwd ./cmd/jwd
gofmt -l cmd internal          # no output
go vet ./...
go test ./...
go test -race ./...
for f in scripts/*.sh; do bash -n "$f"; done
```

Run the checks above before committing a behavior change. CI
(`.github/workflows/ci.yml`) runs them and builds the image.

## Changes

- Add or update focused tests with each behavior change (`internal/...`).
- Keep the documents in sync with the code: README, docs/, the dataset card
  template (`internal/export/card.md.tmpl`).
- Add a short entry to `docs/releases/HEAD.md` when a change affects the
  dataset, the updates or the operation.
- Workflows: reference actions by an exact release tag (`@v7.0.1`), then
  run `gh actions-lock` and commit `.github/workflows/actions.lock`, which
  pins each action and the actions it uses to a commit (GitHub enforces it
  at run time). Never use `latest` images or unpinned tools in the update
  path.

## Data and secrets

- The crawl state (`tmp/data` or `JWD_DATA_DIR`) and exported datasets are
  never committed. `.env` holds local settings and tokens and is ignored.
- Do not upload to the Hugging Face Hub, push, tag or release without the
  maintainer's go-ahead.
- Do not print or commit tokens (`HF_TOKEN`, GitHub tokens).

## Git

- Inspect the working tree before staging; do not revert unrelated changes.
- Concise English commit messages that describe the change and why.
