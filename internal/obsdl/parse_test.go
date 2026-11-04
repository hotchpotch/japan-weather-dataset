package obsdl

import (
	"errors"
	"os"
	"testing"
	"time"
)

func day(y, m, d int) time.Time { return time.Date(y, time.Month(m), d, 0, 0, 0, 0, time.UTC) }

func mustRead(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// value returns the record value of a column.
func value(t *testing.T, f Frequency, rec Record, name string) any {
	t.Helper()
	cols := append(KeyColumns(f), ValueColumns(f)...)
	if len(cols) != len(rec) {
		t.Fatalf("record has %d values, schema has %d columns", len(rec), len(cols))
	}
	for i, c := range cols {
		if c.Name == name {
			return rec[i]
		}
	}
	t.Fatalf("no column %q", name)
	return nil
}

func TestParseDaily(t *testing.T) {
	req := TableRequest{Frequency: Daily, Stations: []string{"s47662", "a0371"}, Elements: DailyElements,
		From: day(2026, 10, 1), To: day(2026, 10, 3)}
	tab, err := ParseTable(mustRead(t, "daily_s47662_a0371_20261001-03.csv"), req)
	if err != nil {
		t.Fatal(err)
	}
	if got := tab.DownloadedAt.Format(time.RFC3339); got != "2026-11-04T15:13:22+09:00" {
		t.Errorf("DownloadedAt = %s", got)
	}
	if len(tab.Stamps) != 3 || tab.StationNames[0] != "東京" || tab.StationNames[1] != "羽田" {
		t.Fatalf("stamps=%d names=%v", len(tab.Stamps), tab.StationNames)
	}

	tokyo := tab.Convert(req, 0, req.From, req.To)
	if len(tokyo.Records) != 3 || len(tokyo.Warnings) != 0 {
		t.Fatalf("tokyo records=%d warnings=%v", len(tokyo.Records), tokyo.Warnings)
	}
	r := tokyo.Records[0]
	checks := map[string]any{
		"station_id":                        "s47662",
		"date":                              day(2026, 10, 1),
		"temperature_mean":                  23.0,
		"temperature_mean_quality":          uint8(8),
		"precipitation_total":               0.0,
		"precipitation_total_no_phenomenon": false,
		"precipitation_max_1h":              0.5,
		"wind_speed_max":                    4.0,
		"wind_speed_max_direction":          uint8(7), // 南南東
		"wind_gust_max_direction":           uint8(2), // 北東
		"wind_direction_mode":               uint8(16),
		"weather_day":                       "晴一時雨",
		"cloud_cover_mean":                  4.0,
	}
	for k, want := range checks {
		if got := value(t, Daily, r, k); got != want {
			t.Errorf("tokyo %s = %#v, want %#v", k, got, want)
		}
	}
	if got := value(t, Daily, tokyo.Records[2], "precipitation_total_no_phenomenon"); got != true {
		t.Errorf("tokyo 10/3 no_phenomenon = %#v", got)
	}

	haneda := tab.Convert(req, 1, req.From, req.To)
	h := haneda.Records[0]
	if got := value(t, Daily, h, "precipitation_total"); got != 0.5 {
		t.Errorf("haneda precipitation = %#v", got)
	}
	if got := value(t, Daily, h, "precipitation_total_no_phenomenon"); got != nil {
		t.Errorf("AMeDAS has no no-phenomenon flag, got %#v", got)
	}
	if got := value(t, Daily, h, "snow_depth_max"); got != nil {
		t.Errorf("unobserved snow depth = %#v", got)
	}
	if got := value(t, Daily, h, "snow_depth_max_quality"); got != uint8(0) {
		t.Errorf("unobserved snow depth quality = %#v", got)
	}
}

func TestParseHourly(t *testing.T) {
	req := TableRequest{Frequency: Hourly, Stations: []string{"s47662"}, Elements: HourlyElements,
		From: day(2026, 10, 1), To: day(2026, 10, 2)}
	tab, err := ParseTable(mustRead(t, "hourly_s47662_20261001-02.csv"), req)
	if err != nil {
		t.Fatal(err)
	}
	if len(tab.Stamps) != 48 {
		t.Fatalf("stamps = %d", len(tab.Stamps))
	}
	if s := tab.Stamps[23]; s.Date != day(2026, 10, 1) || s.Hour != 24 {
		t.Errorf("midnight row = %+v", s)
	}
	res := tab.Convert(req, 0, day(2026, 10, 2), day(2026, 10, 2))
	if len(res.Records) != 24 {
		t.Fatalf("records = %d", len(res.Records))
	}
	if got := value(t, Hourly, res.Records[0], "datetime_jst"); got != time.Date(2026, 10, 2, 1, 0, 0, 0, time.UTC) {
		t.Errorf("datetime = %v", got)
	}
	all := tab.Convert(req, 0, req.From, req.To)
	r3 := all.Records[2] // 2026/10/1 3:00
	checks := map[string]any{
		"hour":                        uint8(3),
		"temperature":                 17.8,
		"wind_speed":                  2.5,
		"wind_speed_direction":        uint8(1), // 北北東
		"weather":                     int16(4),
		"cloud_cover":                 10.0,
		"cloud_cover_modifier":        "-",
		"visibility":                  15.0,
		"precipitation_no_phenomenon": false,
	}
	for k, want := range checks {
		if got := value(t, Hourly, r3, k); got != want {
			t.Errorf("%s = %#v, want %#v", k, got, want)
		}
	}
	if got := value(t, Hourly, all.Records[23], "datetime_jst"); got != time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC) {
		t.Errorf("hour 24 datetime = %v", got)
	}
}

func TestParseRejectsWrongElementOrder(t *testing.T) {
	els := append([]Element{}, HourlyElements...)
	els[0], els[1] = els[1], els[0]
	req := TableRequest{Frequency: Hourly, Stations: []string{"s47662"}, Elements: els,
		From: day(2026, 10, 1), To: day(2026, 10, 2)}
	if _, err := ParseTable(mustRead(t, "hourly_s47662_20261001-02.csv"), req); err == nil {
		t.Fatal("expected an error for swapped elements")
	}
}

func TestClassifyErrorPage(t *testing.T) {
	if err := classifyErrorPage(mustRead(t, "error_too_large.html")); !errors.Is(err, ErrTooLarge) {
		t.Errorf("too large page: %v", err)
	}
	if err := classifyErrorPage(mustRead(t, "error_rejected.html")); !errors.Is(err, ErrRejected) {
		t.Errorf("rejected page: %v", err)
	}
}

func TestCells(t *testing.T) {
	r := TableRequest{Frequency: Hourly, Stations: []string{"s47662"}, Elements: HourlyElements,
		From: day(2024, 1, 1), To: day(2025, 12, 31)}
	if got := r.Cells(); got != 731*24*15 {
		t.Errorf("cells = %d", got)
	}
	r.SameDaysEachYear = true
	r.To = day(2025, 1, 1)
	if got := r.Cells(); got != 2*24*15 {
		t.Errorf("same-days cells = %d", got)
	}
}
