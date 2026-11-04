package store

import (
	"context"
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
