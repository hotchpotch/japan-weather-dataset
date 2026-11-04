// Package store keeps the crawl state in a DuckDB file and the parsed
// observations as one Parquet file per (frequency, station, year).
//
// Layout of the data directory:
//
//	state.duckdb                         stations, coverage, chunks, requests, homogeneity breaks
//	chunks/<frequency>/<year>/<id>.parquet  observations of one station and year
//	raw/<frequency>/<yyyymmdd>/<request>.csv.gz  raw responses (optional)
package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/duckdb/duckdb-go/v2"

	"github.com/hotchpotch/japan-weather-dataset/internal/obsdl"
)

type Store struct {
	Dir string
	DB  *sql.DB
}

const schema = `
CREATE TABLE IF NOT EXISTS stations (
	station_id      VARCHAR PRIMARY KEY,
	station_type    VARCHAR NOT NULL,  -- official / amedas
	name            VARCHAR NOT NULL,
	name_kana       VARCHAR,
	prefecture_code VARCHAR NOT NULL,
	prefecture_name VARCHAR NOT NULL,
	latitude        DOUBLE NOT NULL,
	longitude       DOUBLE NOT NULL,
	elevation_m     DOUBLE,
	kansoku         VARCHAR,
	ended_on        DATE,
	listed          BOOLEAN NOT NULL,  -- present in the latest station list
	first_listed_at TIMESTAMP NOT NULL,
	last_listed_at  TIMESTAMP NOT NULL
);
CREATE TABLE IF NOT EXISTS coverage (
	station_id  VARCHAR NOT NULL,
	frequency   VARCHAR NOT NULL,
	first_year  INTEGER,             -- null: no data found by the probe
	last_year   INTEGER,
	categories  VARCHAR NOT NULL,    -- comma separated observed categories
	probed_at   TIMESTAMP NOT NULL,
	PRIMARY KEY (station_id, frequency)
);
CREATE TABLE IF NOT EXISTS chunks (
	frequency          VARCHAR NOT NULL,
	station_id         VARCHAR NOT NULL,
	year               INTEGER NOT NULL,
	covered_from       DATE NOT NULL,
	covered_to         DATE NOT NULL,
	rows               INTEGER NOT NULL,
	fetched_at         TIMESTAMP NOT NULL,
	jma_downloaded_at  TIMESTAMP NOT NULL,
	request_id         BIGINT NOT NULL,
	PRIMARY KEY (frequency, station_id, year)
);
CREATE SEQUENCE IF NOT EXISTS request_seq;
CREATE TABLE IF NOT EXISTS requests (
	request_id   BIGINT PRIMARY KEY,
	started_at   TIMESTAMP NOT NULL,
	duration_ms  BIGINT,
	purpose      VARCHAR NOT NULL,
	frequency    VARCHAR NOT NULL,
	stations     VARCHAR NOT NULL,
	elements     INTEGER NOT NULL,
	date_from    DATE NOT NULL,
	date_to      DATE NOT NULL,
	same_days    BOOLEAN NOT NULL,
	cells        INTEGER NOT NULL,
	status       VARCHAR NOT NULL,
	bytes        INTEGER,
	sha256       VARCHAR,
	warnings     INTEGER,
	error        VARCHAR
);
CREATE TABLE IF NOT EXISTS breaks (
	station_id VARCHAR NOT NULL,
	frequency  VARCHAR NOT NULL,
	element    VARCHAR NOT NULL,
	date       DATE NOT NULL,
	hour       UTINYINT NOT NULL
);
CREATE TABLE IF NOT EXISTS dirty (
	frequency VARCHAR NOT NULL,
	year      INTEGER NOT NULL,
	PRIMARY KEY (frequency, year)
);
`

func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	db, err := sql.Open("duckdb", filepath.Join(dir, "state.duckdb"))
	if err != nil {
		return nil, err
	}
	// DuckDB allows a single writer; keep everything on one connection.
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema + accessSchema); err != nil {
		db.Close()
		return nil, fmt.Errorf("init schema: %w", err)
	}
	s := &Store{Dir: dir, DB: db}
	for _, f := range []obsdl.Frequency{obsdl.Hourly, obsdl.Daily} {
		if _, err := db.Exec(fmt.Sprintf("CREATE OR REPLACE TABLE %s (%s)", stagingTable(f), columnDDL(f))); err != nil {
			db.Close()
			return nil, fmt.Errorf("create staging table: %w", err)
		}
	}
	return s, nil
}

func (s *Store) Close() error { return s.DB.Close() }

func stagingTable(f obsdl.Frequency) string { return "staging_" + string(f) }

func Columns(f obsdl.Frequency) []obsdl.Column {
	return append(obsdl.KeyColumns(f), obsdl.ValueColumns(f)...)
}

func columnDDL(f obsdl.Frequency) string {
	var parts []string
	for _, c := range Columns(f) {
		parts = append(parts, fmt.Sprintf("%q %s", c.Name, c.Type))
	}
	return strings.Join(parts, ", ")
}

func orderBy(f obsdl.Frequency) string {
	if f == obsdl.Hourly {
		return "station_id, date, hour"
	}
	return "station_id, date"
}

// ChunkPath is the Parquet file of one station and year.
func (s *Store) ChunkPath(f obsdl.Frequency, station string, year int) string {
	return filepath.Join(s.Dir, "chunks", string(f), fmt.Sprint(year), station+".parquet")
}

func sqlString(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

// Fetch describes where a set of rows came from.
type Fetch struct {
	RequestID      int64
	FetchedAt      time.Time
	JMADownloadsAt time.Time
}

// WriteChunk replaces the rows of [from, to] (dates within year) in the
// chunk of station/year with records, keeping rows outside the range.
// Homogeneity breaks of the range are replaced as well.
func (s *Store) WriteChunk(ctx context.Context, f obsdl.Frequency, station string, year int,
	from, to time.Time, records []obsdl.Record, breaks []obsdl.Break, fetch Fetch) error {
	conn, err := s.DB.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	staging := stagingTable(f)
	if _, err := conn.ExecContext(ctx, "DELETE FROM "+staging); err != nil {
		return err
	}
	if len(records) > 0 {
		err = conn.Raw(func(dc any) error {
			app, err := duckdb.NewAppenderFromConn(dc.(driver.Conn), "", staging)
			if err != nil {
				return err
			}
			for _, r := range records {
				vals := make([]driver.Value, len(r))
				for i, v := range r {
					vals[i] = v
				}
				if err := app.AppendRow(vals...); err != nil {
					app.Close()
					return err
				}
			}
			return app.Close()
		})
		if err != nil {
			return fmt.Errorf("append %s %s %d: %w", f, station, year, err)
		}
	}

	path := s.ChunkPath(f, station, year)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	query := "SELECT * FROM " + staging
	if _, err := os.Stat(path); err == nil {
		query += fmt.Sprintf(" UNION ALL BY NAME SELECT * FROM read_parquet(%s) WHERE date < DATE %s OR date > DATE %s",
			sqlString(path), sqlString(from.Format(time.DateOnly)), sqlString(to.Format(time.DateOnly)))
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	var rows int
	if err := conn.QueryRowContext(ctx, "SELECT count(*) FROM ("+query+")").Scan(&rows); err != nil {
		return fmt.Errorf("count %s: %w", path, err)
	}
	if rows == 0 {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	} else {
		tmp := path + ".tmp"
		copySQL := fmt.Sprintf("COPY (SELECT * FROM (%s) ORDER BY %s) TO %s (FORMAT parquet, COMPRESSION zstd)",
			query, orderBy(f), sqlString(tmp))
		if _, err := conn.ExecContext(ctx, copySQL); err != nil {
			return fmt.Errorf("write %s: %w", path, err)
		}
		if err := os.Rename(tmp, path); err != nil {
			return err
		}
	}

	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM breaks WHERE station_id = ? AND frequency = ? AND date BETWEEN ? AND ?`,
		station, string(f), from, to); err != nil {
		return err
	}
	for _, b := range breaks {
		if b.At.Date.Before(from) || b.At.Date.After(to) {
			continue
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO breaks VALUES (?, ?, ?, ?, ?)`,
			station, string(f), b.Element, b.At.Date, uint8(b.At.Hour)); err != nil {
			return err
		}
	}
	// Extend the covered range when the new range touches the old one.
	var oldFrom, oldTo time.Time
	err = tx.QueryRowContext(ctx, `SELECT covered_from, covered_to FROM chunks WHERE frequency = ? AND station_id = ? AND year = ?`,
		string(f), station, year).Scan(&oldFrom, &oldTo)
	newFrom, newTo := from, to
	switch {
	case err == nil:
		if !from.After(oldTo.AddDate(0, 0, 1)) && !to.Before(oldFrom.AddDate(0, 0, -1)) {
			if oldFrom.Before(newFrom) {
				newFrom = oldFrom
			}
			if oldTo.After(newTo) {
				newTo = oldTo
			}
		}
	case errors.Is(err, sql.ErrNoRows):
	default:
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO chunks VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		string(f), station, year, newFrom, newTo, rows, fetch.FetchedAt, fetch.JMADownloadsAt, fetch.RequestID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO dirty VALUES (?, ?)`, string(f), year); err != nil {
		return err
	}
	return tx.Commit()
}
