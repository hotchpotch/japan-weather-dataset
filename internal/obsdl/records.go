package obsdl

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Column is a dataset column with its DuckDB type.
type Column struct {
	Name        string
	Type        string
	Description string
}

// KeyColumns are the leading columns of every observation table.
func KeyColumns(f Frequency) []Column {
	cols := []Column{
		{"station_id", "VARCHAR", "obsdl station id: s + 5 digits (official station) or a + 4 digits (AMeDAS)"},
		{"date", "DATE", "JST calendar day the value belongs to"},
	}
	if f == Hourly {
		cols = append(cols,
			Column{"hour", "UTINYINT", "1..24; the value covers the hour ending at hour:00 JST (24 = midnight ending the day)"},
			Column{"datetime_jst", "TIMESTAMP", "end of the hour in JST (no time zone)"},
		)
	}
	return cols
}

// ValueColumns lists the columns produced for the elements of a frequency.
func ValueColumns(f Frequency) []Column {
	var cols []Column
	for _, e := range Elements(f) {
		desc := fmt.Sprintf("%s [%s] (%s)", e.Description, e.Unit, e.NameJa)
		switch e.Type {
		case Number:
			cols = append(cols, Column{e.Key, "DOUBLE", desc})
		case Direction:
			cols = append(cols, Column{e.Key, "UTINYINT", desc + "; 1=NNE .. 16=N, 0=calm"})
		case Code:
			cols = append(cols, Column{e.Key, "SMALLINT", desc})
		case Text:
			cols = append(cols, Column{e.Key, "VARCHAR", desc})
		}
		if e.AllowModifier {
			cols = append(cols, Column{e.Key + "_modifier", "VARCHAR", `"+" or "-" written after the value ("0+": more than 0, "10-": less than 10)`})
		}
		if e.HasNoPhenomenon {
			cols = append(cols, Column{e.Key + "_no_phenomenon", "BOOLEAN", "true when no phenomenon occurred (official stations only; null for AMeDAS)"})
		}
		cols = append(cols, Column{e.Key + "_quality", "UTINYINT", "JMA quality: 8 normal, 5 quasi-normal, 4 insufficient data, 2 questionable, 1 missing, 0 not observed"})
		if e.WithDirection {
			cols = append(cols,
				Column{e.Key + "_direction", "UTINYINT", "wind direction; 1=NNE .. 16=N, 0=calm"},
				Column{e.Key + "_direction_quality", "UTINYINT", "JMA quality of the direction"},
			)
		}
	}
	return cols
}

// Record is one row of an observation table: key columns followed by
// value columns, matching KeyColumns and ValueColumns.
type Record []any

// Break is a change of the JMA homogeneity number (均質番号) of an element:
// values from At onwards are not homogeneous with values before it.
type Break struct {
	Element string
	At      Stamp
}

// StationResult holds the converted rows of one station.
type StationResult struct {
	StationID string
	Name      string
	Records   []Record
	Stamps    []Stamp // stamps of Records
	Breaks    []Break
	Warnings  []string
}

// Convert turns the cells of station s into records for rows within
// [from, to] (inclusive dates). Rows where every element has quality 0
// (not observed) are dropped. Rows before from are only used to detect a
// homogeneity break at from.
func (t *Table) Convert(req TableRequest, s int, from, to time.Time) StationResult {
	res := StationResult{StationID: req.Stations[s], Name: t.StationNames[s]}
	all := Elements(req.Frequency)
	pos := map[string]int{}
	for i, e := range req.Elements {
		pos[e.Key] = i
	}
	prevHomog := make([]int, len(req.Elements))
	for r, stamp := range t.Stamps {
		cells := t.Cells[s][r]
		inRange := !stamp.Date.Before(from) && !stamp.Date.After(to)
		for i, c := range cells {
			if c.Homogeneity == 0 {
				continue
			}
			if prevHomog[i] != 0 && c.Homogeneity != prevHomog[i] && inRange {
				res.Breaks = append(res.Breaks, Break{Element: req.Elements[i].Key, At: stamp})
			}
			prevHomog[i] = c.Homogeneity
		}
		if !inRange {
			continue
		}
		observed := false
		for _, c := range cells {
			if c.Quality > 0 || (c.Quality < 0 && c.Value != "") {
				observed = true
				break
			}
		}
		if !observed {
			continue
		}
		rec := Record{res.StationID, stamp.Date}
		if req.Frequency == Hourly {
			rec = append(rec, uint8(stamp.Hour), stamp.Date.Add(time.Duration(stamp.Hour)*time.Hour))
		}
		for _, e := range all {
			i, ok := pos[e.Key]
			if !ok {
				rec = appendNulls(rec, e)
				continue
			}
			var warn string
			rec, warn = appendCell(rec, e, cells[i])
			if warn != "" {
				res.Warnings = append(res.Warnings, fmt.Sprintf("%s %s %v h%d: %s", res.StationID, e.Key, stamp.Date.Format(time.DateOnly), stamp.Hour, warn))
			}
		}
		res.Records = append(res.Records, rec)
		res.Stamps = append(res.Stamps, stamp)
	}
	return res
}

func nColumns(e Element) int {
	n := 2 // value, quality
	if e.AllowModifier {
		n++
	}
	if e.HasNoPhenomenon {
		n++
	}
	if e.WithDirection {
		n += 2
	}
	return n
}

func appendNulls(rec Record, e Element) Record {
	for range nColumns(e) {
		rec = append(rec, nil)
	}
	return rec
}

func flag(v int8) any {
	if v < 0 {
		return nil
	}
	return uint8(v)
}

func appendCell(rec Record, e Element, c Cell) (Record, string) {
	var warn string
	var value any
	modifier := ""
	v := c.Value
	if v != "" {
		switch e.Type {
		case Number:
			if e.AllowModifier && (strings.HasSuffix(v, "+") || strings.HasSuffix(v, "-")) {
				modifier = v[len(v)-1:]
				v = v[:len(v)-1]
			}
			f, err := strconv.ParseFloat(v, 64)
			if err != nil {
				warn = fmt.Sprintf("unparsable number %q", c.Value)
			} else {
				value = f
			}
		case Direction:
			if d, ok := windDirections[v]; ok {
				value = d
			} else {
				warn = fmt.Sprintf("unknown direction %q", v)
			}
		case Code:
			n, err := strconv.ParseInt(v, 10, 16)
			if err != nil {
				warn = fmt.Sprintf("unparsable code %q", v)
			} else {
				value = int16(n)
			}
		case Text:
			value = v
		}
	}
	rec = append(rec, value)
	if e.AllowModifier {
		if modifier == "" {
			rec = append(rec, nil)
		} else {
			rec = append(rec, modifier)
		}
	}
	if e.HasNoPhenomenon {
		switch c.NoPhenomenon {
		case 0:
			rec = append(rec, false)
		case 1:
			rec = append(rec, true)
		default:
			rec = append(rec, nil)
		}
	}
	rec = append(rec, flag(c.Quality))
	if e.WithDirection {
		var dir any
		if c.Direction != "" {
			if d, ok := windDirections[c.Direction]; ok {
				dir = d
			} else if warn == "" {
				warn = fmt.Sprintf("unknown direction %q", c.Direction)
			}
		}
		rec = append(rec, dir, flag(c.DirQuality))
	}
	return rec, warn
}
