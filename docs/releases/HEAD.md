# HEAD

## Operation

- A block by JMA (exit code 4) in the daily or monthly run disables both
  workflows, so that no scheduled run sends requests until the maintainer
  enables them again. keepalive no longer enables workflows that were
  disabled by hand or after a block, only active ones and those GitHub
  disabled for inactivity.

- `sync.sh` of the agent skill reads `manifest.json` with the Python of uv
  when `python3` is missing, and downloads the metadata and the data of
  one Hub revision, so that a monthly update between its downloads cannot
  leave a month twice in the copy.

- Dependabot now opens a monthly pull request with version updates of the
  actions (grouped, 7-day cooldown, lockfile regenerated), not only
  security updates. Go modules stay on security updates only.

## Dataset

- The monthly run stops with exit code 8 before the export, and uploads
  nothing, when the refetch removed many rows of an ended month (at least
  two days and more than a tenth of a station's rows). An incomplete answer
  from JMA would otherwise be settled in a year file that is not fetched
  again. `jwd monthly -allow-row-loss` exports anyway.

- The dataset card no longer names the latest day, the generation time or
  the last year, which went stale between the monthly runs that rewrite
  it; it gives the first years and points to `latest_date` in
  `manifest.json`. The usage examples name the repository
  (`hotchpotch/japan-weather`) instead of `<repo>`.
