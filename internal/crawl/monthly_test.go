package crawl

import (
	"context"
	"errors"
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
		// The run on the 1st (yesterday the last day of a month).
		{day(2026, 11, 30), day(2026, 10, 1)},
		{day(2026, 12, 31), day(2026, 11, 1)},
		{day(2027, 2, 28), day(2027, 1, 1)},  // 2027-03-01
		{day(2028, 2, 28), day(2027, 12, 1)}, // 2028-02-29: still February
		{day(2028, 2, 29), day(2028, 1, 1)},  // 2028-03-01
	} {
		if got := monthlyFrom(c.yesterday); !got.Equal(c.want) {
			t.Errorf("monthlyFrom(%v) = %v, want %v", c.yesterday, got, c.want)
		}
	}
	if got := MonthlyYears(day(2027, 1, 1)); len(got) != 2 || got[0] != 2026 || got[1] != 2027 {
		t.Errorf("MonthlyYears(2027-01-01) = %v", got)
	}
	if got := MonthlyYears(day(2026, 12, 31)); len(got) != 1 || got[0] != 2026 {
		t.Errorf("MonthlyYears(2026-12-31) = %v", got)
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
	stats    RunStats
}

func runMonthly(t *testing.T, dataset string, at time.Time) (*monthlyRun, error) {
	t.Helper()
	return runMonthlyWith(t, dataset, at, MonthlyOptions{}, func(req obsdl.TableRequest) *obsdl.Table {
		return fakeTable(req, func(time.Time) string { return "2" })
	})
}

// runMonthlyWith runs Monthly on dataset with o (Dataset and SkipStations
// are set) and the source answering with table.
func runMonthlyWith(t *testing.T, dataset string, at time.Time, o MonthlyOptions, table func(obsdl.TableRequest) *obsdl.Table) (*monthlyRun, error) {
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
		return table(req), int64(len(r.requests)), nil
	}
	o.Dataset, o.SkipStations = dataset, true
	err = c.Monthly(context.Background(), o)
	r.stats = c.Stats()
	return r, err
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
	// 10-01..12-01 (62 days) for two stations; the overlap day is not stored.
	if r.stats != (RunStats{Requests: 1, Rows: 62 * 2}) {
		t.Errorf("stats = %+v", r.stats)
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
	if !strings.Contains(string(card), "Daily values from 2025 ") {
		t.Errorf("card does not show the first year 2025 of the whole range")
	}
	if strings.Contains(string(card), "2026-12-01") {
		t.Errorf("card names the latest day, which goes stale between the runs that rewrite it")
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

// On the 1st at 20:00 (yesterday 11-30) the run refetches 10-01..11-30 and
// leaves the same files as on 12-02 without December: October goes into
// the year file, November stays the only month file.
func TestMonthlyFirstOfMonth(t *testing.T) {
	full := fullDataset(t, time.Date(2026, 11, 2, 6, 0, 0, 0, obsdl.JST))
	ds := partialCopy(t, full, 2026)
	r, err := runMonthly(t, ds, time.Date(2026, 12, 1, 20, 0, 0, 0, obsdl.JST))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(r.requests, " "); got != "2026-09-30..2026-11-30 a0001,a0002" {
		t.Errorf("requests = %s", got)
	}
	if r.stats != (RunStats{Requests: 1, Rows: 61 * 2}) {
		t.Errorf("stats = %+v", r.stats)
	}
	for rel, want := range map[string]bool{
		"daily/year=2026/2026-10.parquet": false,
		"daily/year=2026/2026-11.parquet": true,
		"daily/year=2026/2026-12.parquet": false, // no rows yet
	} {
		if exists(ds, rel) != want {
			t.Errorf("%s exists = %v, want %v", rel, !want, want)
		}
	}
	if n := r.count(t, "daily/year=2026/data.parquet", "true"); n != 304*2 {
		t.Errorf("year file rows = %d, want %d", n, 304*2)
	}
	if n := r.count(t, "daily/year=2026/2026-11.parquet", "precipitation_total = 2"); n != 30*2 {
		t.Errorf("refetched November rows = %d, want %d", n, 30*2)
	}
	m, err := export.ReadManifest(ds)
	if err != nil || m.Daily.YearFilesThrough != "2026-10-31" || m.Daily.LatestDate != "2026-11-30" || len(m.Daily.MonthFiles) != 1 {
		t.Errorf("manifest = %+v (%v)", m.Daily, err)
	}
}

// On 2028-03-01 (yesterday the leap day 02-29) the run refetches
// 01-01..02-29: January goes into the year file of 2028, February with its
// 29 days stays the month file.
func TestMonthlyLeapDay(t *testing.T) {
	full := fullDataset(t, time.Date(2028, 2, 2, 6, 0, 0, 0, obsdl.JST))
	ds := partialCopy(t, full, 2028)
	r, err := runMonthly(t, ds, time.Date(2028, 3, 1, 20, 0, 0, 0, obsdl.JST))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(r.requests, " "); got != "2027-12-31..2028-02-29 a0001,a0002" {
		t.Errorf("requests = %s", got)
	}
	for rel, want := range map[string]bool{
		"daily/year=2028/data.parquet":    true,
		"daily/year=2028/2028-01.parquet": false,
		"daily/year=2028/2028-02.parquet": true,
		"daily/year=2028/2028-03.parquet": false,
	} {
		if exists(ds, rel) != want {
			t.Errorf("%s exists = %v, want %v", rel, !want, want)
		}
	}
	if n := r.count(t, "daily/year=2028/data.parquet", "true"); n != 31*2 {
		t.Errorf("2028 year file rows = %d, want %d", n, 31*2)
	}
	if n := r.count(t, "daily/year=2028/2028-02.parquet", "precipitation_total = 2"); n != 29*2 {
		t.Errorf("February rows = %d, want %d", n, 29*2)
	}
	m, _ := export.ReadManifest(ds)
	if m.Daily.YearFilesThrough != "2028-01-31" || m.Daily.LatestDate != "2028-02-29" || len(m.Daily.MonthFiles) != 1 {
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

func TestCheckRowLoss(t *testing.T) {
	oct := day(2026, 10, 1)
	k := func(st string) store.StationMonth { return store.StationMonth{Station: st, Month: oct} }
	before := map[store.StationMonth]int{k("a"): 31 * 24, k("b"): 31 * 24, k("c"): 31 * 24, k("d"): 31 * 24}
	after := map[store.StationMonth]int{
		k("a"): 31 * 24, // unchanged
		k("b"): 30 * 24, // one day less: an ordinary revision
		k("c"): 15 * 24, // half of the month lost
		k("e"): 31 * 24, // new station
	} // d: all rows lost
	got := checkRowLoss(obsdl.Hourly, before, after)
	want := []string{"hourly c 2026-10: 744 -> 360 rows", "hourly d 2026-10: 744 -> 0 rows"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("checkRowLoss = %q, want %q", got, want)
	}
	// Two daily rows of a short month are a loss; one is not.
	if got := checkRowLoss(obsdl.Daily, map[store.StationMonth]int{k("a"): 10}, map[store.StationMonth]int{k("a"): 9}); len(got) != 0 {
		t.Errorf("one daily row reported: %q", got)
	}
	if got := checkRowLoss(obsdl.Daily, map[store.StationMonth]int{k("a"): 10}, map[store.StationMonth]int{k("a"): 8}); len(got) != 1 {
		t.Errorf("two of ten daily rows not reported: %q", got)
	}
}

// When the source answers without the second half of October, the monthly
// run on 12-02 stops before the export: October stays a month file with
// all its rows instead of being settled in the year file without them.
func TestMonthlyRowLoss(t *testing.T) {
	full := fullDataset(t, time.Date(2026, 11, 2, 6, 0, 0, 0, obsdl.JST))
	truncated := func(req obsdl.TableRequest) *obsdl.Table {
		tab := fakeTable(req, func(time.Time) string { return "2" })
		var stamps []obsdl.Stamp
		keep := make([]bool, len(tab.Stamps))
		for i, st := range tab.Stamps {
			if st.Date.Before(day(2026, 10, 16)) || !st.Date.Before(day(2026, 11, 1)) {
				keep[i] = true
				stamps = append(stamps, st)
			}
		}
		for s := range tab.Cells {
			var rows [][]obsdl.Cell
			for i, row := range tab.Cells[s] {
				if keep[i] {
					rows = append(rows, row)
				}
			}
			tab.Cells[s] = rows
		}
		tab.Stamps = stamps
		return tab
	}
	at := time.Date(2026, 12, 2, 6, 0, 0, 0, obsdl.JST)

	ds := partialCopy(t, full, 2026)
	manifest, _ := os.ReadFile(filepath.Join(ds, "manifest.json"))
	_, err := runMonthlyWith(t, ds, at, MonthlyOptions{}, truncated)
	if !errors.Is(err, ErrRowLoss) || !strings.Contains(err.Error(), "daily a0001 2026-10: 31 -> 15 rows") {
		t.Fatalf("err = %v, want ErrRowLoss for October", err)
	}
	r := &monthlyRun{dataset: ds}
	if !exists(ds, "daily/year=2026/2026-10.parquet") || r.count(t, "daily/year=2026/2026-10.parquet", "true") != 31*2 {
		t.Errorf("the October month file changed")
	}
	if after, _ := os.ReadFile(filepath.Join(ds, "manifest.json")); string(after) != string(manifest) {
		t.Errorf("manifest.json changed")
	}

	ds = partialCopy(t, full, 2026)
	if _, err := runMonthlyWith(t, ds, at, MonthlyOptions{AllowRowLoss: true}, truncated); err != nil {
		t.Fatalf("with AllowRowLoss: %v", err)
	}
	r = &monthlyRun{dataset: ds}
	if exists(ds, "daily/year=2026/2026-10.parquet") {
		t.Errorf("with AllowRowLoss October is still a month file")
	}
	if n := r.count(t, "daily/year=2026/data.parquet", "date >= DATE '2026-10-01'"); n != 15*2 {
		t.Errorf("with AllowRowLoss October has %d rows in the year file, want %d", n, 15*2)
	}
}
