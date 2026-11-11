package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hotchpotch/japan-weather-dataset/internal/crawl"
	"github.com/hotchpotch/japan-weather-dataset/internal/export"
	"github.com/hotchpotch/japan-weather-dataset/internal/obsdl"
)

func TestWriteSummary(t *testing.T) {
	var b strings.Builder
	writeSummary(&b, runSummary{
		cmd:     "month",
		err:     fmt.Errorf("days missing: %w", crawl.ErrMissingDays),
		elapsed: 95 * time.Second,
		stats:   crawl.RunStats{Requests: 3, Rows: 120, Warnings: 1},
		events:  map[obsdl.AccessEventKind]int{obsdl.EventRateLimited: 2},
		manifest: &export.Manifest{Daily: &export.FrequencyManifest{LatestDate: "2026-11-10", YearFilesThrough: "2026-09-30",
			MonthFiles: []export.MonthManifest{{Month: "2026-11", Rows: 12870, MissingDays: []string{"2026-11-03"}}}}},
	})
	got := b.String()
	for _, want := range []string{
		"### jwd month",
		"| Result | failed (exit code 7): days missing: ",
		"| Elapsed | 1m35s |",
		"| Data requests | 3 (120 rows, 1 conversion warnings) |",
		"| Access limits | " + string(obsdl.EventRateLimited) + " ×2 |",
		"| daily | latest 2026-11-10; year files through 2026-09-30; 2026-11: 12870 rows, missing 2026-11-03 |",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("summary misses %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "hourly") {
		t.Errorf("summary has a line for the missing hourly manifest:\n%s", got)
	}
}

func TestAppendSummary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "summary.md")
	if err := os.WriteFile(path, []byte("before\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GITHUB_STEP_SUMMARY", path)
	if err := appendSummary(runSummary{cmd: "monthly", dryRun: true}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	if !strings.HasPrefix(string(b), "before\n### jwd monthly") || !strings.Contains(string(b), "dry run") {
		t.Errorf("summary file = %q", b)
	}
	t.Setenv("GITHUB_STEP_SUMMARY", "")
	if err := appendSummary(runSummary{cmd: "monthly"}); err != nil {
		t.Errorf("without GITHUB_STEP_SUMMARY: %v", err)
	}
}
