// Package export builds the published dataset from the stored chunks:
//
//	stations/stations.parquet
//	daily/year=<YYYY>/data.parquet      rows before the recent months
//	daily/year=<YYYY>/<YYYY-MM>.parquet rows of a recent month
//	hourly/...                          the same for hourly rows
//	homogeneity_breaks/homogeneity_breaks.parquet
//	manifest.json  (dates covered by the files; see WriteManifest)
//	README.md      (Hugging Face dataset card)
//
// The recent months are the month of the day after the latest date and the
// month before (see RecentMonths). Their values may still be revised by JMA, and the
// month of the latest date is updated every day, possibly by the month
// command, which does not need the crawl state.
package export

import (
	"bytes"
	"context"
	"database/sql"
	_ "embed"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"text/template"
	"time"

	"github.com/hotchpotch/japan-weather-dataset/internal/obsdl"
	"github.com/hotchpotch/japan-weather-dataset/internal/store"
)

//go:embed card.md.tmpl
var cardTemplate string

type Exporter struct {
	Store *store.Store
	Out   string
	Log   *slog.Logger
	// All re-exports every year instead of the years changed since the last export.
	All bool
}

func q(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

func (e *Exporter) Run(ctx context.Context) error {
	db := e.Store.DB
	if err := os.MkdirAll(e.Out, 0o755); err != nil {
		return err
	}
	for _, f := range []obsdl.Frequency{obsdl.Daily, obsdl.Hourly} {
		latest, ok, err := e.Store.LastChunkDate(ctx, f)
		if err != nil {
			return err
		}
		var months []time.Time
		if ok {
			months = RecentMonths(latest)
		}
		years, err := e.years(ctx, f, months)
		if err != nil {
			return err
		}
		for _, y := range years {
			if err := e.exportYear(ctx, f, y, months); err != nil {
				return fmt.Errorf("export %s %d: %w", f, y, err)
			}
			if err := e.Store.ClearDirty(ctx, f, y); err != nil {
				return err
			}
		}
	}
	if err := e.exportStations(ctx); err != nil {
		return err
	}
	breaks := filepath.Join(e.Out, "homogeneity_breaks", "homogeneity_breaks.parquet")
	if err := os.MkdirAll(filepath.Dir(breaks), 0o755); err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx, fmt.Sprintf(`COPY (SELECT station_id, frequency, element, date, hour FROM breaks
ORDER BY station_id, frequency, element, date, hour) TO %s (FORMAT parquet, COMPRESSION zstd)`, q(breaks))); err != nil {
		return fmt.Errorf("export breaks: %w", err)
	}
	if err := e.writeCard(ctx); err != nil {
		return err
	}
	return WriteManifest(ctx, db, e.Out, time.Now(), nil)
}

// RecentMonths returns the month of the day after latest and the month
// before (first days). Their rows are written to month files; older rows to
// year files. Counting from the next day makes an export on the 1st (latest
// the last day of the month before) the same as one later in the month: on
// 12-01 and on 12-02 October goes into its year file. A month without rows
// gets no file (writeRows).
func RecentMonths(latest time.Time) []time.Time {
	next := latest.AddDate(0, 0, 1)
	cur := time.Date(next.Year(), next.Month(), 1, 0, 0, 0, 0, time.UTC)
	return []time.Time{cur.AddDate(0, -1, 0), cur}
}

// YearPath is the file of the rows of year y before the recent months.
func YearPath(out string, f obsdl.Frequency, y int) string {
	return filepath.Join(out, string(f), fmt.Sprintf("year=%d", y), "data.parquet")
}

// MonthPath is the file of the rows of month m (first day).
func MonthPath(out string, f obsdl.Frequency, m time.Time) string {
	return filepath.Join(out, string(f), fmt.Sprintf("year=%d", m.Year()), m.Format("2006-01")+".parquet")
}

// MonthFiles returns the month files of a frequency in a dataset directory.
func MonthFiles(out string, f obsdl.Frequency) (map[time.Time]string, error) {
	paths, err := filepath.Glob(filepath.Join(out, string(f), "year=*", "????-??.parquet"))
	if err != nil {
		return nil, err
	}
	res := map[time.Time]string{}
	for _, p := range paths {
		m, err := time.Parse("2006-01", strings.TrimSuffix(filepath.Base(p), ".parquet"))
		if err != nil {
			continue
		}
		res[m] = p
	}
	return res, nil
}

// YearFiles returns the files of year y of a frequency in a dataset
// directory: the year file, if any, and the month files of y.
func YearFiles(out string, f obsdl.Frequency, y int) ([]string, error) {
	var paths []string
	if _, err := os.Stat(YearPath(out, f, y)); err == nil {
		paths = append(paths, YearPath(out, f, y))
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	months, err := MonthFiles(out, f)
	if err != nil {
		return nil, err
	}
	var keys []time.Time
	for m := range months {
		if m.Year() == y {
			keys = append(keys, m)
		}
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].Before(keys[j]) })
	for _, m := range keys {
		paths = append(paths, months[m])
	}
	return paths, nil
}

// rowsSQL selects the rows of year y from the chunks with updated_at.
// Chunks written before updated_at existed take the fetch time of the chunk.
func (e *Exporter) rowsSQL(ctx context.Context, f obsdl.Frequency, y int) (string, error) {
	src := filepath.Join(e.Store.Dir, "chunks", string(f), strconv.Itoa(y), "*.parquet")
	var hasUpdated bool
	if err := e.Store.DB.QueryRowContext(ctx, fmt.Sprintf(`SELECT count(*) > 0 FROM (DESCRIBE SELECT * FROM read_parquet(%s, union_by_name = true)) WHERE column_name = 'updated_at'`,
		q(src))).Scan(&hasUpdated); err != nil {
		return "", err
	}
	cols, updated := "p.*", "CAST(c.fetched_at AS TIMESTAMPTZ)"
	if hasUpdated {
		cols, updated = "p.* EXCLUDE (updated_at)", "coalesce(p.updated_at, CAST(c.fetched_at AS TIMESTAMPTZ))"
	}
	return fmt.Sprintf(`SELECT %s, %s AS updated_at
FROM read_parquet(%s, union_by_name = true) p
LEFT JOIN chunks c ON c.frequency = %s AND c.year = %d AND c.station_id = p.station_id`, cols, updated, q(src), q(string(f)), y), nil
}

func orderBy(f obsdl.Frequency) string {
	if f == obsdl.Hourly {
		return "station_id, date, hour"
	}
	return "station_id, date"
}

// writeRows writes the rows of query matching where to dst, or removes dst
// when there are none. It returns the number of rows.
func writeRows(ctx context.Context, db *sql.DB, f obsdl.Frequency, query, where, dst string) (int, error) {
	var n int
	if err := db.QueryRowContext(ctx, fmt.Sprintf("SELECT count(*) FROM (%s) WHERE %s", query, where)).Scan(&n); err != nil {
		return 0, err
	}
	if n == 0 {
		if err := os.Remove(dst); err != nil && !os.IsNotExist(err) {
			return 0, err
		}
		return 0, nil
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return 0, err
	}
	tmp := dst + ".tmp"
	if _, err := db.ExecContext(ctx, fmt.Sprintf(`COPY (SELECT * FROM (%s) WHERE %s ORDER BY %s)
TO %s (FORMAT parquet, COMPRESSION zstd, ROW_GROUP_SIZE 100000)`, query, where, orderBy(f), q(tmp))); err != nil {
		return 0, err
	}
	return n, os.Rename(tmp, dst)
}

func dateLit(t time.Time) string { return "DATE " + q(t.Format(time.DateOnly)) }

// exportYear writes the year file of y and the month files of the recent
// months within y, and removes month files of y that are no longer recent.
func (e *Exporter) exportYear(ctx context.Context, f obsdl.Frequency, y int, months []time.Time) error {
	dir := filepath.Join(e.Out, string(f), fmt.Sprintf("year=%d", y))
	files, _ := filepath.Glob(filepath.Join(e.Store.Dir, "chunks", string(f), strconv.Itoa(y), "*.parquet"))
	if len(files) == 0 {
		return os.RemoveAll(dir)
	}
	query, err := e.rowsSQL(ctx, f, y)
	if err != nil {
		return err
	}
	where := "true"
	if len(months) > 0 {
		where = "date < " + dateLit(months[0])
	}
	n, err := writeRows(ctx, e.Store.DB, f, query, where, YearPath(e.Out, f, y))
	if err != nil {
		return err
	}
	parts := []any{"frequency", f, "year", y, "chunks", len(files), "rows", n}
	keep := map[time.Time]bool{}
	for _, m := range months {
		if m.Year() != y {
			continue
		}
		keep[m] = true
		mn, err := writeRows(ctx, e.Store.DB, f, query,
			fmt.Sprintf("date >= %s AND date < %s", dateLit(m), dateLit(m.AddDate(0, 1, 0))), MonthPath(e.Out, f, m))
		if err != nil {
			return err
		}
		parts = append(parts, m.Format("2006-01"), mn)
	}
	existing, err := MonthFiles(e.Out, f)
	if err != nil {
		return err
	}
	for m, p := range existing {
		if m.Year() == y && !keep[m] {
			if err := os.Remove(p); err != nil {
				return err
			}
		}
	}
	e.Log.Info("exported", parts...)
	return nil
}

// WriteMonth writes the month file of month m (first day) from the chunks,
// or removes it when there are no rows. It returns the number of rows.
func (e *Exporter) WriteMonth(ctx context.Context, f obsdl.Frequency, m time.Time) (int, error) {
	files, _ := filepath.Glob(filepath.Join(e.Store.Dir, "chunks", string(f), strconv.Itoa(m.Year()), "*.parquet"))
	dst := MonthPath(e.Out, f, m)
	if len(files) == 0 {
		if err := os.Remove(dst); err != nil && !os.IsNotExist(err) {
			return 0, err
		}
		return 0, nil
	}
	query, err := e.rowsSQL(ctx, f, m.Year())
	if err != nil {
		return 0, err
	}
	return writeRows(ctx, e.Store.DB, f, query,
		fmt.Sprintf("date >= %s AND date < %s", dateLit(m), dateLit(m.AddDate(0, 1, 0))), dst)
}

// years returns the years to export for a frequency: the years changed
// since the last export (all years with -all), the years of the recent
// months and the years of month files that are no longer recent.
func (e *Exporter) years(ctx context.Context, f obsdl.Frequency, months []time.Time) ([]int, error) {
	set := map[int]bool{}
	dirty, err := e.Store.DirtyYears(ctx, f)
	if err != nil {
		return nil, err
	}
	for _, y := range dirty {
		set[y] = true
	}
	if e.All {
		entries, err := os.ReadDir(filepath.Join(e.Store.Dir, "chunks", string(f)))
		if err != nil && !os.IsNotExist(err) {
			return nil, err
		}
		for _, en := range entries {
			if y, err := strconv.Atoi(en.Name()); err == nil && en.IsDir() {
				set[y] = true
			}
		}
	}
	recent := map[time.Time]bool{}
	for _, m := range months {
		recent[m] = true
		set[m.Year()] = true
	}
	existing, err := MonthFiles(e.Out, f)
	if err != nil {
		return nil, err
	}
	for m := range existing {
		if !recent[m] {
			set[m.Year()] = true
		}
	}
	out := make([]int, 0, len(set))
	for y := range set {
		out = append(out, y)
	}
	sort.Ints(out)
	return out, nil
}

func (e *Exporter) exportStations(ctx context.Context) error {
	path := filepath.Join(e.Out, "stations", "stations.parquet")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	sql := fmt.Sprintf(`COPY (
SELECT s.station_id, s.station_type, s.name, s.name_kana, s.prefecture_code, s.prefecture_name,
       s.latitude, s.longitude, s.elevation_m, s.kansoku, s.ended_on, s.listed,
       d.first_year AS daily_first_year, d.last_year AS daily_last_year,
       h.first_year AS hourly_first_year, h.last_year AS hourly_last_year,
       d.categories AS observed_categories
FROM stations s
LEFT JOIN coverage d ON d.station_id = s.station_id AND d.frequency = 'daily'
LEFT JOIN coverage h ON h.station_id = s.station_id AND h.frequency = 'hourly'
ORDER BY s.station_id) TO %s (FORMAT parquet, COMPRESSION zstd)`, q(path))
	_, err := e.Store.DB.ExecContext(ctx, sql)
	return err
}

type cardColumn struct{ Name, Type, Description string }

type cardData struct {
	GeneratedAt   string
	LatestDate    string
	Stations      int
	DailyColumns  []cardColumn
	HourlyColumns []cardColumn
	DailyYears    string
	HourlyYears   string
}

func columns(f obsdl.Frequency) []cardColumn {
	var out []cardColumn
	for _, c := range store.Columns(f) {
		out = append(out, cardColumn{c.Name, c.Type, strings.ReplaceAll(c.Description, "|", "/")})
	}
	return append(out, cardColumn{"updated_at", "TIMESTAMPTZ", store.UpdatedAtDescription})
}

// yearSpan is the range of years with data: from the chunks and from the
// probed coverage of the stations, so that a partial state (the monthly
// command loads only the recent years) still gives the whole range.
func (e *Exporter) yearSpan(ctx context.Context, f obsdl.Frequency) string {
	var lo, hi *int
	err := e.Store.DB.QueryRowContext(ctx, `SELECT min(y0), max(y1) FROM (
	SELECT year AS y0, year AS y1 FROM chunks WHERE frequency = $1 AND rows > 0
	UNION ALL
	SELECT first_year, last_year FROM coverage WHERE frequency = $1 AND first_year > 0)`, string(f)).Scan(&lo, &hi)
	if err != nil || lo == nil {
		return "-"
	}
	return fmt.Sprintf("%d-%d", *lo, *hi)
}

func (e *Exporter) writeCard(ctx context.Context) error {
	d := cardData{
		GeneratedAt:   time.Now().In(obsdl.JST).Format(time.RFC3339),
		DailyColumns:  columns(obsdl.Daily),
		HourlyColumns: columns(obsdl.Hourly),
		DailyYears:    e.yearSpan(ctx, obsdl.Daily),
		HourlyYears:   e.yearSpan(ctx, obsdl.Hourly),
	}
	if t, ok, err := e.Store.LastChunkDate(ctx, obsdl.Daily); err == nil && ok {
		d.LatestDate = t.Format(time.DateOnly)
	}
	if err := e.Store.DB.QueryRowContext(ctx, `SELECT count(*) FROM stations`).Scan(&d.Stations); err != nil {
		return err
	}
	tmpl, err := template.New("card").Parse(cardTemplate)
	if err != nil {
		return err
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, d); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(e.Out, "README.md"), buf.Bytes(), 0o644)
}
