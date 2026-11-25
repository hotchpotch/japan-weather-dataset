# site

The introduction page of the project (Japanese only), meant for GitHub
Pages at <https://hotchpotch.github.io/japan-weather-dataset/>. Pages is
not enabled yet.

- `index.html`: the page. The prompt example and the links for AI agents
  point to the raw URL of [docs/usage.md](../docs/usage.md) on `main`,
  so they work only once the repository is public. Self-contained: inline
  CSS and JS (the question demo, the station map, the document viewer, the
  copy buttons), inline SVG icons ([Lucide](https://lucide.dev), ISC
  License; the Hugging Face logo), system fonts, no external scripts. Light and dark follow the
  system setting; animations respect `prefers-reduced-motion`. The station
  coordinates and the warming stripes (yearly anomalies of the 15-station
  mean, 1898–2025) are inlined as JSON.
- `analyses/<name>/index.html`: the example analyses made with the dataset
  (`warming`, `kokusho`, `snow`, `hisho`, `sept`). Each is a single page
  with inline data and scripts; it loads only Google Fonts. A thin bar at
  the top links back to the introduction page. Each page states the
  source credit (who processed the data) and its method and limits.
- `images/`: the thumbnails of the analyses (1200×750, WebP, also their
  `og:image`) and the Open Graph image of the page `og.png` (1200×630).
- `favicon.svg`: the icon (warming stripes).
- `.nojekyll`: serve the files as they are.

Keep the numbers on the page (stations, rows, sizes, periods) in sync with
README, docs/dataset.md and the dataset card when they change much. The
analyses are snapshots of the data at the time they were made; do not
update their numbers silently.

To check the pages locally, open `site/index.html` in a browser (no server
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
