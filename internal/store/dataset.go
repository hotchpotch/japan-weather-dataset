package store

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/hotchpotch/japan-weather-dataset/internal/obsdl"
)

// The functions below seed a fresh state directory from a published dataset
// (stations, homogeneity breaks, year and month files), so that recent
// months can be updated without the full crawl state (see the month and
// monthly commands).

// ImportStations loads stations and their coverage from a stations.parquet
// written by export. Stations without observed_categories were never probed
// and get no coverage rows.
func (s *Store) ImportStations(ctx context.Context, path string, now time.Time) error {
	src := fmt.Sprintf("read_parquet(%s, hive_partitioning = false)", sqlString(path))
	ts := tsLiteral(now)
	stmts := []string{
		fmt.Sprintf(`INSERT OR REPLACE INTO stations
SELECT station_id, station_type, name, name_kana, prefecture_code, prefecture_name, latitude, longitude,
       elevation_m, kansoku, ended_on, listed, %[2]s, %[2]s FROM %[1]s`, src, ts),
		fmt.Sprintf(`INSERT OR REPLACE INTO coverage
SELECT station_id, 'daily', daily_first_year, daily_last_year, observed_categories, %[2]s FROM %[1]s
WHERE observed_categories IS NOT NULL`, src, ts),
		fmt.Sprintf(`INSERT OR REPLACE INTO coverage
SELECT station_id, 'hourly', hourly_first_year, hourly_last_year, observed_categories, %[2]s FROM %[1]s
WHERE observed_categories IS NOT NULL`, src, ts),
	}
	for _, q := range stmts {
		if _, err := s.DB.ExecContext(ctx, q); err != nil {
			return fmt.Errorf("import stations from %s: %w", path, err)
		}
	}
	return nil
}

// ImportBreaks loads the homogeneity breaks of a homogeneity_breaks.parquet
// written by export. Fetches replace the breaks of the days they cover.
func (s *Store) ImportBreaks(ctx context.Context, path string) error {
	_, err := s.DB.ExecContext(ctx, fmt.Sprintf(`INSERT INTO breaks
SELECT station_id, frequency, element, date, hour FROM read_parquet(%s, hive_partitioning = false)`, sqlString(path)))
	if err != nil {
		return fmt.Errorf("import breaks from %s: %w", path, err)
	}
	return nil
}

// ImportRows splits exported files (rows of any stations and dates, with
// updated_at) into chunks, so that later fetches merge with these rows like
// with rows fetched before. The files of a year (its year file and month
// files) must be imported in one call: a chunk holds a station-year and is
// replaced by each call. It returns the number of rows.
func (s *Store) ImportRows(ctx context.Context, f obsdl.Frequency, paths ...string) (int, error) {
	if len(paths) == 0 {
		return 0, nil
	}
	list := ""
	for i, p := range paths {
		if i > 0 {
			list += ", "
		}
		list += sqlString(p)
	}
	conn, err := s.DB.Conn(ctx)
	if err != nil {
		return 0, err
	}
	defer conn.Close()
	table := "import_" + string(f)
	if _, err := conn.ExecContext(ctx, fmt.Sprintf(`CREATE OR REPLACE TEMP TABLE %s AS
SELECT * FROM read_parquet([%s], hive_partitioning = false, union_by_name = true)`, table, list)); err != nil {
		return 0, fmt.Errorf("read %s: %w", strings.Join(paths, ", "), err)
	}
	defer conn.ExecContext(context.WithoutCancel(ctx), "DROP TABLE IF EXISTS "+table)
	rows, err := conn.QueryContext(ctx, fmt.Sprintf(`SELECT station_id, year(date)::INTEGER, min(date), max(date), count(*)::INTEGER, max(updated_at)
FROM %s GROUP BY ALL ORDER BY ALL`, table))
	if err != nil {
		return 0, err
	}
	type part struct {
		station  string
		year     int
		from, to time.Time
		rows     int
		updated  time.Time
	}
	var parts []part
	for rows.Next() {
		var p part
		if err := rows.Scan(&p.station, &p.year, &p.from, &p.to, &p.rows, &p.updated); err != nil {
			rows.Close()
			return 0, err
		}
		parts = append(parts, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	total := 0
	for _, p := range parts {
		path := s.ChunkPath(f, p.station, p.year)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return 0, err
		}
		if _, err := conn.ExecContext(ctx, fmt.Sprintf(`COPY (SELECT * FROM %s WHERE station_id = %s AND year(date) = %d ORDER BY %s)
TO %s (FORMAT parquet, COMPRESSION zstd)`, table, sqlString(p.station), p.year, orderBy(f), sqlString(path))); err != nil {
			return 0, fmt.Errorf("write %s: %w", path, err)
		}
		if _, err := conn.ExecContext(ctx, `INSERT OR REPLACE INTO chunks VALUES (?, ?, ?, ?, ?, ?, ?, ?, 0)`,
			string(f), p.station, p.year, p.from, p.to, p.rows, p.updated, p.updated); err != nil {
			return 0, err
		}
		total += p.rows
	}
	return total, nil
}

// chunkList returns the chunk files of the years from..to as a DuckDB list
// literal, or "" when there are none.
func (s *Store) chunkList(f obsdl.Frequency, from, to time.Time) (string, error) {
	var files []string
	for y := from.Year(); y <= to.Year(); y++ {
		m, err := filepath.Glob(filepath.Join(s.Dir, "chunks", string(f), fmt.Sprint(y), "*.parquet"))
		if err != nil {
			return "", err
		}
		files = append(files, m...)
	}
	if len(files) == 0 {
		return "", nil
	}
	quoted := make([]string, len(files))
	for i, p := range files {
		quoted[i] = sqlString(p)
	}
	return "[" + strings.Join(quoted, ", ") + "]", nil
}

// Dates returns the dates in [from, to] that have at least one stored row.
func (s *Store) Dates(ctx context.Context, f obsdl.Frequency, from, to time.Time) (map[time.Time]bool, error) {
	out := map[time.Time]bool{}
	list, err := s.chunkList(f, from, to)
	if err != nil || list == "" {
		return out, err
	}
	rows, err := s.DB.QueryContext(ctx, fmt.Sprintf(`SELECT DISTINCT date FROM read_parquet(%s, hive_partitioning = false)
WHERE date BETWEEN ? AND ?`, list), from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var d time.Time
		if err := rows.Scan(&d); err != nil {
			return nil, err
		}
		out[d.UTC()] = true
	}
	return out, rows.Err()
}

// StationMonth identifies the rows of one station in one month (first day).
type StationMonth struct {
	Station string
	Month   time.Time
}

// MonthRows returns the number of stored rows per station and month for the
// dates in [from, to].
func (s *Store) MonthRows(ctx context.Context, f obsdl.Frequency, from, to time.Time) (map[StationMonth]int, error) {
	out := map[StationMonth]int{}
	list, err := s.chunkList(f, from, to)
	if err != nil || list == "" {
		return out, err
	}
	rows, err := s.DB.QueryContext(ctx, fmt.Sprintf(`SELECT station_id, CAST(date_trunc('month', date) AS DATE), count(*)
FROM read_parquet(%s, hive_partitioning = false) WHERE date BETWEEN ? AND ? GROUP BY ALL`, list), from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var k StationMonth
		var n int
		if err := rows.Scan(&k.Station, &k.Month, &n); err != nil {
			return nil, err
		}
		k.Month = k.Month.UTC()
		out[k] = n
	}
	return out, rows.Err()
}
