package crawl

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/hotchpotch/japan-weather-dataset/internal/obsdl"
	"github.com/hotchpotch/japan-weather-dataset/internal/store"
)

// probeDays are the days of the year sampled to find the years in which a
// station has data. Four samples catch stations that ran for a few months.
var probeDays = [][2]int{{1, 15}, {4, 15}, {7, 15}, {10, 15}}

// probeBudget is the request size of probes. Requests spanning 150 years
// time out on the server well below obsdl.ServerCellLimit.
const probeBudget = 40_000

// hourlyProbeElements are enough to tell whether an official station has
// hourly values in a year.
var hourlyProbeElements = []string{"temperature", "precipitation"}

// AMeDASMinYear is the first year probed for AMeDAS stations. AMeDAS began
// in 1974; a sample of 55 AMeDAS stations had no values before 1976.
const AMeDASMinYear = 1970

// probeSet is a group of stations probed with the same elements and years.
type probeSet struct {
	f        obsdl.Frequency
	minYear  int
	official bool
}

// Probe samples a few days of every year for each station to find the
// years with data and the observed element categories. Daily coverage is
// probed for every station; hourly coverage only for official stations,
// whose hourly records can start decades after their daily records.
// AMeDAS stations reuse their daily coverage for hourly data. With
// onlyMissing, stations that already have coverage are skipped. Results are
// saved after each batch, so an interrupted probe can be resumed.
func (c *Crawler) Probe(ctx context.Context, stations []*store.StationRow, onlyMissing bool) error {
	sets := []probeSet{
		{obsdl.Daily, MinYear, true},
		{obsdl.Daily, AMeDASMinYear, false},
		{obsdl.Hourly, MinYear, true},
	}
	yesterday := c.Yesterday()
	for _, set := range sets {
		var units []unit
		byID := map[string]*store.StationRow{}
		for _, st := range stations {
			if st.Official() != set.official || (onlyMissing && st.Coverage[set.f] != nil) {
				continue
			}
			var els []obsdl.Element
			if set.f == obsdl.Daily {
				els = obsdl.ElementsFor(set.f, set.official, nil)
			} else {
				for _, e := range obsdl.HourlyElements {
					for _, k := range hourlyProbeElements {
						if e.Key == k {
							els = append(els, e)
						}
					}
				}
			}
			units = append(units, unit{station: st.ID, elements: els})
			byID[st.ID] = st
		}
		if len(units) == 0 {
			continue
		}
		// Batches are sized for the longest period (a probe day already past this year).
		first := time.Date(set.minYear, 1, 15, 0, 0, 0, 0, time.UTC)
		batches := c.packWith(probeBudget, "probe", set.f, units, first, time.Date(yesterday.Year(), 1, 15, 0, 0, 0, 0, time.UTC), sameDays)
		c.Log.Info("probing", "frequency", set.f, "official", set.official, "stations", len(units), "batches", len(batches))
		for i, batch := range batches {
			if err := c.probeBatch(ctx, batch, set, byID); err != nil {
				return err
			}
			c.Log.Info("probed", "frequency", set.f, "official", set.official, "stations", len(batch.Req.Stations),
				"progress", fmt.Sprintf("%d/%d", i+1, len(batches)))
		}
	}
	return nil
}

// probeBatch probes the stations of one batch on every probe day and saves
// their coverage.
func (c *Crawler) probeBatch(ctx context.Context, batch job, set probeSet, byID map[string]*store.StationRow) error {
	yesterday := c.Yesterday()
	type acc struct {
		first, last int
		cats        map[obsdl.Category]bool
	}
	results := map[string]*acc{}
	for _, id := range batch.Req.Stations {
		results[id] = &acc{cats: map[obsdl.Category]bool{}}
	}
	for _, pd := range probeDays {
		lastYear := yesterday.Year()
		if time.Date(lastYear, time.Month(pd[0]), pd[1], 0, 0, 0, 0, time.UTC).After(yesterday) {
			lastYear--
		}
		j := batch
		j.Req.From = time.Date(set.minYear, time.Month(pd[0]), pd[1], 0, 0, 0, 0, time.UTC)
		j.Req.To = time.Date(lastYear, time.Month(pd[0]), pd[1], 0, 0, 0, 0, time.UTC)
		j.StoreFrom, j.StoreTo = j.Req.From, j.Req.To
		err := c.fetchJob(ctx, j, func(j job, tab *obsdl.Table, _ int64) error {
			for s, id := range j.Req.Stations {
				a := results[id]
				for r, stamp := range tab.Stamps {
					for e, cell := range tab.Cells[s][r] {
						if cell.Quality <= 0 {
							continue
						}
						y := stamp.Date.Year()
						if a.first == 0 || y < a.first {
							a.first = y
						}
						if y > a.last {
							a.last = y
						}
						a.cats[j.Req.Elements[e].Category] = true
					}
				}
			}
			return nil
		})
		if err != nil {
			return fmt.Errorf("probe %s: %w", j.String(), err)
		}
	}
	now := c.now()
	for id, a := range results {
		cov := store.Coverage{FirstYear: a.first, LastYear: a.last, Categories: a.cats, ProbedAt: now}
		if err := c.Store.SaveCoverage(ctx, id, set.f, cov); err != nil {
			return err
		}
		if set.f == obsdl.Daily && !byID[id].Official() {
			if err := c.Store.SaveCoverage(ctx, id, obsdl.Hourly, cov); err != nil {
				return err
			}
		}
	}
	return nil
}

// yearRange returns the years to fetch for a station: the probed years
// widened by one year on both sides, since the probe samples only a few
// days a year.
func (c *Crawler) yearRange(f obsdl.Frequency, st *store.StationRow) (int, int, bool) {
	cov := st.Coverage[f]
	if cov == nil || cov.FirstYear == 0 {
		return 0, 0, false
	}
	first := max(cov.FirstYear-1, MinYear)
	last := min(cov.LastYear+1, c.Yesterday().Year())
	if st.EndedOn != nil {
		last = min(last, st.EndedOn.Year())
	}
	return first, last, first <= last
}

// BackfillOptions selects what Backfill fetches.
type BackfillOptions struct {
	Frequency obsdl.Frequency
	Stations  map[string]bool // nil means all
	FromYear  int             // 0 means no limit
	ToYear    int
	MaxJobs   int // 0 means no limit
	DryRun    bool
}

// Backfill fetches every station-year that has not been fetched completely,
// newest years first.
func (c *Crawler) Backfill(ctx context.Context, o BackfillOptions) error {
	stations, err := c.Store.Stations(ctx)
	if err != nil {
		return err
	}
	if o.Stations != nil {
		var sel []*store.StationRow
		for _, st := range stations {
			if o.Stations[st.ID] {
				sel = append(sel, st)
			}
		}
		stations = sel
	}
	var unprobed []*store.StationRow
	for _, st := range stations {
		if st.Coverage[obsdl.Daily] == nil || st.Coverage[obsdl.Hourly] == nil {
			unprobed = append(unprobed, st)
		}
	}
	if len(unprobed) > 0 {
		c.Log.Info("probing stations without coverage", "stations", len(unprobed))
		if o.DryRun {
			return fmt.Errorf("%d stations have not been probed; run without --dry-run or run probe first", len(unprobed))
		}
		if err := c.Probe(ctx, unprobed, true); err != nil {
			return err
		}
		if stations, err = c.reload(ctx, stations); err != nil {
			return err
		}
	}
	chunks, err := c.Store.Chunks(ctx, o.Frequency)
	if err != nil {
		return err
	}
	yesterday := c.Yesterday()
	// A station-year is fetched from its first missing day: a chunk that
	// starts on Jan 1 but ends before yesterday (the current year of an
	// earlier run) only needs its tail.
	type span struct {
		year int
		from time.Time
	}
	bySpan := map[span][]unit{}
	for _, st := range stations {
		first, last, ok := c.yearRange(o.Frequency, st)
		if !ok {
			continue
		}
		if o.FromYear > 0 {
			first = max(first, o.FromYear)
		}
		if o.ToYear > 0 {
			last = min(last, o.ToYear)
		}
		els := elementsFor(o.Frequency, st)
		for y := first; y <= last; y++ {
			from := time.Date(y, 1, 1, 0, 0, 0, 0, time.UTC)
			end := minTime(time.Date(y, 12, 31, 0, 0, 0, 0, time.UTC), yesterday)
			if ch, ok := chunks[st.ID][y]; ok && !ch.From.After(from) {
				if !ch.To.Before(end) {
					continue
				}
				from = ch.To.AddDate(0, 0, 1)
			}
			k := span{y, from}
			bySpan[k] = append(bySpan[k], unit{station: st.ID, elements: els})
		}
	}
	spans := make([]span, 0, len(bySpan))
	for k := range bySpan {
		spans = append(spans, k)
	}
	sort.Slice(spans, func(i, j int) bool {
		if spans[i].year != spans[j].year {
			return spans[i].year > spans[j].year
		}
		return spans[i].from.Before(spans[j].from)
	})
	var jobs []job
	for _, k := range spans {
		to := minTime(time.Date(k.year, 12, 31, 0, 0, 0, 0, time.UTC), yesterday)
		jobs = append(jobs, c.pack("backfill", o.Frequency, bySpan[k], k.from, to, withOverlap)...)
	}
	return c.execute(ctx, jobs, o.MaxJobs, o.DryRun)
}

func (c *Crawler) execute(ctx context.Context, jobs []job, maxJobs int, dryRun bool) error {
	if maxJobs > 0 && len(jobs) > maxJobs {
		c.Log.Info("limiting jobs", "planned", len(jobs), "max", maxJobs)
		jobs = jobs[:maxJobs]
	}
	if dryRun {
		cells := 0
		for _, j := range jobs {
			cells += j.Req.Cells()
		}
		for i, j := range jobs {
			if i < 20 {
				fmt.Println(j.String())
			}
		}
		fmt.Printf("planned %d requests, %d cells\n", len(jobs), cells)
		return nil
	}
	return c.runJobs(ctx, jobs)
}

func (c *Crawler) reload(ctx context.Context, sel []*store.StationRow) ([]*store.StationRow, error) {
	all, err := c.Store.Stations(ctx)
	if err != nil {
		return nil, err
	}
	want := map[string]bool{}
	for _, st := range sel {
		want[st.ID] = true
	}
	var out []*store.StationRow
	for _, st := range all {
		if want[st.ID] {
			out = append(out, st)
		}
	}
	return out, nil
}

// UpdateOptions controls the daily update.
type UpdateOptions struct {
	// LookbackDays is the number of days up to yesterday fetched again on
	// every run; JMA revises recent values (e.g. data restored after a
	// network outage) days to weeks later.
	LookbackDays int
	// RefreshJobs is the number of extra requests that refetch the chunks
	// fetched longest ago, so that later corrections of old values reach
	// the dataset over time.
	RefreshJobs int
	// RefreshAge is the minimum age of a chunk to be refreshed.
	RefreshAge time.Duration
	DryRun     bool
}

// Update fetches the recent days of every active station and refreshes
// some of the oldest chunks.
func (c *Crawler) Update(ctx context.Context, o UpdateOptions) error {
	stations, err := c.Store.Stations(ctx)
	if err != nil {
		return err
	}
	if len(stations) == 0 {
		return fmt.Errorf("no stations; run the stations command first")
	}
	yesterday := c.Yesterday()
	from := yesterday.AddDate(0, 0, -(o.LookbackDays - 1))
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
	var jobs []job
	for _, f := range []obsdl.Frequency{obsdl.Daily, obsdl.Hourly} {
		jobs = append(jobs, c.pack("update", f, recentUnits(f, active, from), from, yesterday, withOverlap)...)
	}
	if o.RefreshJobs > 0 {
		refresh, err := c.refreshJobs(ctx, stations, from, o)
		if err != nil {
			return err
		}
		jobs = append(jobs, refresh...)
	}
	return c.execute(ctx, jobs, 0, o.DryRun)
}

// refreshJobs plans requests for the chunks fetched longest ago.
func (c *Crawler) refreshJobs(ctx context.Context, stations []*store.StationRow, windowFrom time.Time, o UpdateOptions) ([]job, error) {
	var out []job
	for _, f := range []obsdl.Frequency{obsdl.Daily, obsdl.Hourly} {
		// Fetch more candidates than needed; packing merges them.
		chunks, err := c.Store.OldestChunks(ctx, f, c.now().Add(-o.RefreshAge), windowFrom.Year(), o.RefreshJobs*c.MaxStations)
		if err != nil {
			return nil, err
		}
		jobs := c.chunkJobs("refresh", f, stations, chunks)
		n := (o.RefreshJobs + 1) / 2
		if len(jobs) > n {
			jobs = jobs[:n]
		}
		out = append(out, jobs...)
	}
	return out, nil
}

// chunkJobs plans requests that refetch whole station-years (up to
// yesterday), packed per year.
func (c *Crawler) chunkJobs(purpose string, f obsdl.Frequency, stations []*store.StationRow, chunks []store.Chunk) []job {
	byID := map[string]*store.StationRow{}
	for _, st := range stations {
		byID[st.ID] = st
	}
	yesterday := c.Yesterday()
	byYear := map[int][]unit{}
	for _, ch := range chunks {
		st := byID[ch.Station]
		if st == nil {
			continue
		}
		byYear[ch.Year] = append(byYear[ch.Year], unit{station: st.ID, elements: elementsFor(f, st)})
	}
	years := make([]int, 0, len(byYear))
	for y := range byYear {
		years = append(years, y)
	}
	sort.Ints(years)
	var jobs []job
	for _, y := range years {
		from := time.Date(y, 1, 1, 0, 0, 0, 0, time.UTC)
		to := minTime(time.Date(y, 12, 31, 0, 0, 0, 0, time.UTC), yesterday)
		jobs = append(jobs, c.pack(purpose, f, byYear[y], from, to, withOverlap)...)
	}
	return jobs
}
