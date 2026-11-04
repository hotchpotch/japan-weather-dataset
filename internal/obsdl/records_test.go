package obsdl

import "testing"

func TestConvertBreaksAndFilter(t *testing.T) {
	els := []Element{DailyElements[0]} // temperature_mean
	req := TableRequest{Frequency: Daily, Stations: []string{"a1002"}, Elements: els}
	cell := func(v string, q int8, h int) []Cell {
		return []Cell{{Value: v, Quality: q, NoPhenomenon: -1, DirQuality: -1, Homogeneity: h}}
	}
	tab := &Table{
		StationNames: []string{"練馬"},
		Stamps: []Stamp{
			{Date: day(2012, 12, 31)}, // overlap day before the stored range
			{Date: day(2013, 1, 1)},
			{Date: day(2013, 1, 2)},
			{Date: day(2013, 1, 3)},
		},
		Cells: [][][]Cell{{
			cell("1.0", 8, 1),
			cell("2.0", 8, 2), // break at the first stored day
			cell("", 0, 2),    // not observed: dropped
			cell("3.0", 8, 3),
		}},
	}
	res := tab.Convert(req, 0, day(2013, 1, 1), day(2013, 1, 3))
	if len(res.Records) != 2 {
		t.Fatalf("records = %d, want 2", len(res.Records))
	}
	if len(res.Breaks) != 2 || res.Breaks[0].At.Date != day(2013, 1, 1) || res.Breaks[1].At.Date != day(2013, 1, 3) {
		t.Fatalf("breaks = %+v", res.Breaks)
	}
	if res.Breaks[0].Element != "temperature_mean" {
		t.Errorf("break element = %s", res.Breaks[0].Element)
	}
	// Elements not requested become nulls so that every record has the full schema.
	if got, want := len(res.Records[0]), len(KeyColumns(Daily))+len(ValueColumns(Daily)); got != want {
		t.Errorf("record length = %d, want %d", got, want)
	}
}
