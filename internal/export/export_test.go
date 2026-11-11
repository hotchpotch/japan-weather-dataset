package export

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/hotchpotch/japan-weather-dataset/internal/obsdl"
	"github.com/hotchpotch/japan-weather-dataset/internal/store"
)

func date(y, m, d int) time.Time { return time.Date(y, time.Month(m), d, 0, 0, 0, 0, time.UTC) }

func addDay(t *testing.T, s *store.Store, d time.Time) {
	t.Helper()
	r := make(obsdl.Record, len(store.Columns(obsdl.Daily)))
	r[0], r[1], r[2] = "s47662", d, 10.0
	at := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)
	if err := s.WriteChunk(context.Background(), obsdl.Daily, "s47662", d.Year(), d, d, []obsdl.Record{r}, nil,
		store.Fetch{RequestID: 1, FetchedAt: at, JMADownloadsAt: at}); err != nil {
		t.Fatal(err)
	}
}

// files lists the daily files of a dataset with their dates.
func files(t *testing.T, s *store.Store, out string) map[string]string {
	t.Helper()
	paths, _ := filepath.Glob(filepath.Join(out, "daily", "*", "*.parquet"))
	res := map[string]string{}
	for _, p := range paths {
		rows, err := s.DB.Query("SELECT strftime(date, '%m-%d') FROM read_parquet('" + p + "', hive_partitioning = false) ORDER BY date")
		if err != nil {
			t.Fatal(err)
		}
		var ds []string
		for rows.Next() {
			var d string
			rows.Scan(&d)
			ds = append(ds, d)
		}
		rows.Close()
		rel, _ := filepath.Rel(filepath.Join(out, "daily"), p)
		res[rel] = strings.Join(ds, ",")
	}
	return res
}

func check(t *testing.T, got map[string]string, want map[string]string) {
	t.Helper()
	keys := func(m map[string]string) []string {
		var k []string
		for x := range m {
			k = append(k, x+"="+m[x])
		}
		sort.Strings(k)
		return k
	}
	if g, w := strings.Join(keys(got), " "), strings.Join(keys(want), " "); g != w {
		t.Errorf("files:\n got  %s\n want %s", g, w)
	}
}

// The month of the latest date and the month before are in month files;
// when the latest date moves to a new month, the oldest month file goes
// into the year file.
func TestExportLayout(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	out := t.TempDir()
	ex := &Exporter{Store: s, Out: out, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	for _, d := range []time.Time{date(2025, 12, 31), date(2026, 1, 15), date(2026, 9, 30), date(2026, 10, 1), date(2026, 11, 5)} {
		addDay(t, s, d)
	}
	if err := ex.Run(ctx); err != nil {
		t.Fatal(err)
	}
	check(t, files(t, s, out), map[string]string{
		"year=2025/data.parquet":    "12-31",
		"year=2026/data.parquet":    "01-15,09-30",
		"year=2026/2026-10.parquet": "10-01",
		"year=2026/2026-11.parquet": "11-05",
	})
	m, err := ReadManifest(out)
	if err != nil || m == nil || m.Daily == nil {
		t.Fatalf("manifest %+v (%v)", m, err)
	}
	if m.Hourly != nil || m.Daily.LatestDate != "2026-11-05" || m.Daily.YearFilesThrough != "2026-09-30" || len(m.Daily.MonthFiles) != 2 {
		t.Fatalf("manifest daily = %+v", m.Daily)
	}
	oct, nov := m.Daily.MonthFiles[0], m.Daily.MonthFiles[1]
	if oct.Month != "2026-10" || oct.Path != "daily/year=2026/2026-10.parquet" || oct.Rows != 1 || len(oct.MissingDays) != 30 {
		t.Errorf("october = %+v", oct)
	}
	if nov.LastDate != "2026-11-05" || strings.Join(nov.MissingDays, ",") != "2026-11-01,2026-11-02,2026-11-03,2026-11-04" {
		t.Errorf("november = %+v", nov)
	}

	// December starts: October moves into the year file.
	addDay(t, s, date(2026, 12, 1))
	if err := ex.Run(ctx); err != nil {
		t.Fatal(err)
	}
	check(t, files(t, s, out), map[string]string{
		"year=2025/data.parquet":    "12-31",
		"year=2026/data.parquet":    "01-15,09-30,10-01",
		"year=2026/2026-11.parquet": "11-05",
		"year=2026/2026-12.parquet": "12-01",
	})

	// January: the recent months span two years.
	addDay(t, s, date(2027, 1, 2))
	if err := ex.Run(ctx); err != nil {
		t.Fatal(err)
	}
	check(t, files(t, s, out), map[string]string{
		"year=2025/data.parquet":    "12-31",
		"year=2026/data.parquet":    "01-15,09-30,10-01,11-05",
		"year=2026/2026-12.parquet": "12-01",
		"year=2027/2027-01.parquet": "01-02",
	})
	m, _ = ReadManifest(out)
	if m.Daily.LatestDate != "2027-01-02" || m.Daily.YearFilesThrough != "2026-11-05" || m.Daily.MonthFiles[0].Month != "2026-12" {
		t.Errorf("manifest daily = %+v", m.Daily)
	}
	if _, err := os.Stat(filepath.Join(out, "daily", "year=2027", "data.parquet")); !os.IsNotExist(err) {
		t.Errorf("year=2027 has no rows before the recent months; data.parquet must not exist (%v)", err)
	}
}

// The card credits the source and names who processed it (PDL 1.0, 1.1.2).
func TestCardCredit(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	out := t.TempDir()
	addDay(t, s, date(2026, 11, 5))
	ex := &Exporter{Store: s, Out: out, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if err := ex.Run(ctx); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(out, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	card := string(b)
	for _, want := range []string{
		"license: cc-by-4.0",
		"出典：気象庁ホームページ「過去の気象データ・ダウンロード」（https://www.data.jma.go.jp/risk/obsdl/）をもとに Yuichi Tateno (@hotchpotch) が加工して作成",
		"public_data_license_v1.0",
	} {
		if !strings.Contains(card, want) {
			t.Errorf("card lacks %q", want)
		}
	}
}
