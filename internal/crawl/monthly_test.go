package crawl

import (
	"context"
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

func TestMonthlyFrom(t *testing.T) {
	for _, c := range []struct{ yesterday, want time.Time }{
		{day(2026, 12, 1), day(2026, 10, 1)},
		{day(2027, 1, 1), day(2026, 11, 1)},
		{day(2027, 2, 1), day(2026, 12, 1)},
		{day(2026, 12, 15), day(2026, 10, 1)},
	} {
		if got := monthlyFrom(c.yesterday); !got.Equal(c.want) {
			t.Errorf("monthlyFrom(%v) = %v, want %v", c.yesterday, got, c.want)
		}
	}
	if got := MonthlyYears(day(2027, 1, 1)); len(got) != 2 || got[0] != 2026 || got[1] != 2027 {
		t.Errorf("MonthlyYears(2027-01-01) = %v", got)
	}
}

// fullDataset builds the crawl state of two rain-only stations with daily
// data from 2025 up to yesterday at `at` (one homogeneity break in 2025),
// exports it and returns the dataset directory.
func fullDataset(t *testing.T, at time.Time) string {
	t.Helper()
	ctx := context.Background()
	s, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	probed := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	rain := map[obsdl.Category]bool{obsdl.CatRain: true}
	var sts []obsdl.Station
	for _, id := range []string{"a0001", "a0002"} {
		sts = append(sts, obsdl.Station{ID: id, Name: id, PrefectureCode: "44", PrefectureName: "東京", Kansoku: "100000"})
	}
	if err := s.SaveStations(ctx, sts, probed); err != nil {
		t.Fatal(err)
	}
	for _, st := range sts {
		if err := s.SaveCoverage(ctx, st.ID, obsdl.Daily, store.Coverage{FirstYear: 2025, LastYear: at.Year(), Categories: rain, ProbedAt: probed}); err != nil {
			t.Fatal(err)
		}
		if err := s.SaveCoverage(ctx, st.ID, obsdl.Hourly, store.Coverage{Categories: rain, ProbedAt: probed}); err != nil {
			t.Fatal(err)
		}
	}
	c := &Crawler{Store: s, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Budget: 100_000, MaxStations: 50,
		Now: func() time.Time { return at }}
	c.fetchFunc = func(_ context.Context, _ string, req obsdl.TableRequest) (*obsdl.Table, int64, error) {
		return fakeTable(req, func(time.Time) string { return "1" }), 1, nil
	}
	if err := c.Backfill(ctx, BackfillOptions{Frequency: obsdl.Daily, FromYear: 2025, ToYear: at.Year()}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(`INSERT INTO breaks VALUES ('a0001', 'daily', 'rain', DATE '2025-06-01', 0)`); err != nil {
		t.Fatal(err)
	}
	out := t.TempDir()
	if err := (&export.Exporter{Store: s, Out: out, Log: c.Log}).Run(ctx); err != nil {
		t.Fatal(err)
	}
	return out
}

// partialCopy copies what the monthly job downloads: the metadata, all
// month files and the year files of the given years.
func partialCopy(t *testing.T, src string, years ...int) string {
	t.Helper()
	dst := t.TempDir()
	rels := []string{"manifest.json", "stations/stations.parquet", "homogeneity_breaks/homogeneity_breaks.parquet"}
	for _, f := range []obsdl.Frequency{obsdl.Daily, obsdl.Hourly} {
		months, _ := export.MonthFiles(src, f)
		for _, p := range months {
			rel, _ := filepath.Rel(src, p)
			rels = append(rels, rel)
		}
		for _, y := range years {
			rel, _ := filepath.Rel(src, export.YearPath(src, f, y))
			rels = append(rels, rel)
		}
	}
	for _, rel := range rels {
		b, err := os.ReadFile(filepath.Join(src, rel))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dst, rel)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dst, rel), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dst
}

type monthlyRun struct {
	requests []string
	dataset  string
}

func runMonthly(t *testing.T, dataset string, at time.Time) (*monthlyRun, error) {
	t.Helper()
	s, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	r := &monthlyRun{dataset: dataset}
	c := &Crawler{Store: s, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Budget: 100_000, MaxStations: 50,
		Now: func() time.Time { return at }}
	c.fetchFunc = func(_ context.Context, _ string, req obsdl.TableRequest) (*obsdl.Table, int64, error) {
		r.requests = append(r.requests, req.From.Format(time.DateOnly)+".."+req.To.Format(time.DateOnly)+" "+strings.Join(req.Stations, ","))
		return fakeTable(req, func(time.Time) string { return "2" }), int64(len(r.requests)), nil
	}
	return r, c.Monthly(context.Background(), MonthlyOptions{Dataset: dataset, SkipStations: true})
}

func (r *monthlyRun) count(t *testing.T, rel, where string) int {
	t.Helper()
	s, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var n int
	if err := s.DB.QueryRow("SELECT count(*) FROM read_parquet('" + filepath.Join(r.dataset, rel) + "', hive_partitioning = false) WHERE " + where).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func exists(dir, rel string) bool {
	_, err := os.Stat(filepath.Join(dir, rel))
	return err == nil
}

// On 12-02 the monthly run reads only 2026, refetches 10-01..12-01, puts
// October into the year file and keeps the rest of 2026 and the breaks.
func TestMonthly(t *testing.T) {
	full := fullDataset(t, time.Date(2026, 11, 2, 6, 0, 0, 0, obsdl.JST))
	ds := partialCopy(t, full, 2026)
	r, err := runMonthly(t, ds, time.Date(2026, 12, 2, 6, 0, 0, 0, obsdl.JST))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(r.requests, " "); got != "2026-09-30..2026-12-01 a0001,a0002" {
		t.Errorf("requests = %s", got)
	}
	for rel, want := range map[string]bool{
		"daily/year=2026/data.parquet":    true,
		"daily/year=2026/2026-10.parquet": false,
		"daily/year=2026/2026-11.parquet": true,
		"daily/year=2026/2026-12.parquet": true,
		"daily/year=2025/data.parquet":    false, // not downloaded, not written
		"README.md":                       true,
	} {
		if exists(ds, rel) != want {
			t.Errorf("%s exists = %v, want %v", rel, !want, want)
		}
	}
	// 2026-01-01..10-31 for two stations; refetched days have value 2.
	if n := r.count(t, "daily/year=2026/data.parquet", "true"); n != 304*2 {
		t.Errorf("year file rows = %d, want %d", n, 304*2)
	}
	if n := r.count(t, "daily/year=2026/data.parquet", "precipitation_total = 2"); n != 31*2 {
		t.Errorf("refetched October rows = %d, want %d", n, 31*2)
	}
	if n := r.count(t, "daily/year=2026/2026-12.parquet", "true"); n != 2 {
		t.Errorf("December rows = %d", n)
	}
	if n := r.count(t, "homogeneity_breaks/homogeneity_breaks.parquet", "date = DATE '2025-06-01'"); n != 1 {
		t.Errorf("the 2025 break was lost")
	}
	m, err := export.ReadManifest(ds)
	if err != nil || m.Daily.YearFilesThrough != "2026-10-31" || m.Daily.LatestDate != "2026-12-01" || len(m.Daily.MonthFiles) != 2 {
		t.Errorf("manifest = %+v (%v)", m.Daily, err)
	}
	card, _ := os.ReadFile(filepath.Join(ds, "README.md"))
	if !strings.Contains(string(card), "2025-2026") {
		t.Errorf("card does not show the whole year range 2025-2026")
	}
}

// On 01-02 the run reads 2026 and 2027: November goes into 2026, December
// and January are month files.
func TestMonthlyNewYear(t *testing.T) {
	full := fullDataset(t, time.Date(2026, 12, 2, 6, 0, 0, 0, obsdl.JST))
	ds := partialCopy(t, full, 2026, 2027)
	r, err := runMonthly(t, ds, time.Date(2027, 1, 2, 6, 0, 0, 0, obsdl.JST))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(r.requests, " "); !strings.HasPrefix(got, "2026-10-31..") {
		t.Errorf("requests = %s", got)
	}
	for rel, want := range map[string]bool{
		"daily/year=2026/2026-11.parquet": false,
		"daily/year=2026/2026-12.parquet": true,
		"daily/year=2027/2027-01.parquet": true,
		"daily/year=2027/data.parquet":    false,
	} {
		if exists(ds, rel) != want {
			t.Errorf("%s exists = %v, want %v", rel, !want, want)
		}
	}
	if n := r.count(t, "daily/year=2026/data.parquet", "true"); n != 334*2 {
		t.Errorf("2026 year file rows = %d, want %d", n, 334*2)
	}
	m, _ := export.ReadManifest(ds)
	if m.Daily.YearFilesThrough != "2026-11-30" || m.Daily.LatestDate != "2027-01-01" {
		t.Errorf("manifest = %+v", m.Daily)
	}
}

// Without the year file the manifest lists, the run stops before fetching:
// the export would rewrite that year from the month files alone.
func TestMonthlyMissingYearFile(t *testing.T) {
	full := fullDataset(t, time.Date(2026, 11, 2, 6, 0, 0, 0, obsdl.JST))
	ds := partialCopy(t, full) // no year files
	r, err := runMonthly(t, ds, time.Date(2026, 12, 2, 6, 0, 0, 0, obsdl.JST))
	if err == nil || !strings.Contains(err.Error(), "year file of daily 2026 is missing") {
		t.Fatalf("err = %v", err)
	}
	if len(r.requests) > 0 || !exists(ds, "daily/year=2026/2026-10.parquet") {
		t.Errorf("requests %v sent or files changed", r.requests)
	}
}
