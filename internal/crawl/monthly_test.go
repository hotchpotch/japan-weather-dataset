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
	} {
		if got := monthlyFrom(c.yesterday); !got.Equal(c.want) {
			t.Errorf("monthlyFrom(%v) = %v, want %v", c.yesterday, got, c.want)
		}
	}
}

type monthlyFixture struct {
	c        *Crawler
	requests []obsdl.TableRequest
	now      time.Time
	// fail, when set, answers the requests of a purpose with an error.
	fail map[string]error
}

// newMonthlyFixture has two rain-only stations with daily data 2020-2025,
// fetched in January 2026.
func newMonthlyFixture(t *testing.T) *monthlyFixture {
	t.Helper()
	ctx := context.Background()
	s, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	probed := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	rain := map[obsdl.Category]bool{obsdl.CatRain: true}
	var sts []obsdl.Station
	for _, id := range []string{"a0001", "a0002"} {
		sts = append(sts, obsdl.Station{ID: id, Name: id, PrefectureCode: "44", PrefectureName: "東京", Kansoku: "100000"})
	}
	if err := s.SaveStations(ctx, sts, probed); err != nil {
		t.Fatal(err)
	}
	for _, st := range sts {
		if err := s.SaveCoverage(ctx, st.ID, obsdl.Daily, store.Coverage{FirstYear: 2020, LastYear: 2026, Categories: rain, ProbedAt: probed}); err != nil {
			t.Fatal(err)
		}
		if err := s.SaveCoverage(ctx, st.ID, obsdl.Hourly, store.Coverage{Categories: rain, ProbedAt: probed}); err != nil {
			t.Fatal(err)
		}
	}
	fx := &monthlyFixture{}
	fx.c = &Crawler{Store: s, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Budget: 100_000, MaxStations: 50,
		Now: func() time.Time { return fx.now }}
	fx.c.fetchFunc = func(_ context.Context, purpose string, req obsdl.TableRequest) (*obsdl.Table, int64, error) {
		if err := fx.fail[purpose]; err != nil {
			return nil, 0, err
		}
		fx.requests = append(fx.requests, req)
		return fakeTable(req, func(time.Time) string { return "1" }), int64(len(fx.requests)), nil
	}
	fx.now = time.Date(2026, 1, 2, 6, 0, 0, 0, obsdl.JST)
	if err := fx.c.Backfill(ctx, BackfillOptions{Frequency: obsdl.Daily, FromYear: 2020, ToYear: 2025}); err != nil {
		t.Fatal(err)
	}
	fx.requests = nil
	return fx
}

func TestMonthly(t *testing.T) {
	ctx := context.Background()
	fx := newMonthlyFixture(t)
	c := fx.c
	fx.now = time.Date(2026, 12, 2, 6, 0, 0, 0, obsdl.JST)
	out := t.TempDir()
	if err := c.Monthly(ctx, MonthlyOptions{Out: out, RefreshCycleMonths: 4, RefreshAge: 30 * 24 * time.Hour, SkipStations: true}); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, r := range fx.requests {
		got = append(got, r.From.Format(time.DateOnly)+".."+r.To.Format(time.DateOnly)+" "+strings.Join(r.Stations, ","))
	}
	// October 1 (with the day before) to yesterday for both stations; then
	// 12 station-years / 4 = the 3 fetched longest ago.
	want := "2026-09-30..2026-12-01 a0001,a0002 " +
		"2019-12-31..2020-12-31 a0001 2020-12-31..2021-12-31 a0001 2021-12-31..2022-12-31 a0001"
	if strings.Join(got, " ") != want {
		t.Errorf("requests:\n got  %s\n want %s", strings.Join(got, " "), want)
	}
	for _, p := range []string{"daily/year=2026/data.parquet", "daily/year=2026/2026-11.parquet", "daily/year=2026/2026-12.parquet", "daily/year=2020/data.parquet"} {
		if _, err := os.Stat(filepath.Join(out, p)); err != nil {
			t.Errorf("missing %s: %v", p, err)
		}
	}
	m, err := export.ReadManifest(out)
	if err != nil || m.Daily.YearFilesThrough != "2026-10-31" || m.Daily.LatestDate != "2026-12-01" {
		t.Errorf("manifest = %+v (%v)", m.Daily, err)
	}
	// The next run refreshes the next station-years.
	fx.requests = nil
	fx.now = time.Date(2027, 1, 2, 6, 0, 0, 0, obsdl.JST)
	if err := c.Monthly(ctx, MonthlyOptions{Out: out, RefreshCycleMonths: 4, RefreshAge: 30 * 24 * time.Hour, SkipStations: true}); err != nil {
		t.Fatal(err)
	}
	got = nil
	for _, r := range fx.requests[1:] {
		got = append(got, r.To.Format("2006")+" "+strings.Join(r.Stations, ","))
	}
	// 14 station-years up to 2026 now (2026 itself was fetched last month);
	// 4 of them, the oldest first (requests ordered by year).
	if strings.Join(got, " ") != "2020 a0002 2023 a0001 2024 a0001 2025 a0001" {
		t.Errorf("second refresh: %v", got)
	}
}

// A failure while refreshing old years keeps the recent months exported.
func TestMonthlyRefreshFailure(t *testing.T) {
	fx := newMonthlyFixture(t)
	fx.now = time.Date(2026, 12, 2, 6, 0, 0, 0, obsdl.JST)
	fx.fail = map[string]error{"refresh": obsdl.ErrBlocked}
	out := t.TempDir()
	err := fx.c.Monthly(context.Background(), MonthlyOptions{Out: out, RefreshCycleMonths: 4, RefreshAge: 30 * 24 * time.Hour, SkipStations: true})
	if !errors.Is(err, obsdl.ErrBlocked) {
		t.Fatalf("err = %v, want blocked", err)
	}
	m, err := export.ReadManifest(out)
	if err != nil || m == nil || m.Daily.LatestDate != "2026-12-01" {
		t.Fatalf("recent months not exported: %+v (%v)", m, err)
	}
}
