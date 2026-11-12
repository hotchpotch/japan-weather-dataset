# HEAD

## Operation

- New workflow `keepalive.yml` enables the scheduled workflows again twice
  a month, so that GitHub does not disable them after 60 days without
  activity in the public repository.
- The daily workflow is renamed from `month.yml` to `daily.yml`.
- The monthly run moves from the 2nd, 06:17 JST to the 1st, 20:00 JST, far
  from the daily run. The recent months (month files) now count from the
  day after the latest date, so the run on the 1st refetches the same two
  months (the month before last and the last month) and merges the month
  before last into its year file, as the run on the 2nd did. Also affects
  `jwd export` when the latest date is the end of a month.
