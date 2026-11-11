// Command jwd collects JMA weather observations into a Parquet dataset.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/hotchpotch/japan-weather-dataset/internal/crawl"
	"github.com/hotchpotch/japan-weather-dataset/internal/export"
	"github.com/hotchpotch/japan-weather-dataset/internal/obsdl"
	"github.com/hotchpotch/japan-weather-dataset/internal/store"
)

// version is set from the release tag by the Docker build (-ldflags -X).
var version = "0.1.3"

const usage = `jwd - build a Parquet dataset of JMA weather observations

Usage:
  jwd [global flags] <command> [flags]

Commands:
  stations   download the station lists of all areas
  probe      find the years with data of each station
  backfill   fetch all station-years not fetched yet (newest first)
  update     fetch the latest days and refresh old chunks
  monthly    monthly run on a dataset directory without the crawl state:
             stations, the last two months again, the oldest month file
             into its year file (see docs/operations.md)
  export     write the dataset (Parquet + dataset card) to a directory
  month      update the current month files of a dataset directory
             without the crawl state (e.g. on GitHub Actions)
  status     show crawl progress
  version    print the version

Global flags:
`

type global struct {
	dataDir     string
	interval    time.Duration
	minInterval time.Duration
	userAgent   string
	budget      int
	maxStations int
	maxRetries  int
	maxPerDay   int
	maxRateHits int
	keepRaw     bool
	verbose     bool
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func main() {
	var g global
	fs := flag.NewFlagSet("jwd", flag.ExitOnError)
	fs.StringVar(&g.dataDir, "data-dir", env("JWD_DATA_DIR", "data"), "data directory (env JWD_DATA_DIR)")
	fs.DurationVar(&g.interval, "interval", 6*time.Second, "initial pause between the end of a response and the next request to JMA; doubled on rate limiting, lowered by 0.5s after 50 successes in a row")
	fs.DurationVar(&g.minInterval, "min-interval", 3*time.Second, "lower bound of the adaptive interval (set to -interval or more for a fixed interval)")
	fs.StringVar(&g.userAgent, "user-agent", env("JWD_USER_AGENT",
		"japan-weather-dataset/"+version+" (+https://github.com/hotchpotch/japan-weather-dataset)"), "User-Agent header (env JWD_USER_AGENT)")
	fs.IntVar(&g.budget, "budget", 100_000, fmt.Sprintf("maximum request size in cells (server limit %d)", obsdl.ServerCellLimit))
	fs.IntVar(&g.maxStations, "max-stations", 50, "maximum stations per request")
	fs.IntVar(&g.maxRetries, "max-retries", 5, "retries of a failed request (exponential backoff from 30s)")
	fs.IntVar(&g.maxPerDay, "max-requests-per-24h", 0, "stop when this many requests were sent in the last 24 hours (0: no limit)")
	fs.IntVar(&g.maxRateHits, "max-rate-limit-hits", 6, "consecutive rate-limited responses (429/503) before stopping; waits 1m, 2m, ... up to 30m in between")
	fs.BoolVar(&g.keepRaw, "keep-raw", false, "keep raw CSV responses under <data-dir>/raw")
	fs.BoolVar(&g.verbose, "v", false, "debug logging")
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, usage)
		fs.PrintDefaults()
	}
	fs.Parse(os.Args[1:])
	if fs.NArg() == 0 {
		fs.Usage()
		os.Exit(2)
	}
	if fs.Arg(0) == "version" {
		fmt.Println(version)
		return
	}
	level := slog.LevelInfo
	if g.verbose {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := runCommand(ctx, g, log, fs.Arg(0), fs.Args()[1:]); err != nil {
		if errors.Is(err, context.Canceled) {
			log.Warn("interrupted")
			os.Exit(130)
		}
		code := exitCode(err)
		log.Error("failed", "err", err, "exit_code", code)
		os.Exit(code)
	}
}

// Exit codes tell schedulers why a run stopped.
const (
	exitRateLimited = 3 // JMA kept answering "too many requests"
	exitBlocked     = 4 // JMA refused access
	exitMaintenance = 5 // the service is under maintenance
	exitQuota       = 6 // the self-imposed request limit was reached
	exitMissingDays = 7 // month: days are missing beyond -max-gap-days
)

func exitCode(err error) int {
	switch {
	case errors.Is(err, obsdl.ErrBlocked):
		return exitBlocked
	case errors.Is(err, obsdl.ErrRateLimited):
		return exitRateLimited
	case errors.Is(err, obsdl.ErrMaintenance):
		return exitMaintenance
	case errors.Is(err, crawl.ErrQuota):
		return exitQuota
	case errors.Is(err, crawl.ErrMissingDays):
		return exitMissingDays
	}
	return 1
}

// accessState maps the result of a run to the state of access_status.json.
func accessState(err error) store.AccessState {
	switch {
	case errors.Is(err, obsdl.ErrBlocked):
		return store.AccessBlocked
	case errors.Is(err, obsdl.ErrRateLimited):
		return store.AccessRateLimited
	case errors.Is(err, obsdl.ErrMaintenance):
		return store.AccessMaintenance
	case errors.Is(err, crawl.ErrQuota):
		return store.AccessQuota
	}
	return store.AccessOK
}

func runCommand(ctx context.Context, g global, log *slog.Logger, cmd string, args []string) (err error) {
	dataDir := g.dataDir
	if cmd == "month" || cmd == "monthly" {
		// month and monthly work from the dataset files only; their state
		// is temporary.
		tmp, err := os.MkdirTemp("", "jwd-"+cmd+"-")
		if err != nil {
			return err
		}
		defer os.RemoveAll(tmp)
		dataDir = tmp
	}
	st, err := store.Open(dataDir)
	if err != nil {
		return err
	}
	defer st.Close()
	if g.budget > obsdl.ServerCellLimit {
		return fmt.Errorf("--budget must not exceed %d", obsdl.ServerCellLimit)
	}
	var client *obsdl.Client
	var lastEvent *obsdl.AccessEvent
	limits := map[obsdl.AccessEventKind]int{}
	client = obsdl.NewClient(obsdl.Options{
		UserAgent:        g.userAgent,
		Interval:         g.interval,
		MinInterval:      g.minInterval,
		MaxRetries:       g.maxRetries,
		MaxRateLimitHits: g.maxRateHits,
		Logger:           log,
		OnAccessEvent: func(ev obsdl.AccessEvent) {
			lastEvent = &ev
			if ev.Kind != obsdl.EventRecovered && ev.Kind != obsdl.EventSpedUp {
				limits[ev.Kind]++
			}
			if err := st.RecordAccessEvent(context.WithoutCancel(ctx), ev); err != nil {
				log.Warn("record access event", "err", err)
			}
			state := store.AccessRateLimited
			switch ev.Kind {
			case obsdl.EventRecovered, obsdl.EventSpedUp:
				state = store.AccessOK
			case obsdl.EventBlocked:
				state = store.AccessBlocked
			case obsdl.EventMaintenance:
				state = store.AccessMaintenance
			}
			writeStatus(st, log, state, cmd, client, lastEvent, "")
		},
	})
	if prev, _ := st.ReadAccessStatus(); prev != nil {
		restoreInterval(client, prev)
	}
	c := &crawl.Crawler{
		Client:            client,
		Store:             st,
		Log:               log,
		Budget:            g.budget,
		MaxStations:       g.maxStations,
		KeepRaw:           g.keepRaw,
		MaxRequestsPer24h: g.maxPerDay,
	}
	switch cmd {
	case "stations", "probe", "backfill", "update", "monthly":
		defer func() {
			if errors.Is(err, context.Canceled) {
				return
			}
			state := accessState(err)
			msg := ""
			if state != store.AccessOK {
				msg = err.Error()
			} else if err != nil {
				// Other failures do not change the access state.
				if prev, _ := st.ReadAccessStatus(); prev != nil {
					state = prev.State
				}
			}
			writeStatus(st, log, state, cmd, client, lastEvent, msg)
		}()
	}
	// The dataset directory and -dry-run of month and monthly, for the job
	// summary.
	var dataset *string
	var dryRun *bool
	if cmd == "month" || cmd == "monthly" {
		start := time.Now()
		defer func() {
			s := runSummary{cmd: cmd, err: err, dryRun: dryRun != nil && *dryRun, elapsed: time.Since(start),
				stats: c.Stats(), events: limits}
			if dataset != nil && !s.dryRun {
				s.manifest, _ = export.ReadManifest(*dataset)
			}
			if err := appendSummary(s); err != nil {
				log.Warn("job summary", "err", err)
			}
		}()
	}
	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	switch cmd {
	case "stations":
		fs.Parse(args)
		sts, err := c.RefreshStations(ctx)
		if err != nil {
			return err
		}
		log.Info("saved stations", "count", len(sts))
		return nil

	case "probe":
		ids := fs.String("stations", "", "comma separated station ids (default: all)")
		all := fs.Bool("all", false, "probe stations that already have coverage again")
		fs.Parse(args)
		sts, err := st.Stations(ctx)
		if err != nil {
			return err
		}
		return c.Probe(ctx, filterStations(sts, *ids), !*all)

	case "backfill":
		freq := fs.String("frequency", "daily", "daily or hourly")
		ids := fs.String("stations", "", "comma separated station ids (default: all)")
		fromYear := fs.Int("from-year", 0, "first year (default: from the probe)")
		toYear := fs.Int("to-year", 0, "last year (default: from the probe)")
		maxJobs := fs.Int("max-jobs", 0, "stop after this many requests (0: no limit)")
		dryRun := fs.Bool("dry-run", false, "print the plan without requesting")
		fs.Parse(args)
		f, err := obsdl.ParseFrequency(*freq)
		if err != nil {
			return err
		}
		o := crawl.BackfillOptions{Frequency: f, FromYear: *fromYear, ToYear: *toYear, MaxJobs: *maxJobs, DryRun: *dryRun}
		if *ids != "" {
			o.Stations = map[string]bool{}
			for _, id := range strings.Split(*ids, ",") {
				o.Stations[strings.TrimSpace(id)] = true
			}
		}
		return c.Backfill(ctx, o)

	case "update":
		lookback := fs.Int("lookback-days", 31, "days up to yesterday fetched on every run")
		refresh := fs.Int("refresh-jobs", 0, "extra requests that refetch the oldest chunks")
		refreshAge := fs.Duration("refresh-age", 30*24*time.Hour, "minimum age of a chunk to refresh")
		dryRun := fs.Bool("dry-run", false, "print the plan without requesting")
		fs.Parse(args)
		if *lookback < 1 {
			return fmt.Errorf("--lookback-days must be at least 1")
		}
		return c.Update(ctx, crawl.UpdateOptions{LookbackDays: *lookback, RefreshJobs: *refresh, RefreshAge: *refreshAge, DryRun: *dryRun})

	case "monthly":
		dataset = fs.String("dataset", env("JWD_EXPORT_DIR", "dataset"), "dataset directory with stations, homogeneity breaks, manifest.json, the month files and the year files of the recent years (env JWD_EXPORT_DIR)")
		skipStations := fs.Bool("skip-stations", false, "do not download the station list")
		dryRun = fs.Bool("dry-run", false, "print the plan without requesting or writing")
		fs.Parse(args)
		return c.Monthly(ctx, crawl.MonthlyOptions{Dataset: *dataset, SkipStations: *skipStations, DryRun: *dryRun})

	case "export":
		out := fs.String("out", env("JWD_EXPORT_DIR", "dataset"), "output directory (env JWD_EXPORT_DIR)")
		all := fs.Bool("all", false, "re-export every year, not only changed years")
		fs.Parse(args)
		return (&export.Exporter{Store: st, Out: *out, Log: log, All: *all}).Run(ctx)

	case "month":
		dataset = fs.String("dataset", env("JWD_EXPORT_DIR", "dataset"), "dataset directory with stations/stations.parquet and the current month files (env JWD_EXPORT_DIR)")
		freqs := fs.String("frequency", "daily,hourly", "comma separated frequencies")
		refetch := fs.Int("refetch-days", 3, "last days up to yesterday fetched on every run")
		maxGap := fs.Int("max-gap-days", 3, "missing days before them filled at most (more: exit code 7)")
		dryRun = fs.Bool("dry-run", false, "print the plan without requesting or writing")
		fs.Parse(args)
		o := crawl.MonthOptions{Dataset: *dataset, RefetchDays: *refetch, MaxGapDays: *maxGap, DryRun: *dryRun}
		for _, name := range strings.Split(*freqs, ",") {
			f, err := obsdl.ParseFrequency(strings.TrimSpace(name))
			if err != nil {
				return err
			}
			o.Frequencies = append(o.Frequencies, f)
		}
		return c.UpdateMonth(ctx, o)

	case "status":
		fs.Parse(args)
		return status(ctx, st)
	}
	return fmt.Errorf("unknown command %q", cmd)
}

// restoreInterval starts from the interval and the last rate limit of the
// previous run when it ended less than 24 hours ago, so that a restart does
// not go back to a faster pace than the server accepted.
func restoreInterval(client *obsdl.Client, prev *store.AccessStatus) {
	if time.Since(prev.UpdatedAt) > 24*time.Hour {
		return
	}
	iv, _ := time.ParseDuration(prev.Interval)
	hitIv, _ := time.ParseDuration(prev.LastRateLimitInterval)
	client.RestoreRateLimit(iv, hitIv, prev.LastRateLimitAt)
}

func writeStatus(st *store.Store, log *slog.Logger, state store.AccessState, cmd string, client *obsdl.Client, ev *obsdl.AccessEvent, msg string) {
	s := store.AccessStatus{State: state, Command: cmd, Interval: client.Interval().String(), Message: msg}
	if iv, at := client.LastRateLimit(); !at.IsZero() {
		s.LastRateLimitInterval, s.LastRateLimitAt = iv.String(), at
	} else if prev, _ := st.ReadAccessStatus(); prev != nil {
		s.LastRateLimitInterval, s.LastRateLimitAt = prev.LastRateLimitInterval, prev.LastRateLimitAt
	}
	if ev != nil {
		s.LastEvent = store.NewEventJSON(*ev)
	}
	if err := st.WriteAccessStatus(s); err != nil {
		log.Warn("write access status", "err", err)
	}
}

func filterStations(sts []*store.StationRow, ids string) []*store.StationRow {
	if ids == "" {
		return sts
	}
	want := map[string]bool{}
	for _, id := range strings.Split(ids, ",") {
		want[strings.TrimSpace(id)] = true
	}
	var out []*store.StationRow
	for _, s := range sts {
		if want[s.ID] {
			out = append(out, s)
		}
	}
	return out
}

func status(ctx context.Context, st *store.Store) error {
	if a, err := st.ReadAccessStatus(); err == nil && a != nil {
		fmt.Printf("== access\nstate=%s since=%s updated=%s command=%s interval=%s\n", a.State,
			a.Since.Format("2006-01-02 15:04"), a.UpdatedAt.Format("2006-01-02 15:04"), a.Command, a.Interval)
		if a.Message != "" {
			fmt.Println("message:", a.Message)
		}
	}
	queries := []struct{ title, sql string }{
		{"stations", `SELECT station_type, count(*) AS stations, count(*) FILTER (WHERE ended_on IS NULL) AS active FROM stations GROUP BY ALL ORDER BY 1`},
		{"coverage", `SELECT frequency, count(*) AS probed, count(first_year) AS with_data, min(first_year) AS first, max(last_year) AS last FROM coverage GROUP BY ALL ORDER BY 1`},
		{"chunks", `SELECT frequency, count(*) AS chunks, sum(rows) AS rows, min(year) AS first, max(year) AS last, max(covered_to) AS latest FROM chunks GROUP BY ALL ORDER BY 1`},
		{"requests", `SELECT purpose, status, count(*) AS requests, sum(cells) AS cells, round(avg(duration_ms)) AS avg_ms, max(started_at) AS last FROM requests GROUP BY ALL ORDER BY 1, 2`},
		{"requests in the last 24 hours", `SELECT count(*) AS requests FROM requests WHERE started_at >= (now() AT TIME ZONE 'UTC') - INTERVAL 24 HOUR`},
		{"access events (last 7 days)", `SELECT kind, count(*) AS events, max(http_status) AS http_status, max(time) AS last, max(interval_ms) AS max_interval_ms FROM access_events WHERE time >= (now() AT TIME ZONE 'UTC') - INTERVAL 7 DAY GROUP BY ALL ORDER BY last DESC`},
	}
	for _, q := range queries {
		rows, err := st.DB.QueryContext(ctx, q.sql)
		if err != nil {
			return err
		}
		cols, _ := rows.Columns()
		fmt.Printf("== %s\n%s\n", q.title, strings.Join(cols, "\t"))
		for rows.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				rows.Close()
				return err
			}
			parts := make([]string, len(vals))
			for i, v := range vals {
				switch t := v.(type) {
				case time.Time:
					parts[i] = t.Format("2006-01-02 15:04")
				case nil:
					parts[i] = "-"
				default:
					parts[i] = fmt.Sprint(t)
				}
			}
			fmt.Println(strings.Join(parts, "\t"))
		}
		rows.Close()
	}
	return nil
}
