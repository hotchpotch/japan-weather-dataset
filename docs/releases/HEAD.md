# HEAD

## Operation

- `daily.yml` and `monthly.yml` report their start and result to
  Healthchecks.io (secrets `HEALTHCHECKS_DAILY_URL` and
  `HEALTHCHECKS_MONTHLY_URL`), which alerts when a run failed or did not
  happen (docs/operations.md, "Monitoring").

## Dataset

- The dataset card is shorter: a summary, links to docs/dataset.md, the
  entry point for AI agents (`docs/usage.md`) and the Japanese
  introduction page, one DuckDB example, the credit and license, and the
  column tables folded in a `<details>` block. The quality flags table
  moved to docs/dataset.md.

## Documents

- README is shorter; the crawler reference moved to docs/crawler.md.
- The entry point for AI agents is `docs/usage.md` (raw URL on `main`)
  instead of `llms.txt` on GitHub Pages.
- `site/` has the Japanese introduction page and the example analyses,
  deployed to GitHub Pages by `.github/workflows/pages.yml`.
- README, the dataset card, the agent skill and docs/usage.md ask to use
  the data under PDL 1.0 or CC BY 4.0 (linked) instead of prescribing who
  to name in the credit, and the README states the dataset license.
