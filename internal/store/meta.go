package store

import (
	"context"
	"database/sql"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/hotchpotch/japan-weather-dataset/internal/obsdl"
)

// StationRow is a station with its crawl state.
type StationRow struct {
	obsdl.Station
	Listed   bool
	Coverage map[obsdl.Frequency]*Coverage
}

type Coverage struct {
	FirstYear, LastYear int // 0 when no data was found
	Categories          map[obsdl.Category]bool
	ProbedAt            time.Time
}

// SaveStations upserts the current station list and marks stations missing
// from it as unlisted (they are kept, as their data stays valid).
func (s *Store) SaveStations(ctx context.Context, stations []obsdl.Station, now time.Time) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE stations SET listed = false`); err != nil {
		return err
	}
	for _, st := range stations {
		typ := "amedas"
		if st.Official() {
			typ = "official"
		}
		var ended any
		if st.EndedOn != nil {
			ended = *st.EndedOn
		}
		var elev any
		if st.ElevationM != nil {
			elev = *st.ElevationM
		}
		_, err := tx.ExecContext(ctx, `
INSERT INTO stations VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, true, ?, ?)
ON CONFLICT (station_id) DO UPDATE SET
	station_type = excluded.station_type, name = excluded.name, name_kana = excluded.name_kana,
	prefecture_code = excluded.prefecture_code, prefecture_name = excluded.prefecture_name,
	latitude = excluded.latitude, longitude = excluded.longitude, elevation_m = excluded.elevation_m,
	kansoku = excluded.kansoku, ended_on = excluded.ended_on, listed = true, last_listed_at = excluded.last_listed_at`,
			st.ID, typ, st.Name, st.Kana, st.PrefectureCode, st.PrefectureName, st.Latitude, st.Longitude,
			elev, st.Kansoku, ended, now, now)
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

func parseCategories(s string) map[obsdl.Category]bool {
	m := map[obsdl.Category]bool{}
	for _, name := range strings.Split(s, ",") {
		for c := obsdl.CatRain; c <= obsdl.CatOther; c++ {
			if c.String() == name {
				m[c] = true
			}
		}
	}
	return m
}

func formatCategories(m map[obsdl.Category]bool) string {
	var names []string
	for c := obsdl.CatRain; c <= obsdl.CatOther; c++ {
		if m[c] {
			names = append(names, c.String())
		}
	}
	return strings.Join(names, ",")
}

// Stations loads all stations with their coverage, sorted by id.
func (s *Store) Stations(ctx context.Context) ([]*StationRow, error) {
	rows, err := s.DB.QueryContext(ctx, `
SELECT station_id, name, coalesce(name_kana, ''), prefecture_code, prefecture_name, latitude, longitude,
       elevation_m, coalesce(kansoku, ''), ended_on, listed
FROM stations ORDER BY station_id`)
	if err != nil {
		return nil, err
	}
	byID := map[string]*StationRow{}
	var out []*StationRow
	for rows.Next() {
		r := &StationRow{Coverage: map[obsdl.Frequency]*Coverage{}}
		var elev sql.NullFloat64
		var ended sql.NullTime
		if err := rows.Scan(&r.ID, &r.Name, &r.Kana, &r.PrefectureCode, &r.PrefectureName, &r.Latitude, &r.Longitude,
			&elev, &r.Kansoku, &ended, &r.Listed); err != nil {
			rows.Close()
			return nil, err
		}
		if elev.Valid {
			r.ElevationM = &elev.Float64
		}
		if ended.Valid {
			t := ended.Time
			r.EndedOn = &t
		}
		byID[r.ID] = r
		out = append(out, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows, err = s.DB.QueryContext(ctx, `SELECT station_id, frequency, coalesce(first_year, 0), coalesce(last_year, 0), categories, probed_at FROM coverage`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, f, cats string
		c := &Coverage{}
		if err := rows.Scan(&id, &f, &c.FirstYear, &c.LastYear, &cats, &c.ProbedAt); err != nil {
			return nil, err
		}
		c.Categories = parseCategories(cats)
		if r := byID[id]; r != nil {
			r.Coverage[obsdl.Frequency(f)] = c
		}
	}
	return out, rows.Err()
}

func (s *Store) SaveCoverage(ctx context.Context, station string, f obsdl.Frequency, c Coverage) error {
	var first, last any
	if c.FirstYear > 0 {
		first, last = c.FirstYear, c.LastYear
	}
	_, err := s.DB.ExecContext(ctx, `INSERT OR REPLACE INTO coverage VALUES (?, ?, ?, ?, ?, ?)`,
		station, string(f), first, last, formatCategories(c.Categories), c.ProbedAt)
	return err
}

// Chunk is the stored state of one station-year.
type Chunk struct {
	Station   string
	Year      int
	From, To  time.Time
	Rows      int
	FetchedAt time.Time
}

// Chunks returns the chunks of a frequency keyed by station and year.
func (s *Store) Chunks(ctx context.Context, f obsdl.Frequency) (map[string]map[int]Chunk, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT station_id, year, covered_from, covered_to, rows, fetched_at FROM chunks WHERE frequency = ?`, string(f))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]map[int]Chunk{}
	for rows.Next() {
		var c Chunk
		if err := rows.Scan(&c.Station, &c.Year, &c.From, &c.To, &c.Rows, &c.FetchedAt); err != nil {
			return nil, err
		}
		if out[c.Station] == nil {
			out[c.Station] = map[int]Chunk{}
		}
		out[c.Station][c.Year] = c
	}
	return out, rows.Err()
}

// CountChunks returns the number of chunks of a frequency up to maxYear.
func (s *Store) CountChunks(ctx context.Context, f obsdl.Frequency, maxYear int) (int, error) {
	var n int
	err := s.DB.QueryRowContext(ctx, `SELECT count(*) FROM chunks WHERE frequency = ? AND year <= ?`, string(f), maxYear).Scan(&n)
	return n, err
}

// OldestChunks returns up to n chunks fetched before cutoff, oldest first.
func (s *Store) OldestChunks(ctx context.Context, f obsdl.Frequency, before time.Time, maxYear, n int) ([]Chunk, error) {
	rows, err := s.DB.QueryContext(ctx, `
SELECT station_id, year, covered_from, covered_to, rows, fetched_at FROM chunks
WHERE frequency = ? AND fetched_at < ? AND year <= ?
ORDER BY fetched_at, station_id, year LIMIT ?`, string(f), before, maxYear, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Chunk
	for rows.Next() {
		var c Chunk
		if err := rows.Scan(&c.Station, &c.Year, &c.From, &c.To, &c.Rows, &c.FetchedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// RequestLog is one row of the requests table.
type RequestLog struct {
	ID        int64
	StartedAt time.Time
	Duration  time.Duration
	Purpose   string
	Req       obsdl.TableRequest
	Status    string
	Bytes     int
	SHA256    string
	Warnings  int
	Err       error
}

func (s *Store) NextRequestID(ctx context.Context) (int64, error) {
	var id int64
	err := s.DB.QueryRowContext(ctx, `SELECT nextval('request_seq')`).Scan(&id)
	return id, err
}

func (s *Store) LogRequest(ctx context.Context, l RequestLog) error {
	ids := append([]string{}, l.Req.Stations...)
	sort.Strings(ids)
	var errText any
	if l.Err != nil {
		errText = l.Err.Error()
	}
	_, err := s.DB.ExecContext(ctx, `INSERT INTO requests VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		l.ID, l.StartedAt, l.Duration.Milliseconds(), l.Purpose, string(l.Req.Frequency), strings.Join(ids, ","),
		len(l.Req.Elements), l.Req.From, l.Req.To, l.Req.SameDaysEachYear, l.Req.Cells(), l.Status,
		l.Bytes, l.SHA256, l.Warnings, errText)
	return err
}

// DirtyYears returns the years whose chunks changed since the last export.
func (s *Store) DirtyYears(ctx context.Context, f obsdl.Frequency) ([]int, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT year FROM dirty WHERE frequency = ? ORDER BY year`, string(f))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int
	for rows.Next() {
		var y int
		if err := rows.Scan(&y); err != nil {
			return nil, err
		}
		out = append(out, y)
	}
	return out, rows.Err()
}

func (s *Store) ClearDirty(ctx context.Context, f obsdl.Frequency, year int) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM dirty WHERE frequency = ? AND year = ?`, string(f), year)
	return err
}

// LastChunkDate returns the latest covered date of a frequency, if any.
func (s *Store) LastChunkDate(ctx context.Context, f obsdl.Frequency) (time.Time, bool, error) {
	var t sql.NullTime
	err := s.DB.QueryRowContext(ctx, `SELECT max(covered_to) FROM chunks WHERE frequency = ?`, string(f)).Scan(&t)
	if errors.Is(err, sql.ErrNoRows) || !t.Valid {
		return time.Time{}, false, err
	}
	return t.Time, true, err
}
