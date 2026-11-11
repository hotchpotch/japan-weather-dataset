package export

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/hotchpotch/japan-weather-dataset/internal/obsdl"
)

// Manifest describes which dates the files of a dataset directory cover. It
// is small, so that the month command (and users) can check the state of
// the dataset without downloading the data.
type Manifest struct {
	UpdatedAt time.Time          `json:"updated_at"`
	Daily     *FrequencyManifest `json:"daily,omitempty"`
	Hourly    *FrequencyManifest `json:"hourly,omitempty"`
}

type FrequencyManifest struct {
	// LatestDate is the latest date with rows.
	LatestDate string `json:"latest_date"`
	// YearFilesThrough is the latest date in the year files (data.parquet);
	// later rows are in month files.
	YearFilesThrough string          `json:"year_files_through,omitempty"`
	MonthFiles       []MonthManifest `json:"month_files"`
}

type MonthManifest struct {
	Month     string `json:"month"` // YYYY-MM
	Path      string `json:"path"`  // relative to the dataset directory
	FirstDate string `json:"first_date"`
	LastDate  string `json:"last_date"`
	Rows      int    `json:"rows"`
	// MissingDays are the days from the first day of the month to LastDate
	// (to the end of the month for months before the latest one) without
	// any row.
	MissingDays []string `json:"missing_days"`
}

func (m *Manifest) For(f obsdl.Frequency) *FrequencyManifest {
	if f == obsdl.Hourly {
		return m.Hourly
	}
	return m.Daily
}

// ReadManifest reads manifest.json of a dataset directory; a missing file
// gives nil.
func ReadManifest(dir string) (*Manifest, error) {
	b, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var m Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("parse manifest.json: %w", err)
	}
	return &m, nil
}

// WriteManifest scans the data files of a dataset directory and writes
// manifest.json. With prev (the manifest of a partial copy of the dataset,
// e.g. only the current month files were downloaded), the entries of files
// that are not in the directory are kept from prev.
func WriteManifest(ctx context.Context, db *sql.DB, out string, now time.Time, prev *Manifest) error {
	m := Manifest{UpdatedAt: now.UTC().Truncate(time.Second)}
	for _, f := range []obsdl.Frequency{obsdl.Daily, obsdl.Hourly} {
		fm, err := frequencyManifest(ctx, db, out, f)
		if err != nil {
			return fmt.Errorf("manifest %s: %w", f, err)
		}
		if prev != nil {
			fm = keepAbsent(out, prev.For(f), fm)
		}
		if f == obsdl.Daily {
			m.Daily = fm
		} else {
			m.Hourly = fm
		}
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(out, "manifest.json.tmp")
	if err := os.WriteFile(tmp, append(b, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(out, "manifest.json"))
}

// keepAbsent adds the entries of prev for files that are not in out to cur.
func keepAbsent(out string, prev, cur *FrequencyManifest) *FrequencyManifest {
	if prev == nil {
		return cur
	}
	if cur == nil {
		cur = &FrequencyManifest{MonthFiles: []MonthManifest{}}
	}
	years, _ := filepath.Glob(filepath.Join(out, "*", "year=*", "data.parquet"))
	if cur.YearFilesThrough == "" && len(years) == 0 {
		cur.YearFilesThrough = prev.YearFilesThrough
	}
	have := map[string]bool{}
	latestMonth := ""
	for _, mm := range cur.MonthFiles {
		have[mm.Month] = true
		latestMonth = max(latestMonth, mm.Month)
	}
	// At most the two months before the latest one can still be month
	// files (three month files exist between the first daily run of a
	// month and the monthly run). Older entries are stale, e.g. from a
	// manifest downloaded before a monthly run merged that month.
	oldest := ""
	if t, err := time.Parse("2006-01", latestMonth); err == nil {
		oldest = t.AddDate(0, -2, 0).Format("2006-01")
	}
	for _, mm := range prev.MonthFiles {
		if have[mm.Month] || mm.Month < oldest {
			continue
		}
		if _, err := os.Stat(filepath.Join(out, filepath.FromSlash(mm.Path))); err == nil {
			continue
		}
		cur.MonthFiles = append(cur.MonthFiles, mm)
	}
	sort.Slice(cur.MonthFiles, func(i, j int) bool { return cur.MonthFiles[i].Month < cur.MonthFiles[j].Month })
	latest := cur.YearFilesThrough
	for _, mm := range cur.MonthFiles {
		if mm.LastDate > latest {
			latest = mm.LastDate
		}
	}
	cur.LatestDate = latest
	return cur
}

func frequencyManifest(ctx context.Context, db *sql.DB, out string, f obsdl.Frequency) (*FrequencyManifest, error) {
	years, err := filepath.Glob(filepath.Join(out, string(f), "year=*", "data.parquet"))
	if err != nil {
		return nil, err
	}
	months, err := monthFiles(out, f)
	if err != nil {
		return nil, err
	}
	if len(years) == 0 && len(months) == 0 {
		return nil, nil
	}
	fm := &FrequencyManifest{MonthFiles: []MonthManifest{}}
	var latest time.Time
	if len(years) > 0 {
		// The row group statistics answer this without reading the data.
		var t sql.NullTime
		if err := db.QueryRowContext(ctx, fmt.Sprintf("SELECT max(date) FROM read_parquet(%s, hive_partitioning = false)", list(years))).Scan(&t); err != nil {
			return nil, err
		}
		if t.Valid {
			latest = t.Time
			fm.YearFilesThrough = t.Time.Format(time.DateOnly)
		}
	}
	keys := make([]time.Time, 0, len(months))
	for k := range months {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].Before(keys[j]) })
	for i, k := range keys {
		mm, last, err := monthManifest(ctx, db, out, months[k], k, i == len(keys)-1)
		if err != nil {
			return nil, err
		}
		if last.After(latest) {
			latest = last
		}
		fm.MonthFiles = append(fm.MonthFiles, mm)
	}
	if !latest.IsZero() {
		fm.LatestDate = latest.Format(time.DateOnly)
	}
	return fm, nil
}

func list(paths []string) string {
	s := "["
	for i, p := range paths {
		if i > 0 {
			s += ", "
		}
		s += q(p)
	}
	return s + "]"
}

func monthManifest(ctx context.Context, db *sql.DB, out, path string, month time.Time, latestMonth bool) (MonthManifest, time.Time, error) {
	rel, err := filepath.Rel(out, path)
	if err != nil {
		return MonthManifest{}, time.Time{}, err
	}
	mm := MonthManifest{Month: month.Format("2006-01"), Path: filepath.ToSlash(rel), MissingDays: []string{}}
	rows, err := db.QueryContext(ctx, fmt.Sprintf("SELECT date, count(*) FROM read_parquet(%s, hive_partitioning = false) GROUP BY 1 ORDER BY 1", q(path)))
	if err != nil {
		return mm, time.Time{}, err
	}
	defer rows.Close()
	have := map[time.Time]bool{}
	var first, last time.Time
	for rows.Next() {
		var d time.Time
		var n int
		if err := rows.Scan(&d, &n); err != nil {
			return mm, time.Time{}, err
		}
		d = d.UTC()
		have[d] = true
		if first.IsZero() {
			first = d
		}
		last = d
		mm.Rows += n
	}
	if err := rows.Err(); err != nil {
		return mm, time.Time{}, err
	}
	if last.IsZero() {
		return mm, last, nil
	}
	mm.FirstDate, mm.LastDate = first.Format(time.DateOnly), last.Format(time.DateOnly)
	end := last
	if !latestMonth {
		end = month.AddDate(0, 1, -1)
	}
	for d := month; !d.After(end); d = d.AddDate(0, 0, 1) {
		if !have[d] {
			mm.MissingDays = append(mm.MissingDays, d.Format(time.DateOnly))
		}
	}
	return mm, last, nil
}
