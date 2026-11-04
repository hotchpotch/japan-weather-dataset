package store

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/hotchpotch/japan-weather-dataset/internal/obsdl"
)

func day(m, d int) time.Time { return time.Date(2026, time.Month(m), d, 0, 0, 0, 0, time.UTC) }

func record(date time.Time, temp float64) obsdl.Record {
	n := len(obsdl.KeyColumns(obsdl.Daily)) + len(obsdl.ValueColumns(obsdl.Daily))
	r := make(obsdl.Record, n)
	r[0], r[1], r[2] = "s47662", date, temp
	return r
}

func TestWriteChunkMerge(t *testing.T) {
	ctx := context.Background()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	fetch := Fetch{RequestID: 1, FetchedAt: time.Now(), JMADownloadsAt: time.Now()}

	var recs []obsdl.Record
	for d := 1; d <= 10; d++ {
		recs = append(recs, record(day(1, d), float64(d)))
	}
	if err := s.WriteChunk(ctx, obsdl.Daily, "s47662", 2026, day(1, 1), day(1, 10), recs, nil, fetch); err != nil {
		t.Fatal(err)
	}
	// Refetch days 8..12: days 8-10 are replaced, 11-12 added, day 9 disappears.
	recs = []obsdl.Record{record(day(1, 8), 80), record(day(1, 10), 100), record(day(1, 11), 110), record(day(1, 12), 120)}
	breaks := []obsdl.Break{{Element: "temperature_mean", At: obsdl.Stamp{Date: day(1, 11)}}}
	if err := s.WriteChunk(ctx, obsdl.Daily, "s47662", 2026, day(1, 8), day(1, 12), recs, breaks, fetch); err != nil {
		t.Fatal(err)
	}

	var n int
	var sum float64
	q := "SELECT count(*), sum(temperature_mean) FROM read_parquet('" + s.ChunkPath(obsdl.Daily, "s47662", 2026) + "')"
	if err := s.DB.QueryRow(q).Scan(&n, &sum); err != nil {
		t.Fatal(err)
	}
	if want := 1 + 2 + 3 + 4 + 5 + 6 + 7 + 80 + 100 + 110 + 120.0; n != 11 || sum != want {
		t.Errorf("rows=%d sum=%v, want 11 rows sum %v", n, sum, want)
	}
	chunks, err := s.Chunks(ctx, obsdl.Daily)
	if err != nil {
		t.Fatal(err)
	}
	c := chunks["s47662"][2026]
	if !c.From.Equal(day(1, 1)) || !c.To.Equal(day(1, 12)) || c.Rows != 11 {
		t.Errorf("chunk = %+v", c)
	}
	var nb int
	if err := s.DB.QueryRow("SELECT count(*) FROM breaks").Scan(&nb); err != nil || nb != 1 {
		t.Errorf("breaks = %d (%v)", nb, err)
	}
	years, err := s.DirtyYears(ctx, obsdl.Daily)
	if err != nil || len(years) != 1 || years[0] != 2026 {
		t.Errorf("dirty = %v (%v)", years, err)
	}
}

func TestWriteChunkUpdatedAt(t *testing.T) {
	ctx := context.Background()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	path := s.ChunkPath(obsdl.Daily, "s47662", 2026)
	t1 := time.Date(2026, 11, 1, 3, 0, 0, 0, time.UTC)
	at := func(h int) Fetch {
		return Fetch{RequestID: int64(h), FetchedAt: t1.Add(time.Duration(h) * time.Hour), JMADownloadsAt: t1}
	}
	write := func(from, to int, f Fetch, temp func(d int) float64) {
		t.Helper()
		var recs []obsdl.Record
		for d := from; d <= to; d++ {
			recs = append(recs, record(day(1, d), temp(d)))
		}
		if err := s.WriteChunk(ctx, obsdl.Daily, "s47662", 2026, day(1, from), day(1, to), recs, nil, f); err != nil {
			t.Fatal(err)
		}
	}
	updated := func() map[int]time.Time {
		t.Helper()
		rows, err := s.DB.Query("SELECT day(date), updated_at FROM read_parquet('" + path + "') ORDER BY date")
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		out := map[int]time.Time{}
		for rows.Next() {
			var d int
			var u time.Time
			if err := rows.Scan(&d, &u); err != nil {
				t.Fatal(err)
			}
			out[d] = u.UTC()
		}
		return out
	}
	dirty := func() bool {
		t.Helper()
		years, err := s.DirtyYears(ctx, obsdl.Daily)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.ClearDirty(ctx, obsdl.Daily, 2026); err != nil {
			t.Fatal(err)
		}
		return len(years) > 0
	}
	same := func(d int) float64 { return float64(d) }

	write(1, 10, at(0), same)
	if !dirty() {
		t.Error("first write not dirty")
	}
	// Same values again: nothing changes, the year stays clean.
	write(1, 10, at(1), same)
	if dirty() {
		t.Error("unchanged refetch marked dirty")
	}
	for d, u := range updated() {
		if !u.Equal(t1) {
			t.Errorf("day %d updated_at %v, want %v", d, u, t1)
		}
	}
	// Day 5 corrected, day 6 the same.
	write(5, 6, at(2), func(d int) float64 {
		if d == 5 {
			return 55
		}
		return same(d)
	})
	if !dirty() {
		t.Error("corrected refetch not dirty")
	}
	u := updated()
	if !u[5].Equal(at(2).FetchedAt) || !u[6].Equal(t1) || !u[4].Equal(t1) {
		t.Errorf("updated_at after correction: 4=%v 5=%v 6=%v", u[4], u[5], u[6])
	}

	// A chunk written before updated_at existed keeps the chunk's fetch
	// time on an unchanged refetch.
	legacy := path + ".legacy"
	if _, err := s.DB.Exec("COPY (SELECT * EXCLUDE (updated_at) FROM read_parquet('" + path + "')) TO '" + legacy + "' (FORMAT parquet)"); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(legacy, path); err != nil {
		t.Fatal(err)
	}
	write(1, 10, at(3), func(d int) float64 {
		if d == 5 {
			return 55
		}
		return same(d)
	})
	if dirty() {
		t.Error("unchanged refetch of a legacy chunk marked dirty")
	}
	for d, u := range updated() {
		if !u.Equal(at(2).FetchedAt) {
			t.Errorf("legacy day %d updated_at %v, want chunk fetch time %v", d, u, at(2).FetchedAt)
		}
	}
}
