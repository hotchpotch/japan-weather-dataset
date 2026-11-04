package obsdl

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"

	"golang.org/x/text/encoding/japanese"
	"golang.org/x/text/transform"
)

// JST is Japan Standard Time. obsdl timestamps are JST without offsets.
var JST = time.FixedZone("JST", 9*60*60)

// Cell holds the raw columns of one element for one station and time.
// Missing attribute columns are -1.
type Cell struct {
	Value        string
	NoPhenomenon int8 // 現象なし情報: 1 = no phenomenon, 0 = phenomenon
	Quality      int8 // 品質情報: 8 normal, 5 quasi-normal, 4 insufficient, 2 questionable, 1 missing, 0 not observed
	Direction    string
	DirQuality   int8
	Homogeneity  int // 均質番号; 0 when absent
}

// Stamp identifies a row. For hourly data Hour is 1..24 and the value covers
// the hour ending at Hour:00 JST of Date (hour 24 is midnight at the end of
// Date). For daily data Hour is 0.
type Stamp struct {
	Date time.Time // midnight UTC of the JST calendar day
	Hour int
}

// Time returns the end of the period in JST (the start of the day for daily rows).
func (s Stamp) Time() time.Time {
	return time.Date(s.Date.Year(), s.Date.Month(), s.Date.Day(), s.Hour, 0, 0, 0, JST)
}

// Table is a parsed obsdl CSV.
type Table struct {
	DownloadedAt time.Time
	Stamps       []Stamp
	// StationNames as written in the header, in request order.
	StationNames []string
	// Cells[station][row][element]
	Cells [][][]Cell
}

func decodeCP932(b []byte) ([]byte, error) {
	out, _, err := transform.Bytes(japanese.ShiftJIS.NewDecoder(), b)
	return out, err
}

var reDate = regexp.MustCompile(`^(\d{4})/(\d{1,2})/(\d{1,2})(?: (\d{1,2}):(\d{2}):(\d{2}))?$`)

func parseStamp(s string, hourly bool) (Stamp, error) {
	m := reDate.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return Stamp{}, fmt.Errorf("bad timestamp %q", s)
	}
	y, _ := strconv.Atoi(m[1])
	mo, _ := strconv.Atoi(m[2])
	d, _ := strconv.Atoi(m[3])
	date := time.Date(y, time.Month(mo), d, 0, 0, 0, 0, time.UTC)
	if !hourly {
		if m[4] != "" {
			return Stamp{}, fmt.Errorf("unexpected time in daily timestamp %q", s)
		}
		return Stamp{Date: date}, nil
	}
	if m[4] == "" {
		return Stamp{}, fmt.Errorf("missing time in hourly timestamp %q", s)
	}
	h, _ := strconv.Atoi(m[4])
	if m[5] != "00" || m[6] != "00" || h > 23 {
		return Stamp{}, fmt.Errorf("unexpected hourly timestamp %q", s)
	}
	if h == 0 {
		return Stamp{Date: date.AddDate(0, 0, -1), Hour: 24}, nil
	}
	return Stamp{Date: date, Hour: h}, nil
}

func isBlank(rec []string) bool {
	for _, f := range rec {
		if strings.TrimSpace(f) != "" {
			return false
		}
	}
	return true
}

type colRole int

const (
	roleValue colRole = iota
	roleNoPhenomenon
	roleQuality
	roleDirection
	roleDirQuality
	roleHomogeneity
)

type column struct {
	group int
	role  colRole
}

// ParseTable parses a CSV returned for req. The column layout differs by
// station type and element, so it is derived from the header rows and
// checked against the requested stations and elements.
func ParseTable(raw []byte, req TableRequest) (*Table, error) {
	text, err := decodeCP932(raw)
	if err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	r := csv.NewReader(bytes.NewReader(text))
	r.FieldsPerRecord = -1
	r.LazyQuotes = true
	var recs [][]string
	for {
		rec, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("csv: %w", err)
		}
		recs = append(recs, rec)
	}
	if len(recs) == 0 {
		return nil, fmt.Errorf("empty CSV")
	}
	t := &Table{}
	first := strings.Join(recs[0], ",")
	const dlPrefix = "ダウンロードした時刻："
	if !strings.HasPrefix(first, dlPrefix) {
		return nil, fmt.Errorf("unexpected first line %q", first)
	}
	if t.DownloadedAt, err = time.ParseInLocation("2006/01/02 15:04:05", strings.TrimPrefix(first, dlPrefix), JST); err != nil {
		return nil, fmt.Errorf("download time: %w", err)
	}

	dataStart := -1
	for i := 1; i < len(recs); i++ {
		if len(recs[i]) > 0 && reDate.MatchString(strings.TrimSpace(recs[i][0])) {
			dataStart = i
			break
		}
	}
	if dataStart < 0 {
		return nil, fmt.Errorf("no data rows")
	}
	var header [][]string
	for _, rec := range recs[1:dataStart] {
		if !isBlank(rec) {
			header = append(header, rec)
		}
	}
	// station row, element row, optional sub-element rows, attribute row
	if len(header) < 3 {
		return nil, fmt.Errorf("expected at least 3 header rows, got %d", len(header))
	}
	stationRow, elementRow, attrRow := header[0], header[1], header[len(header)-1]
	subRows := header[2 : len(header)-1]
	if !strings.HasPrefix(elementRow[0], "年月日") {
		return nil, fmt.Errorf("unexpected element header %q", elementRow[0])
	}
	width := len(elementRow)
	get := func(row []string, i int) string {
		if i < len(row) {
			return strings.TrimSpace(row[i])
		}
		return ""
	}

	nEl := len(req.Elements)
	nGroups := len(req.Stations) * nEl
	cols := make([]column, width)
	groupStart := make([]int, 0, nGroups)
	group := -1
	inDirection := false
	for i := 1; i < width; i++ {
		attr := get(attrRow, i)
		sub := ""
		for _, sr := range subRows {
			if s := get(sr, i); s != "" {
				sub = s
			}
		}
		switch {
		case attr == "" && sub == "風向" && group >= 0 && !inDirection:
			inDirection = true
			cols[i] = column{group, roleDirection}
		case attr == "":
			group++
			inDirection = false
			groupStart = append(groupStart, i)
			cols[i] = column{group, roleValue}
		case group < 0:
			return nil, fmt.Errorf("attribute column %d (%q) before any value column", i, attr)
		case attr == "品質情報" && inDirection:
			cols[i] = column{group, roleDirQuality}
		case attr == "品質情報":
			cols[i] = column{group, roleQuality}
		case attr == "現象なし情報":
			cols[i] = column{group, roleNoPhenomenon}
		case attr == "均質番号":
			cols[i] = column{group, roleHomogeneity}
		default:
			return nil, fmt.Errorf("unknown attribute column %q", attr)
		}
	}
	if len(groupStart) != nGroups {
		return nil, fmt.Errorf("expected %d column groups (%d stations x %d elements), got %d",
			nGroups, len(req.Stations), nEl, len(groupStart))
	}
	for g, col := range groupStart {
		st, el := g/nEl, req.Elements[g%nEl]
		name := get(stationRow, col)
		if el.Header != "" && !strings.HasPrefix(get(elementRow, col), el.Header) {
			return nil, fmt.Errorf("column %d: expected element %q, got %q", col, el.Header, get(elementRow, col))
		}
		if g%nEl == 0 {
			t.StationNames = append(t.StationNames, name)
		} else if name != t.StationNames[st] {
			return nil, fmt.Errorf("column %d: station name %q differs from %q", col, name, t.StationNames[st])
		}
	}

	hourly := req.Frequency == Hourly
	t.Cells = make([][][]Cell, len(req.Stations))
	for _, rec := range recs[dataStart:] {
		if isBlank(rec) {
			continue
		}
		stamp, err := parseStamp(rec[0], hourly)
		if err != nil {
			return nil, err
		}
		if len(rec) != width {
			return nil, fmt.Errorf("%s: %d fields, header has %d", rec[0], len(rec), width)
		}
		t.Stamps = append(t.Stamps, stamp)
		row := make([]Cell, nGroups)
		for g := range row {
			row[g] = Cell{NoPhenomenon: -1, Quality: -1, DirQuality: -1}
		}
		for i := 1; i < width; i++ {
			c := &row[cols[i].group]
			v := strings.TrimSpace(rec[i])
			switch cols[i].role {
			case roleValue:
				c.Value = v
			case roleDirection:
				c.Direction = v
			case roleNoPhenomenon:
				if c.NoPhenomenon, err = smallInt(v); err != nil {
					return nil, fmt.Errorf("%s col %d: %w", rec[0], i, err)
				}
			case roleQuality:
				if c.Quality, err = smallInt(v); err != nil {
					return nil, fmt.Errorf("%s col %d: %w", rec[0], i, err)
				}
			case roleDirQuality:
				if c.DirQuality, err = smallInt(v); err != nil {
					return nil, fmt.Errorf("%s col %d: %w", rec[0], i, err)
				}
			case roleHomogeneity:
				if v != "" {
					if c.Homogeneity, err = strconv.Atoi(v); err != nil {
						return nil, fmt.Errorf("%s col %d: %w", rec[0], i, err)
					}
				}
			}
		}
		for s := range req.Stations {
			t.Cells[s] = append(t.Cells[s], row[s*nEl:(s+1)*nEl])
		}
	}
	return t, nil
}

func smallInt(s string) (int8, error) {
	if s == "" {
		return -1, nil
	}
	n, err := strconv.ParseInt(s, 10, 8)
	if err != nil {
		return -1, fmt.Errorf("bad flag %q", s)
	}
	return int8(n), nil
}
