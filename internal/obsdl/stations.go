package obsdl

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Prefecture is an obsdl area. Hokkaido is split into 14 sub-prefectures,
// so the "prefecture" numbers are JMA's own codes, not JIS codes.
type Prefecture struct {
	Code string // two digits, e.g. "44" for Tokyo
	Name string
}

// Station is an observation station as listed by obsdl.
type Station struct {
	ID             string // "s" + 5 digits (official) or "a" + 4 digits (AMeDAS)
	Name           string
	Kana           string
	PrefectureCode string
	PrefectureName string
	Latitude       float64 // decimal degrees, from degrees and 0.1 minutes
	Longitude      float64
	ElevationM     *float64
	Kansoku        string // one digit per Category
	EndedOn        *time.Time
	Title          string // raw tooltip text
}

// Official reports whether the station is an official station (気象台等).
func (s Station) Official() bool { return strings.HasPrefix(s.ID, "s") }

// Categories returns the categories marked as observed in the kansoku string.
func (s Station) Categories() map[Category]bool {
	m := map[Category]bool{}
	for i, r := range s.Kansoku {
		if i < int(numCategories) && r != '0' {
			m[Category(i)] = true
		}
	}
	return m
}

type stationPage struct {
	View      string          `json:"view"`
	PrefTable [][]prefCell    `json:"prefTable"`
	Markers   []stationMarker `json:"markers"`
}

type prefCell struct {
	Type string `json:"type"`
	Prid int    `json:"prid"`
	Name string `json:"name"`
}

type stationMarker struct {
	Kind    string `json:"kind"`
	Stid    string `json:"stid"`
	Stname  string `json:"stname"`
	Prid    string `json:"prid"`
	Kansoku string `json:"kansoku"`
	Ended   bool   `json:"ended"`
	Title   string `json:"title"`
}

func (c *Client) stationPage(ctx context.Context, pd string) (*stationPage, error) {
	resp, err := c.do(ctx, "top/station", url.Values{"pd": {pd}, "format": {"json"}}, false)
	if err != nil {
		return nil, err
	}
	var p stationPage
	if err := json.Unmarshal(resp.Body, &p); err != nil {
		return nil, fmt.Errorf("obsdl: decode station page %s: %w", pd, err)
	}
	return &p, nil
}

// Prefectures lists the areas of the station selector.
func (c *Client) Prefectures(ctx context.Context) ([]Prefecture, error) {
	p, err := c.stationPage(ctx, "00")
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []Prefecture
	for _, row := range p.PrefTable {
		for _, cell := range row {
			if cell.Type != "pref" {
				continue
			}
			code := fmt.Sprintf("%02d", cell.Prid)
			if seen[code] {
				continue
			}
			seen[code] = true
			out = append(out, Prefecture{Code: code, Name: cell.Name})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Code < out[j].Code })
	return out, nil
}

// Stations lists the stations of one area. A station drawn on the map of a
// neighbouring area is returned only by its own area.
func (c *Client) Stations(ctx context.Context, pref Prefecture) ([]Station, error) {
	p, err := c.stationPage(ctx, pref.Code)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []Station
	for _, m := range p.Markers {
		if m.Kind != "station" || seen[m.Stid] {
			continue
		}
		if m.Prid != "" && m.Prid != pref.Code {
			continue
		}
		seen[m.Stid] = true
		st, err := parseStation(m)
		if err != nil {
			return nil, fmt.Errorf("obsdl: station %s: %w", m.Stid, err)
		}
		st.PrefectureCode = pref.Code
		st.PrefectureName = pref.Name
		out = append(out, st)
	}
	return out, nil
}

var (
	reKana    = regexp.MustCompile(`カナ[:：]\s*(\S+)`)
	reLat     = regexp.MustCompile(`(北|南)緯[:：]\s*(\d+)度([\d.]+)分`)
	reLon     = regexp.MustCompile(`(東|西)経[:：]\s*(\d+)度([\d.]+)分`)
	reElev    = regexp.MustCompile(`標高[:：]\s*(-?[\d.]+)m`)
	reEndedOn = regexp.MustCompile(`(\d{4})年(\d{1,2})月(\d{1,2})日に観測終了`)
)

// degMin converts a (hemisphere, degrees, minutes) match to signed decimal degrees.
func degMin(m []string) (float64, error) {
	d, err := strconv.ParseFloat(m[2], 64)
	if err != nil {
		return 0, err
	}
	mi, err := strconv.ParseFloat(m[3], 64)
	if err != nil {
		return 0, err
	}
	v := d + mi/60
	if m[1] == "南" || m[1] == "西" {
		v = -v
	}
	return v, nil
}

func parseStation(m stationMarker) (Station, error) {
	st := Station{ID: m.Stid, Name: m.Stname, Kansoku: m.Kansoku, Title: m.Title}
	if g := reKana.FindStringSubmatch(m.Title); g != nil {
		st.Kana = g[1]
	}
	g := reLat.FindStringSubmatch(m.Title)
	if g == nil {
		return st, fmt.Errorf("no latitude in %q", m.Title)
	}
	var err error
	if st.Latitude, err = degMin(g); err != nil {
		return st, err
	}
	g = reLon.FindStringSubmatch(m.Title)
	if g == nil {
		return st, fmt.Errorf("no longitude in %q", m.Title)
	}
	if st.Longitude, err = degMin(g); err != nil {
		return st, err
	}
	if g := reElev.FindStringSubmatch(m.Title); g != nil {
		if v, err := strconv.ParseFloat(g[1], 64); err == nil {
			st.ElevationM = &v
		}
	}
	if g := reEndedOn.FindStringSubmatch(m.Title); g != nil {
		y, _ := strconv.Atoi(g[1])
		mo, _ := strconv.Atoi(g[2])
		d, _ := strconv.Atoi(g[3])
		t := time.Date(y, time.Month(mo), d, 0, 0, 0, 0, time.UTC)
		st.EndedOn = &t
	} else if m.Ended {
		return st, fmt.Errorf("ended station without end date in %q", m.Title)
	}
	return st, nil
}
