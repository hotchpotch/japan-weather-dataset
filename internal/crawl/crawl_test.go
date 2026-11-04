package crawl

import (
	"fmt"
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
