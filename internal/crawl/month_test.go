package crawl

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hotchpotch/japan-weather-dataset/internal/export"
	"github.com/hotchpotch/japan-weather-dataset/internal/obsdl"
	"github.com/hotchpotch/japan-weather-dataset/internal/store"
)

func dates(ranges [][2]time.Time) string {
	var s []string
	for _, r := range ranges {
		s = append(s, r[0].Format("01-02")+".."+r[1].Format("01-02"))
	}
	return strings.Join(s, " ")
}

func TestMonthPlan(t *testing.T) {
	have := func(days ...int) map[time.Time]bool {
		m := map[time.Time]bool{}
		for _, d := range days {
			m[day(2026, 11, d)] = true
		}
		return m
	}
	cases := []struct {
		name            string
		yesterday       time.Time
		have            map[time.Time]bool
		refetch, maxGap int
		want, missing   string
	}{
		{"complete", day(2026, 11, 10), have(1, 2, 3, 4, 5, 6, 7, 8, 9), 3, 3, "11-08..11-10", ""},
		{"one day missed", day(2026, 11, 10), have(1, 2, 3, 4, 5, 6), 3, 3, "11-07..11-10", ""},
		{"gap in the middle", day(2026, 11, 10), have(1, 2, 4, 5, 6, 7), 3, 3, "11-03..11-03 11-08..11-10", ""},
		{"too many missing", day(2026, 11, 10), have(1, 2), 3, 3, "11-05..11-10", "11-03,11-04"},
		{"new month file", day(2026, 11, 2), nil, 3, 3, "11-01..11-02", ""},
		{"first of the month", day(2026, 12, 1), nil, 3, 3, "12-01..12-01", ""},
		{"no refetch", day(2026, 11, 10), have(1, 2, 3, 4, 5, 6, 7, 8, 9, 10), 0, 3, "", ""},
		{"no gap filling", day(2026, 11, 10), have(1, 2, 3), 3, 0, "11-08..11-10", "11-04,11-05,11-06,11-07"},
	}
	for _, c := range cases {
		ranges, missing := monthPlan(c.yesterday, c.have, c.refetch, c.maxGap)
		var ms []string
		for _, d := range missing {
			ms = append(ms, d.Format("01-02"))
		}
		if got := dates(ranges); got != c.want || strings.Join(ms, ",") != c.missing {
			t.Errorf("%s: fetch %q missing %q, want %q %q", c.name, got, strings.Join(ms, ","), c.want, c.missing)
		}
	}
}

// fakeTable answers a daily request with one row per day and value(day)
// for every element.
func fakeTable(req obsdl.TableRequest, value func(time.Time) string) *obsdl.Table {
	tab := &obsdl.Table{DownloadedAt: time.Now()}
	for d := req.From; !d.After(req.To); d = d.AddDate(0, 0, 1) {
		tab.Stamps = append(tab.Stamps, obsdl.Stamp{Date: d})
	}
	for range req.Stations {
		tab.StationNames = append(tab.StationNames, "test")
		var rows [][]obsdl.Cell
		for _, st := range tab.Stamps {
			var cells []obsdl.Cell
			for range req.Elements {
				cells = append(cells, obsdl.Cell{Value: value(st.Date), Quality: 8, NoPhenomenon: -1, DirQuality: -1, Homogeneity: 1})
			}
			rows = append(rows, cells)
		}
		tab.Cells = append(tab.Cells, rows)
	}
	return tab
}

// writeStations writes stations/stations.parquet of a dataset with one
// rain-only AMeDAS station that has daily but no hourly data.
func writeStations(t *testing.T, dataset string) {
	t.Helper()
	ctx := context.Background()
	s, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	if err := s.SaveStations(ctx, []obsdl.Station{{ID: "a0001", Name: "テスト", PrefectureCode: "44", PrefectureName: "東京",
		Latitude: 35.7, Longitude: 139.7, Kansoku: "100000"}}, now); err != nil {
		t.Fatal(err)
	}
	rain := map[obsdl.Category]bool{obsdl.CatRain: true}
	if err := s.SaveCoverage(ctx, "a0001", obsdl.Daily, store.Coverage{FirstYear: 2000, LastYear: 2026, Categories: rain, ProbedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveCoverage(ctx, "a0001", obsdl.Hourly, store.Coverage{Categories: rain, ProbedAt: now}); err != nil {
		t.Fatal(err)
	}
	ex := &export.Exporter{Store: s, Out: dataset, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if err := ex.Run(ctx); err != nil {
		t.Fatal(err)
	}
}

type monthRun struct {
	at       time.Time // run time
	requests []obsdl.TableRequest
}

// runMonth runs UpdateMonth like the month command: with a fresh state.
func runMonth(t *testing.T, dataset string, at time.Time, value func(time.Time) string) (*monthRun, error) {
	t.Helper()
	s, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	run := &monthRun{at: at}
	c := &Crawler{Store: s, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Budget: 100_000, MaxStations: 50,
		Now: func() time.Time { return at }}
	id := int64(0)
	c.fetchFunc = func(_ context.Context, _ string, req obsdl.TableRequest) (*obsdl.Table, int64, error) {
		run.requests = append(run.requests, req)
		id++
		return fakeTable(req, value), id, nil
	}
	err = c.UpdateMonth(context.Background(), MonthOptions{Dataset: dataset, Frequencies: []obsdl.Frequency{obsdl.Daily, obsdl.Hourly},
		RefetchDays: 3, MaxGapDays: 3})
	return run, err
}

func TestUpdateMonth(t *testing.T) {
	dataset := t.TempDir()
	writeStations(t, dataset)
	jst := func(m, d, h int) time.Time { return time.Date(2026, time.Month(m), d, h, 0, 0, 0, obsdl.JST) }
	value := func(d time.Time) string { return fmt.Sprint(d.Day()) }

	// Run 1 on 11-09 without a month file: refetch 11-06..08, fill the
	// latest 3 missing days (11-03..05); 11-01 and 11-02 stay missing.
	r1, err := runMonth(t, dataset, jst(11, 9, 6), value)
	if !errors.Is(err, ErrMissingDays) || !strings.Contains(err.Error(), "daily 2026-11-01, daily 2026-11-02") {
		t.Fatalf("run 1: err = %v", err)
	}
	if len(r1.requests) != 1 || !r1.requests[0].From.Equal(day(2026, 11, 2)) || !r1.requests[0].To.Equal(day(2026, 11, 8)) {
		t.Fatalf("run 1 requests: %+v", r1.requests)
	}
	m, err := export.ReadManifest(dataset)
	if err != nil || m.Daily == nil || len(m.Daily.MonthFiles) != 1 || m.Hourly != nil {
		t.Fatalf("manifest after run 1: %+v (%v)", m, err)
	}
	if mf := m.Daily.MonthFiles[0]; mf.FirstDate != "2026-11-03" || mf.LastDate != "2026-11-08" || strings.Join(mf.MissingDays, ",") != "2026-11-01,2026-11-02" {
		t.Errorf("month file after run 1: %+v", mf)
	}

	// Run 2 on 11-10: refetch 11-07..09 (11-08 changed at JMA), fill
	// 11-01..02.
	r2, err := runMonth(t, dataset, jst(11, 10, 6), func(d time.Time) string {
		if d.Day() == 8 {
			return "80"
		}
		return value(d)
	})
	if err != nil {
		t.Fatalf("run 2: %v", err)
	}
	var spans []string
	for _, r := range r2.requests {
		spans = append(spans, r.From.Format("01-02")+".."+r.To.Format("01-02"))
	}
	// each request starts one day early to detect homogeneity breaks
	if got := strings.Join(spans, " "); got != "10-31..11-02 11-06..11-09" {
		t.Errorf("run 2 requests: %s", got)
	}

	s, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	path := filepath.Join(dataset, "daily", "year=2026", "2026-11.parquet")
	rows, err := s.DB.Query("SELECT day(date), precipitation_total, updated_at FROM read_parquet('" + path + "', hive_partitioning = false) ORDER BY date")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	first, second := r1.at.UTC(), r2.at.UTC()
	want := map[int]time.Time{1: second, 2: second, 3: first, 4: first, 5: first, 6: first, 7: first, 8: second, 9: second}
	n := 0
	for rows.Next() {
		var d int
		var p float64
		var u time.Time
		if err := rows.Scan(&d, &p, &u); err != nil {
			t.Fatal(err)
		}
		n++
		if !u.Equal(want[d]) {
			t.Errorf("day %d: updated_at %v, want %v", d, u.UTC(), want[d])
		}
		if wantP := float64(d); (d == 8 && p != 80) || (d != 8 && p != wantP) {
			t.Errorf("day %d: precipitation %v", d, p)
		}
	}
	if n != 9 {
		t.Errorf("rows = %d, want 9", n)
	}
	m, _ = export.ReadManifest(dataset)
	if mf := m.Daily.MonthFiles[0]; m.Daily.LatestDate != "2026-11-09" || len(mf.MissingDays) != 0 || mf.Rows != 9 {
		t.Errorf("manifest after run 2: %+v %+v", m.Daily, mf)
	}

	// Run 3 on 12-02 (yesterday 12-01) starts the December file and leaves
	// November to the monthly run.
	if _, err := runMonth(t, dataset, jst(12, 2, 6), value); err != nil {
		t.Fatalf("run 3: %v", err)
	}
	m, _ = export.ReadManifest(dataset)
	if len(m.Daily.MonthFiles) != 2 || m.Daily.MonthFiles[0].LastDate != "2026-11-09" || m.Daily.MonthFiles[1].Month != "2026-12" || m.Daily.LatestDate != "2026-12-01" {
		t.Errorf("manifest after run 3: %+v", m.Daily)
	}
}

// On GitHub Actions only the current month files are downloaded: the
// manifest keeps the entries of the other files.
func TestUpdateMonthPartialDataset(t *testing.T) {
	full := t.TempDir()
	writeStations(t, full)
	jst := func(m, d int) time.Time { return time.Date(2026, time.Month(m), d, 6, 0, 0, 0, obsdl.JST) }
	value := func(d time.Time) string { return "1" }
	for _, at := range []time.Time{jst(10, 4), jst(11, 3)} {
		if _, err := runMonth(t, full, at, value); err != nil {
			t.Fatal(err)
		}
	}
	partial := t.TempDir()
	for _, p := range []string{"manifest.json", "stations/stations.parquet", "daily/year=2026/2026-11.parquet"} {
		b, err := os.ReadFile(filepath.Join(full, p))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(filepath.Join(partial, p)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(partial, p), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := runMonth(t, partial, jst(11, 4), value); err != nil {
		t.Fatal(err)
	}
	m, err := export.ReadManifest(partial)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, mf := range m.Daily.MonthFiles {
		got = append(got, mf.Month+":"+mf.FirstDate+".."+mf.LastDate)
	}
	if strings.Join(got, " ") != "2026-10:2026-10-01..2026-10-03 2026-11:2026-11-01..2026-11-03" || m.Daily.LatestDate != "2026-11-03" {
		t.Errorf("manifest daily = %+v (%v)", m.Daily, got)
	}
}

// Entries of a stale manifest for months that must have gone into a year
// file are dropped.
func TestUpdateMonthDropsStaleEntries(t *testing.T) {
	dataset := t.TempDir()
	writeStations(t, dataset)
	stale := export.Manifest{Daily: &export.FrequencyManifest{LatestDate: "2026-11-30", YearFilesThrough: "2026-08-31", MonthFiles: []export.MonthManifest{
		{Month: "2026-09", Path: "daily/year=2026/2026-09.parquet", LastDate: "2026-09-30"},
		{Month: "2026-10", Path: "daily/year=2026/2026-10.parquet", LastDate: "2026-10-31"},
		{Month: "2026-11", Path: "daily/year=2026/2026-11.parquet", LastDate: "2026-11-30"},
	}}}
	b, _ := json.Marshal(stale)
	if err := os.WriteFile(filepath.Join(dataset, "manifest.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := runMonth(t, dataset, time.Date(2026, 12, 3, 6, 0, 0, 0, obsdl.JST), func(time.Time) string { return "1" }); err != nil {
		t.Fatal(err)
	}
	m, _ := export.ReadManifest(dataset)
	var got []string
	for _, mf := range m.Daily.MonthFiles {
		got = append(got, mf.Month)
	}
	if strings.Join(got, " ") != "2026-10 2026-11 2026-12" {
		t.Errorf("month files = %v", got)
	}
}
