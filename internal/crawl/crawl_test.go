package crawl

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/hotchpotch/japan-weather-dataset/internal/obsdl"
)

func day(y, m, d int) time.Time { return time.Date(y, time.Month(m), d, 0, 0, 0, 0, time.UTC) }

func TestPack(t *testing.T) {
	c := &Crawler{Budget: 180_000, MaxStations: 50}
	official := obsdl.ElementsFor(obsdl.Hourly, true, nil)
	rain := obsdl.ElementsFor(obsdl.Hourly, false, map[obsdl.Category]bool{obsdl.CatRain: true})
	var units []unit
	for i := range 3 {
		units = append(units, unit{station: fmt.Sprintf("s%05d", i), elements: official})
	}
	for i := range 60 {
		units = append(units, unit{station: fmt.Sprintf("a%04d", i), elements: rain})
	}
	jobs := c.pack("backfill", obsdl.Hourly, units, day(2024, 1, 1), day(2024, 12, 31), withOverlap)
	counts := map[int]int{}
	for _, j := range jobs {
		if j.Req.Cells() > c.Budget {
			t.Errorf("job over budget: %s", j)
		}
		if !j.Req.From.Equal(day(2023, 12, 31)) || !j.StoreFrom.Equal(day(2024, 1, 1)) {
			t.Errorf("overlap day missing: %s from %v", j, j.Req.From)
		}
		counts[len(j.Req.Stations)]++
	}
	// official: 15 elements x 367 days x 24h = 132,120 cells -> 1 station per request
	// rain only: 1 element x 8,808 hours -> 20 stations per request
	if counts[1] != 3 || counts[20] != 3 || len(jobs) != 6 {
		t.Errorf("unexpected packing: %v", counts)
	}
}

func TestPackNoOverlapBeforeMinYear(t *testing.T) {
	c := &Crawler{Budget: 100_000, MaxStations: 50}
	units := []unit{{station: "s47412", elements: obsdl.ElementsFor(obsdl.Daily, true, nil)}}
	jobs := c.pack("backfill", obsdl.Daily, units, day(MinYear, 1, 1), day(MinYear, 12, 31), withOverlap)
	if len(jobs) != 1 || !jobs[0].Req.From.Equal(day(MinYear, 1, 1)) {
		t.Errorf("request must not start before %d-01-01: %v", MinYear, jobs)
	}
	jobs = c.pack("backfill", obsdl.Daily, units, day(MinYear+1, 1, 1), day(MinYear+1, 12, 31), withOverlap)
	if len(jobs) != 1 || !jobs[0].Req.From.Equal(day(MinYear, 12, 31)) {
		t.Errorf("overlap day missing: %v", jobs)
	}
}

func TestSplit(t *testing.T) {
	j := job{Req: obsdl.TableRequest{Frequency: obsdl.Daily, Stations: []string{"a", "b", "c"},
		From: day(2023, 12, 31), To: day(2024, 12, 31)}, StoreFrom: day(2024, 1, 1), StoreTo: day(2024, 12, 31)}
	a, b, ok := split(j)
	if !ok || len(a.Req.Stations) != 1 || len(b.Req.Stations) != 2 {
		t.Fatalf("station split: %v %v", a.Req.Stations, b.Req.Stations)
	}
	a, b, ok = split(a)
	if !ok || !a.StoreTo.Add(24*time.Hour).Equal(b.StoreFrom) || !b.Req.From.Equal(a.StoreTo) {
		t.Fatalf("period split: a=%v..%v b=%v..%v (req from %v)", a.StoreFrom, a.StoreTo, b.StoreFrom, b.StoreTo, b.Req.From)
	}
	same := job{Req: obsdl.TableRequest{Stations: []string{"a"}, SameDaysEachYear: true, From: day(1872, 7, 15), To: day(2026, 7, 15)},
		StoreFrom: day(1872, 7, 15), StoreTo: day(2026, 7, 15)}
	a, b, ok = split(same)
	if !ok || !a.Req.To.Equal(day(1949, 7, 15)) || !b.Req.From.Equal(day(1950, 7, 15)) || !b.Req.SameDaysEachYear {
		t.Fatalf("same-days split: a=%v..%v b=%v..%v", a.Req.From, a.Req.To, b.Req.From, b.Req.To)
	}
	one := job{Req: obsdl.TableRequest{Stations: []string{"a"}}, StoreFrom: day(2024, 1, 1), StoreTo: day(2024, 1, 1)}
	if _, _, ok := split(one); ok {
		t.Error("a single station-day cannot be split")
	}
}

func TestPackSameDays(t *testing.T) {
	c := &Crawler{Budget: 180_000, MaxStations: 50}
	var units []unit
	for i := range 120 {
		units = append(units, unit{station: fmt.Sprintf("a%04d", i), elements: obsdl.ElementsFor(obsdl.Daily, false, nil)})
	}
	jobs := c.pack("probe", obsdl.Daily, units, day(MinYear, 7, 15), day(2026, 7, 15), sameDays)
	// 155 days x 14 elements per station: limited by MaxStations
	if len(jobs) != 3 || !jobs[0].Req.SameDaysEachYear || len(jobs[0].Req.Stations) != 50 {
		t.Fatalf("jobs=%d first=%s", len(jobs), jobs[0])
	}
}

func TestPackSplitsPeriodOverBudget(t *testing.T) {
	c := &Crawler{Budget: 100_000, MaxStations: 50}
	official := obsdl.ElementsFor(obsdl.Hourly, true, nil)
	units := []unit{{station: "s00001", elements: official}, {station: "s00002", elements: official}}
	jobs := c.pack("backfill", obsdl.Hourly, units, day(2024, 1, 1), day(2024, 12, 31), withOverlap)
	// one station-year is 132,120 cells: two half-years per station
	if len(jobs) != 4 {
		t.Fatalf("got %d jobs, want 4", len(jobs))
	}
	next := map[string]time.Time{"s00001": day(2024, 1, 1), "s00002": day(2024, 1, 1)}
	for _, j := range jobs {
		if j.Req.Cells() > c.Budget || len(j.Req.Stations) != 1 {
			t.Errorf("job over budget: %s", j)
		}
		s := j.Req.Stations[0]
		if !j.StoreFrom.Equal(next[s]) || !j.Req.From.Equal(j.StoreFrom.AddDate(0, 0, -1)) {
			t.Errorf("gap or missing overlap: %s store %v..%v req from %v", s, j.StoreFrom, j.StoreTo, j.Req.From)
		}
		next[s] = j.StoreTo.AddDate(0, 0, 1)
	}
	for s, n := range next {
		if !n.Equal(day(2025, 1, 1)) {
			t.Errorf("%s not covered to the end of the year: next %v", s, n)
		}
	}
}

func TestFetchJobLowersLimitOnServerError(t *testing.T) {
	var sent []int
	c := &Crawler{Budget: 100_000, MaxStations: 50, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	// the server fails on anything over 30,000 cells, like a gateway timeout
	c.fetchFunc = func(_ context.Context, _ string, req obsdl.TableRequest) (*obsdl.Table, int64, error) {
		sent = append(sent, req.Cells())
		if req.Cells() > 30_000 {
			return nil, 0, obsdl.ErrServer
		}
		return nil, 0, nil
	}
	stations := make([]string, 16)
	for i := range stations {
		stations[i] = fmt.Sprintf("a%04d", i)
	}
	mk := func() job {
		return job{Req: obsdl.TableRequest{Frequency: obsdl.Daily, Elements: make([]obsdl.Element, 16), Stations: stations,
			From: day(2024, 12, 31), To: day(2025, 12, 31)}, StoreFrom: day(2025, 1, 1), StoreTo: day(2025, 12, 31)}
	}
	var stored int
	handle := func(j job, _ *obsdl.Table, _ int64) error { stored += len(j.Req.Stations); return nil }
	// 16 stations x 16 elements x 366 days = 93,696 cells
	if err := c.fetchJob(context.Background(), mk(), handle); err != nil {
		t.Fatal(err)
	}
	failures := 0
	for _, n := range sent {
		if n > 30_000 {
			failures++
		}
	}
	if stored != 16 || c.limit > 30_000 {
		t.Fatalf("stored %d stations, limit %d, sent %v", stored, c.limit, sent)
	}
	// the next job is split up front: no more failed requests
	sent, stored = nil, 0
	if err := c.fetchJob(context.Background(), mk(), handle); err != nil {
		t.Fatal(err)
	}
	for _, n := range sent {
		if n > c.limit {
			t.Errorf("request of %d cells over limit %d", n, c.limit)
		}
	}
	if stored != 16 {
		t.Errorf("second job stored %d stations", stored)
	}
	t.Logf("first job failures %d, limit %d, second job requests %v", failures, c.limit, sent)
}
