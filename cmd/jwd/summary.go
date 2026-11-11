package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/hotchpotch/japan-weather-dataset/internal/crawl"
	"github.com/hotchpotch/japan-weather-dataset/internal/export"
	"github.com/hotchpotch/japan-weather-dataset/internal/obsdl"
)

// runSummary is what month and monthly report at the end of a run in the
// job summary of GitHub Actions (GITHUB_STEP_SUMMARY).
type runSummary struct {
	cmd     string
	err     error
	dryRun  bool
	elapsed time.Duration
	stats   crawl.RunStats
	// events counts the access events that slowed or stopped the run.
	events   map[obsdl.AccessEventKind]int
	manifest *export.Manifest
}

// appendSummary appends the summary to the file named by
// GITHUB_STEP_SUMMARY, if set.
func appendSummary(s runSummary) error {
	path := os.Getenv("GITHUB_STEP_SUMMARY")
	if path == "" {
		return nil
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	writeSummary(f, s)
	return f.Close()
}

func writeSummary(w io.Writer, s runSummary) {
	result := "ok"
	switch {
	case errors.Is(s.err, context.Canceled):
		result = "interrupted"
	case s.err != nil:
		result = fmt.Sprintf("failed (exit code %d): %s", exitCode(s.err), oneLine(s.err.Error()))
	case s.dryRun:
		result = "ok (dry run: nothing requested or written)"
	}
	fmt.Fprintf(w, "### jwd %s\n\n| | |\n|---|---|\n", s.cmd)
	fmt.Fprintf(w, "| Result | %s |\n", result)
	fmt.Fprintf(w, "| Elapsed | %s |\n", s.elapsed.Round(time.Second))
	fmt.Fprintf(w, "| Data requests | %d (%d rows, %d conversion warnings) |\n", s.stats.Requests, s.stats.Rows, s.stats.Warnings)
	events := "none"
	if len(s.events) > 0 {
		var parts []string
		for k, n := range s.events {
			parts = append(parts, fmt.Sprintf("%s ×%d", k, n))
		}
		sort.Strings(parts)
		events = strings.Join(parts, ", ")
	}
	fmt.Fprintf(w, "| Access limits | %s |\n", events)
	if m := s.manifest; m != nil {
		for _, f := range []struct {
			name string
			fm   *export.FrequencyManifest
		}{{"daily", m.Daily}, {"hourly", m.Hourly}} {
			if f.fm != nil {
				fmt.Fprintf(w, "| %s | %s |\n", f.name, manifestLine(f.fm))
			}
		}
	}
	fmt.Fprintln(w)
}

// manifestLine describes the state of one frequency in the manifest.
func manifestLine(fm *export.FrequencyManifest) string {
	parts := []string{"latest " + fm.LatestDate}
	if fm.YearFilesThrough != "" {
		parts = append(parts, "year files through "+fm.YearFilesThrough)
	}
	for _, mf := range fm.MonthFiles {
		p := fmt.Sprintf("%s: %d rows", mf.Month, mf.Rows)
		if len(mf.MissingDays) > 0 {
			p += ", missing " + strings.Join(mf.MissingDays, " ")
		}
		parts = append(parts, p)
	}
	return strings.Join(parts, "; ")
}

func oneLine(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "\n", " "), "|", "\\|")
}
