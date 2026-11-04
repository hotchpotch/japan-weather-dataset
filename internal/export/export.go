// Package export builds the published dataset from the stored chunks:
//
//	stations/stations.parquet
//	daily/year=<YYYY>/data.parquet
//	hourly/year=<YYYY>/data.parquet
//	homogeneity_breaks/homogeneity_breaks.parquet
//	README.md   (Hugging Face dataset card)
package export

import (
	"bytes"
	"context"
	_ "embed"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
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
		years, err := e.years(ctx, f)
		if err != nil {
			return err
		}
		for _, y := range years {
			src := filepath.Join(e.Store.Dir, "chunks", string(f), strconv.Itoa(y), "*.parquet")
			dir := filepath.Join(e.Out, string(f), fmt.Sprintf("year=%d", y))
			dst := filepath.Join(dir, "data.parquet")
			files, _ := filepath.Glob(src)
			if len(files) == 0 {
				if err := os.RemoveAll(dir); err != nil {
					return err
				}
			} else {
				if err := os.MkdirAll(dir, 0o755); err != nil {
					return err
				}
				order := "station_id, date"
				if f == obsdl.Hourly {
					order += ", hour"
				}
				tmp := dst + ".tmp"
				sql := fmt.Sprintf(`COPY (SELECT * FROM read_parquet(%s, union_by_name = true) ORDER BY %s)
TO %s (FORMAT parquet, COMPRESSION zstd, ROW_GROUP_SIZE 100000)`, q(src), order, q(tmp))
				if _, err := db.ExecContext(ctx, sql); err != nil {
					return fmt.Errorf("export %s %d: %w", f, y, err)
				}
				if err := os.Rename(tmp, dst); err != nil {
					return err
				}
				e.Log.Info("exported", "frequency", f, "year", y, "files", len(files))
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
	return e.writeCard(ctx)
}

// years returns the years to export for a frequency.
func (e *Exporter) years(ctx context.Context, f obsdl.Frequency) ([]int, error) {
	if !e.All {
		return e.Store.DirtyYears(ctx, f)
	}
	entries, err := os.ReadDir(filepath.Join(e.Store.Dir, "chunks", string(f)))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []int
	for _, en := range entries {
		if y, err := strconv.Atoi(en.Name()); err == nil && en.IsDir() {
			out = append(out, y)
		}
	}
	dirty, err := e.Store.DirtyYears(ctx, f)
	if err != nil {
		return nil, err
	}
	return append(out, dirty...), nil
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
	return out
}

func (e *Exporter) yearSpan(ctx context.Context, f obsdl.Frequency) string {
	var lo, hi *int
	err := e.Store.DB.QueryRowContext(ctx, `SELECT min(year), max(year) FROM chunks WHERE frequency = ? AND rows > 0`, string(f)).Scan(&lo, &hi)
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
