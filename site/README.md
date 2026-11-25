# site

The introduction page of the project (Japanese only), meant for GitHub
Pages at <https://hotchpotch.github.io/japan-weather-dataset/>. Pages is
not enabled yet.

- `index.html`: the page. Self-contained: inline CSS, a few lines of JS
  for the copy button, system fonts, no external scripts or fonts. Light
  and dark follow the system setting.
- `llms.txt`: the entry point for AI agents ([llms.txt](https://llmstxt.org/)
  format). It links to the agent skill, the dataset card and docs/ by
  absolute URL (raw.githubusercontent.com), so it works only once the
  repository is public. The example prompt on the page uses its URL.
- `images/`: screenshots of the example analyses (1200×750, WebP) and the
  Open Graph image `og.png` (1200×630).
- `.nojekyll`: serve the files as they are.

Keep the numbers on the page (stations, rows, sizes, periods) in sync with
README, docs/dataset.md and the dataset card when they change much. The
heading stripes are the yearly anomalies of the 15-station mean
(1898–2025) as static SVG rectangles.

To check the page locally, open `site/index.html` in a browser (no server
needed).

## Publishing (after the repository is public)

Pages can deploy only `/` or `/docs` from a branch, so the `site/`
directory needs a workflow: Settings → Pages → Source: GitHub Actions, then
a workflow on `push` to `main` (paths `site/**`) that runs
`actions/configure-pages`, `actions/upload-pages-artifact` with
`path: site` and `actions/deploy-pages`, with `permissions: pages: write,
id-token: write` and the `github-pages` environment. Pin the actions by
release tag and update `.github/workflows/actions.lock` as for the other
workflows.
