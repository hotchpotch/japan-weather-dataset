package store

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/hotchpotch/japan-weather-dataset/internal/obsdl"
)

// Rows imported from an exported file merge with a refetch like rows that
// were fetched by the same state: unchanged rows keep updated_at.
func TestImportRowsKeepsUpdatedAt(t *testing.T) {
	ctx := context.Background()
	src, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	t1 := time.Date(2026, 11, 2, 3, 0, 0, 0, time.UTC)
	var recs []obsdl.Record
	for d := 1; d <= 5; d++ {
		recs = append(recs, record(day(11, d), float64(d)))
	}
	if err := src.WriteChunk(ctx, obsdl.Daily, "s47662", 2026, day(11, 1), day(11, 5), recs, nil, Fetch{RequestID: 1, FetchedAt: t1, JMADownloadsAt: t1}); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir() + "/year=2026" // a hive-style path must not add a year column
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	file := dir + "/2026-11.parquet"
	if _, err := src.DB.Exec("COPY (SELECT * FROM read_parquet('" + src.ChunkPath(obsdl.Daily, "s47662", 2026) + "')) TO '" + file + "' (FORMAT parquet)"); err != nil {
		t.Fatal(err)
	}

	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	n, err := s.ImportRows(ctx, obsdl.Daily, file)
	if err != nil || n != 5 {
		t.Fatalf("imported %d rows (%v)", n, err)
	}
	chunks, err := s.Chunks(ctx, obsdl.Daily)
	if err != nil {
		t.Fatal(err)
	}
	if c := chunks["s47662"][2026]; c.Rows != 5 || !c.From.Equal(day(11, 1)) || !c.To.Equal(day(11, 5)) {
		t.Errorf("chunk = %+v", c)
	}
	dates, err := s.Dates(ctx, obsdl.Daily, day(11, 1), day(11, 30))
	if err != nil || len(dates) != 5 || !dates[day(11, 3)] {
		t.Errorf("dates = %v (%v)", dates, err)
	}

	// Refetch days 4-6: day 4 unchanged, day 5 corrected, day 6 new.
	t2 := t1.Add(24 * time.Hour)
	recs = []obsdl.Record{record(day(11, 4), 4), record(day(11, 5), 50), record(day(11, 6), 6)}
	if err := s.WriteChunk(ctx, obsdl.Daily, "s47662", 2026, day(11, 4), day(11, 6), recs, nil, Fetch{RequestID: 2, FetchedAt: t2, JMADownloadsAt: t2}); err != nil {
		t.Fatal(err)
	}
	rows, err := s.DB.Query("SELECT day(date), updated_at FROM read_parquet('" + s.ChunkPath(obsdl.Daily, "s47662", 2026) + "') ORDER BY date")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	want := map[int]time.Time{1: t1, 2: t1, 3: t1, 4: t1, 5: t2, 6: t2}
	got := 0
	for rows.Next() {
		var d int
		var u time.Time
		if err := rows.Scan(&d, &u); err != nil {
			t.Fatal(err)
		}
		got++
		if !u.Equal(want[d]) {
			t.Errorf("day %d updated_at %v, want %v", d, u.UTC(), want[d])
		}
	}
	if got != 6 {
		t.Errorf("rows = %d, want 6", got)
	}
}
