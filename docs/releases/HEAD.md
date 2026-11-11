# HEAD

- Progress logs: `job progress=i/N` also shows the elapsed time and an
  estimate of the time left (`elapsed=… eta=…`), and a `finished jobs` line
  gives the requests, rows and warnings of the run.
- `jwd month` and `jwd monthly` append a summary of the run (result, time,
  requests, access limits, the month files in `manifest.json`) to
  `GITHUB_STEP_SUMMARY` when it is set; `jwd-daily.sh` and
  `jwd-monthly.sh` add the commit of the upload. The workflows mount the
  job summary into the container, so it shows on the run's page.
