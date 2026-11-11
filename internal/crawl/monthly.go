package crawl

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/hotchpotch/japan-weather-dataset/internal/export"
	"github.com/hotchpotch/japan-weather-dataset/internal/obsdl"
	"github.com/hotchpotch/japan-weather-dataset/internal/store"
)

// MonthlyOptions configures Monthly.
type MonthlyOptions struct {
	// Out is the dataset directory written by export.
	Out string
	// RefreshCycleMonths spreads the refetch of all older station-years
	// over this many monthly runs (0: no refresh).
	RefreshCycleMonths int
	// RefreshAge excludes chunks fetched more recently than this from the
	// refresh.
	RefreshAge time.Duration
	// SkipStations skips downloading the station list (it is still
	// probed for stations without coverage).
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

// Monthly is the monthly run on the machine with the crawl state:
//
//  1. download the station list and probe new stations;
//  2. refetch everything from monthlyFrom to yesterday for the active
//     stations (the month before last and the last month, after they
//     ended, plus the current month);
//  3. export (the oldest month file goes into its year file);
//  4. refetch the station-years fetched longest ago, 1/RefreshCycleMonths
//     of them, to pick up corrections JMA did not announce;
//  5. export again.
//
// The data fetched before a failure in step 4 is still exported.
func (c *Crawler) Monthly(ctx context.Context, o MonthlyOptions) error {
	if !o.SkipStations && !o.DryRun {
		if _, err := c.RefreshStations(ctx); err != nil {
			return err
		}
	}
	stations, err := c.Store.Stations(ctx)
	if err != nil {
		return err
	}
	if len(stations) == 0 {
		return errors.New("no stations; run the stations command first")
	}
	yesterday := c.Yesterday()
	from := monthlyFrom(yesterday)
	active := activeStations(stations, from)
	var unprobed []*store.StationRow
	for _, st := range active {
		if st.Coverage[obsdl.Daily] == nil || st.Coverage[obsdl.Hourly] == nil {
			unprobed = append(unprobed, st)
		}
	}
	if len(unprobed) > 0 && !o.DryRun {
		c.Log.Info("probing new stations", "stations", len(unprobed))
		if err := c.Probe(ctx, unprobed, true); err != nil {
			return err
		}
		if active, err = c.reload(ctx, active); err != nil {
			return err
		}
	}
	var recent []job
	for _, f := range []obsdl.Frequency{obsdl.Daily, obsdl.Hourly} {
		recent = append(recent, c.pack("monthly", f, recentUnits(f, active, from), from, yesterday, withOverlap)...)
	}
	refresh, err := c.refreshCycle(ctx, stations, from, o)
	if err != nil {
		return err
	}
	c.Log.Info("monthly plan", "from", from.Format(time.DateOnly), "to", yesterday.Format(time.DateOnly),
		"recent_requests", len(recent), "refresh_requests", len(refresh))
	if o.DryRun {
		if err := c.execute(ctx, recent, 0, true); err != nil {
			return err
		}
		return c.execute(ctx, refresh, 0, true)
	}
	ex := &export.Exporter{Store: c.Store, Out: o.Out, Log: c.Log}
	if err := c.runJobs(ctx, recent); err != nil {
		return err
	}
	if err := ex.Run(ctx); err != nil {
		return err
	}
	if len(refresh) == 0 {
		return nil
	}
	rerr := c.runJobs(ctx, refresh)
	if errors.Is(rerr, context.Canceled) {
		return rerr
	}
	if err := ex.Run(ctx); err != nil {
		return err
	}
	if rerr != nil {
		return fmt.Errorf("refresh (fetched data is exported): %w", rerr)
	}
	return nil
}

// refreshCycle plans the refetch of the 1/RefreshCycleMonths of the
// station-years (up to the year of from) fetched longest ago.
func (c *Crawler) refreshCycle(ctx context.Context, stations []*store.StationRow, from time.Time, o MonthlyOptions) ([]job, error) {
	if o.RefreshCycleMonths <= 0 {
		return nil, nil
	}
	var out []job
	for _, f := range []obsdl.Frequency{obsdl.Daily, obsdl.Hourly} {
		total, err := c.Store.CountChunks(ctx, f, from.Year())
		if err != nil {
			return nil, err
		}
		n := (total + o.RefreshCycleMonths - 1) / o.RefreshCycleMonths
		if n == 0 {
			continue
		}
		chunks, err := c.Store.OldestChunks(ctx, f, c.now().Add(-o.RefreshAge), from.Year(), n)
		if err != nil {
			return nil, err
		}
		c.Log.Info("refresh plan", "frequency", f, "chunks", len(chunks), "of", total,
			"oldest_fetch", func() string {
				if len(chunks) == 0 {
					return "-"
				}
				return chunks[0].FetchedAt.Format(time.DateOnly)
			}())
		out = append(out, c.chunkJobs("refresh", f, stations, chunks)...)
	}
	return out, nil
}
