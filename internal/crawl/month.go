package crawl

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/hotchpotch/japan-weather-dataset/internal/export"
	"github.com/hotchpotch/japan-weather-dataset/internal/obsdl"
	"github.com/hotchpotch/japan-weather-dataset/internal/store"
)

// MonthOptions configures UpdateMonth.
type MonthOptions struct {
	// Dataset is a dataset directory with stations/stations.parquet and,
	// if they exist, the month files of the current month (as written by
	// export); typically downloaded from the Hub.
	Dataset     string
	Frequencies []obsdl.Frequency
	// RefetchDays are the last days up to yesterday fetched on every run
	// (recent values are revised within days).
	RefetchDays int
	// MaxGapDays is the number of missing days before them that are
	// filled (the latest ones first). More missing days are left to the
	// monthly run and reported with ErrMissingDays.
	MaxGapDays int
	DryRun     bool
}

// ErrMissingDays means the month file still lacks days after the run.
var ErrMissingDays = errors.New("days missing from the month file")

// monthPlan returns the days to fetch for the month of yesterday as
// consecutive ranges: the last refetchDays days of the month up to
// yesterday, and the latest maxGapDays days before them without rows
// (have). missing are the days without rows that are not fetched.
func monthPlan(yesterday time.Time, have map[time.Time]bool, refetchDays, maxGapDays int) (ranges [][2]time.Time, missing []time.Time) {
	month := time.Date(yesterday.Year(), yesterday.Month(), 1, 0, 0, 0, 0, time.UTC)
	from := maxTime(month, yesterday.AddDate(0, 0, 1-refetchDays))
	var gaps []time.Time
	for d := month; d.Before(from); d = d.AddDate(0, 0, 1) {
		if !have[d] {
			gaps = append(gaps, d)
		}
	}
	fill := gaps
	if len(gaps) > maxGapDays {
		missing = gaps[:len(gaps)-maxGapDays]
		fill = gaps[len(gaps)-maxGapDays:]
	}
	days := append([]time.Time{}, fill...)
	for d := from; !d.After(yesterday); d = d.AddDate(0, 0, 1) {
		days = append(days, d)
	}
	for _, d := range days {
		if n := len(ranges); n > 0 && ranges[n-1][1].AddDate(0, 0, 1).Equal(d) {
			ranges[n-1][1] = d
			continue
		}
		ranges = append(ranges, [2]time.Time{d, d})
	}
	return ranges, missing
}

// activeStations returns the listed stations that had not ended before from.
func activeStations(stations []*store.StationRow, from time.Time) []*store.StationRow {
	var out []*store.StationRow
	for _, st := range stations {
		if !st.Listed || (st.EndedOn != nil && st.EndedOn.Before(from)) {
			continue
		}
		out = append(out, st)
	}
	return out
}

// recentUnits returns the units of the stations with data in the year
// before from or later (stations never probed are included).
func recentUnits(f obsdl.Frequency, stations []*store.StationRow, from time.Time) []unit {
	var units []unit
	for _, st := range stations {
		if cov := st.Coverage[f]; cov != nil && (cov.FirstYear == 0 || cov.LastYear < from.Year()-1) {
			continue // no recent data
		}
		units = append(units, unit{station: st.ID, elements: elementsFor(f, st)})
	}
	return units
}

// UpdateMonth brings the month files of the month of yesterday in
// o.Dataset up to date without the crawl state: the station list and the
// existing month files are loaded into c.Store (a fresh, temporary state
// directory), the planned days are fetched and merged as in update (rows
// that did not change keep their updated_at), and the month files and
// manifest.json are written back. Other files of the dataset are not
// touched; stations are not probed.
func (c *Crawler) UpdateMonth(ctx context.Context, o MonthOptions) error {
	if o.RefetchDays < 0 || o.MaxGapDays < 0 {
		return errors.New("refetch and gap days must not be negative")
	}
	prev, err := export.ReadManifest(o.Dataset)
	if err != nil {
		return err
	}
	stationsPath := filepath.Join(o.Dataset, "stations", "stations.parquet")
	if err := c.Store.ImportStations(ctx, stationsPath, c.now()); err != nil {
		return err
	}
	stations, err := c.Store.Stations(ctx)
	if err != nil {
		return err
	}
	yesterday := c.Yesterday()
	month := time.Date(yesterday.Year(), yesterday.Month(), 1, 0, 0, 0, 0, time.UTC)
	active := activeStations(stations, month)
	ex := &export.Exporter{Store: c.Store, Out: o.Dataset, Log: c.Log}
	var missing []string
	for _, f := range o.Frequencies {
		path := export.MonthPath(o.Dataset, f, month)
		existed := true
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			existed = false
		} else if err != nil {
			return err
		}
		if existed {
			n, err := c.Store.ImportRows(ctx, f, path)
			if err != nil {
				return err
			}
			c.Log.Info("loaded month file", "frequency", f, "path", path, "rows", n)
		}
		have, err := c.Store.Dates(ctx, f, month, yesterday)
		if err != nil {
			return err
		}
		units := recentUnits(f, active, month)
		if len(units) == 0 {
			c.Log.Info("no stations with recent data", "frequency", f)
			continue
		}
		ranges, gaps := monthPlan(yesterday, have, o.RefetchDays, o.MaxGapDays)
		var jobs []job
		var spans []string
		for _, r := range ranges {
			jobs = append(jobs, c.pack("month", f, units, r[0], r[1], withOverlap)...)
			spans = append(spans, r[0].Format(time.DateOnly)+".."+r[1].Format(time.DateOnly))
		}
		c.Log.Info("month plan", "frequency", f, "month", month.Format("2006-01"), "days_with_rows", len(have),
			"fetch", strings.Join(spans, ","), "stations", len(units), "requests", len(jobs), "not_filled", len(gaps))
		for _, d := range gaps {
			missing = append(missing, string(f)+" "+d.Format(time.DateOnly))
		}
		if err := c.execute(ctx, jobs, 0, o.DryRun); err != nil {
			return err
		}
		if o.DryRun {
			continue
		}
		n, err := ex.WriteMonth(ctx, f, month)
		if err != nil {
			return err
		}
		c.Log.Info("wrote month file", "frequency", f, "path", path, "rows", n)
	}
	if !o.DryRun {
		if err := export.WriteManifest(ctx, c.Store.DB, o.Dataset, c.now(), prev); err != nil {
			return err
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("%w (more than %d days; left to the monthly run): %s", ErrMissingDays, o.MaxGapDays, strings.Join(missing, ", "))
	}
	return nil
}
