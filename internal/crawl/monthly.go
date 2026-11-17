package crawl

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/hotchpotch/japan-weather-dataset/internal/export"
	"github.com/hotchpotch/japan-weather-dataset/internal/obsdl"
	"github.com/hotchpotch/japan-weather-dataset/internal/store"
)

// MonthlyOptions configures Monthly.
type MonthlyOptions struct {
	// Dataset is a dataset directory with stations/stations.parquet,
	// homogeneity_breaks/homogeneity_breaks.parquet, manifest.json, all
	// month files and the year files of the years from monthlyFrom to
	// yesterday (see MonthlyFiles); typically downloaded from the Hub.
	Dataset string
	// SkipStations skips downloading the station list (stations without
	// coverage are still probed).
	SkipStations bool
	// AllowRowLoss exports even when the refetch removed many rows of an
	// ended month (see checkRowLoss), e.g. after JMA really withdrew data.
	AllowRowLoss bool
	DryRun       bool
}

// ErrRowLoss means the refetch removed many rows of a month that the export
// would settle; nothing is written.
var ErrRowLoss = errors.New("the refetch removed rows of ended months")

// A station-month counts as lost when the refetch removed at least these
// rows (two days) and more than a tenth of them. Smaller changes are
// ordinary revisions (e.g. a missing latest day of one station).
const lossFraction = 0.1

func minLoss(f obsdl.Frequency) int {
	if f == obsdl.Hourly {
		return 48
	}
	return 2
}

// checkRowLoss compares the rows per station and month before and after
// the refetch. The refetch replaces the stored rows of its range, so an
// incomplete answer from the source would remove rows; the export would
// then settle the month in its year file, which is not fetched again.
func checkRowLoss(f obsdl.Frequency, before, after map[store.StationMonth]int) []string {
	var lost []string
	for k, n := range before {
		m := after[k]
		if d := n - m; d >= minLoss(f) && float64(d) > lossFraction*float64(n) {
			lost = append(lost, fmt.Sprintf("%s %s %s: %d -> %d rows", f, k.Station, k.Month.Format("2006-01"), n, m))
		}
	}
	sort.Strings(lost)
	return lost
}

// monthlyFrom is the first day refetched by the monthly run: the first day
// of the month two months before the month of today (the day after
// yesterday). On 12-01 (yesterday 11-30) and on 12-02 it is 10-01: October
// and November are fetched again after they ended, and October goes into
// the year file at export (export.RecentMonths counts from the same day).
func monthlyFrom(yesterday time.Time) time.Time {
	today := yesterday.AddDate(0, 0, 1)
	return time.Date(today.Year(), today.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, -2, 0)
}

// MonthlyYears returns the years whose files the monthly run reads and
// rewrites when yesterday is the given day: from the year of monthlyFrom to
// the year of yesterday.
func MonthlyYears(yesterday time.Time) []int {
	var out []int
	for y := monthlyFrom(yesterday).Year(); y <= yesterday.Year(); y++ {
		out = append(out, y)
	}
	return out
}

// Monthly is the monthly run, without the crawl state (like UpdateMonth):
//
//  1. load the stations, the homogeneity breaks and the files of
//     MonthlyYears from o.Dataset into c.Store (a fresh, temporary state
//     directory);
//  2. download the station list and probe new stations;
//  3. refetch everything from monthlyFrom to yesterday for the active
//     stations: the month before last and the last month, after they
//     ended, plus the current month (rows that did not change keep their
//     updated_at);
//  4. stop with ErrRowLoss when the refetch removed many rows of an ended
//     month (checkRowLoss), unless o.AllowRowLoss;
//  5. export into o.Dataset: the oldest month file goes into its year
//     file; stations, homogeneity breaks, the dataset card and
//     manifest.json are rewritten.
//
// Files of other years are neither needed nor touched. A failure before
// the export leaves o.Dataset as it was; the run can simply be repeated.
func (c *Crawler) Monthly(ctx context.Context, o MonthlyOptions) error {
	yesterday := c.Yesterday()
	from := monthlyFrom(yesterday)
	if err := c.loadDataset(ctx, o.Dataset, from, yesterday); err != nil {
		return err
	}
	// The ended months (before the month of today) are checked for lost rows.
	today := yesterday.AddDate(0, 0, 1)
	ended := time.Date(today.Year(), today.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, -1)
	before := map[obsdl.Frequency]map[store.StationMonth]int{}
	for _, f := range []obsdl.Frequency{obsdl.Daily, obsdl.Hourly} {
		n, err := c.Store.MonthRows(ctx, f, from, ended)
		if err != nil {
			return err
		}
		before[f] = n
	}
	if !o.SkipStations && !o.DryRun {
		if _, err := c.RefreshStations(ctx); err != nil {
			return err
		}
	}
	stations, err := c.Store.Stations(ctx)
	if err != nil {
		return err
	}
	active := activeStations(stations, from)
	var unprobed []string
	for _, st := range active {
		if st.Coverage[obsdl.Daily] == nil || st.Coverage[obsdl.Hourly] == nil {
			unprobed = append(unprobed, st.ID)
		}
	}
	if len(unprobed) > 0 {
		c.Log.Info("probing new stations", "stations", len(unprobed), "ids", unprobed)
		if !o.DryRun {
			if err := c.Probe(ctx, active, true); err != nil {
				return err
			}
			if active, err = c.reload(ctx, active); err != nil {
				return err
			}
		}
	}
	var jobs []job
	for _, f := range []obsdl.Frequency{obsdl.Daily, obsdl.Hourly} {
		jobs = append(jobs, c.pack("monthly", f, recentUnits(f, active, from), from, yesterday, withOverlap)...)
	}
	c.Log.Info("monthly plan", "from", from.Format(time.DateOnly), "to", yesterday.Format(time.DateOnly),
		"stations", len(active), "requests", len(jobs))
	if o.DryRun {
		return c.execute(ctx, jobs, 0, true)
	}
	if err := c.runJobs(ctx, jobs); err != nil {
		return err
	}
	var lost []string
	for _, f := range []obsdl.Frequency{obsdl.Daily, obsdl.Hourly} {
		after, err := c.Store.MonthRows(ctx, f, from, ended)
		if err != nil {
			return err
		}
		lost = append(lost, checkRowLoss(f, before[f], after)...)
	}
	if len(lost) > 0 {
		if !o.AllowRowLoss {
			return fmt.Errorf("%w (%d station-months; nothing exported; check the source, then rerun, or pass -allow-row-loss): %s",
				ErrRowLoss, len(lost), strings.Join(lost, ", "))
		}
		c.Log.Warn("refetch removed rows of ended months", "station_months", len(lost), "lost", lost)
	}
	return (&export.Exporter{Store: c.Store, Out: o.Dataset, Log: c.Log}).Run(ctx)
}

// loadDataset imports the stations, the homogeneity breaks and the year
// and month files of the years from the year of from to the year of to.
// A month file of an earlier year would be merged into a year file that
// was not loaded, so it is an error.
func (c *Crawler) loadDataset(ctx context.Context, dir string, from, to time.Time) error {
	stationsPath := filepath.Join(dir, "stations", "stations.parquet")
	if _, err := os.Stat(stationsPath); err != nil {
		return fmt.Errorf("the dataset directory needs stations/stations.parquet: %w", err)
	}
	if err := c.Store.ImportStations(ctx, stationsPath, c.now()); err != nil {
		return err
	}
	breaks := filepath.Join(dir, "homogeneity_breaks", "homogeneity_breaks.parquet")
	if _, err := os.Stat(breaks); err == nil {
		if err := c.Store.ImportBreaks(ctx, breaks); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	manifest, err := export.ReadManifest(dir)
	if err != nil {
		return err
	}
	for _, f := range []obsdl.Frequency{obsdl.Daily, obsdl.Hourly} {
		// The year files the manifest lists must be here: the export
		// rewrites them from what was loaded.
		if fm := manifestFor(manifest, f); fm != nil && fm.YearFilesThrough != "" {
			through, err := time.Parse(time.DateOnly, fm.YearFilesThrough)
			if err != nil {
				return fmt.Errorf("manifest.json: %w", err)
			}
			for y := from.Year(); y <= min(to.Year(), through.Year()); y++ {
				if _, err := os.Stat(export.YearPath(dir, f, y)); err != nil {
					return fmt.Errorf("the year file of %s %d is missing from %s (manifest.json lists year files through %s): %w",
						f, y, dir, fm.YearFilesThrough, err)
				}
			}
		}
		months, err := export.MonthFiles(dir, f)
		if err != nil {
			return err
		}
		for m, p := range months {
			if m.Year() < from.Year() {
				return fmt.Errorf("month file %s is older than the monthly run reads (from %d); export it from the full crawl state", p, from.Year())
			}
		}
		for y := from.Year(); y <= to.Year(); y++ {
			paths, err := export.YearFiles(dir, f, y)
			if err != nil {
				return err
			}
			n, err := c.Store.ImportRows(ctx, f, paths...)
			if err != nil {
				return err
			}
			c.Log.Info("loaded", "frequency", f, "year", y, "files", len(paths), "rows", n)
		}
	}
	return nil
}

func manifestFor(m *export.Manifest, f obsdl.Frequency) *export.FrequencyManifest {
	if m == nil {
		return nil
	}
	return m.For(f)
}
