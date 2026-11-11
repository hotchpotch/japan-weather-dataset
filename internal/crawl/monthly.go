package crawl

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/hotchpotch/japan-weather-dataset/internal/export"
	"github.com/hotchpotch/japan-weather-dataset/internal/obsdl"
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
	DryRun       bool
}

// monthlyFrom is the first day refetched by the monthly run: the first day
// of the month before the month before the month of yesterday. On 12-02
// (yesterday 12-01) it is 10-01: October and November are fetched again
// after they ended, and October goes into the year file at export.
func monthlyFrom(yesterday time.Time) time.Time {
	return time.Date(yesterday.Year(), yesterday.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, -2, 0)
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
//  4. export into o.Dataset: the oldest month file goes into its year
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
