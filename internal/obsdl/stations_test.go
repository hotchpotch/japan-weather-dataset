package obsdl

import (
	"math"
	"testing"
)

func TestParseStation(t *testing.T) {
	st, err := parseStation(stationMarker{Stid: "a0367", Stname: "調布", Kansoku: "100000", Ended: true,
		Title: "地点名：調布\nカナ:チョウフ\n北緯：35度40.1分\n東経：139度31.8分\n標高：41m\n1976年12月13日に観測終了"})
	if err != nil {
		t.Fatal(err)
	}
	if st.Kana != "チョウフ" || math.Abs(st.Latitude-(35+40.1/60)) > 1e-9 || math.Abs(st.Longitude-(139+31.8/60)) > 1e-9 {
		t.Errorf("station = %+v", st)
	}
	if st.ElevationM == nil || *st.ElevationM != 41 || st.EndedOn == nil || st.EndedOn.Format("2006-01-02") != "1976-12-13" {
		t.Errorf("elevation/end = %v %v", st.ElevationM, st.EndedOn)
	}
	if st.Official() || !st.Categories()[CatRain] || st.Categories()[CatTemp] {
		t.Errorf("type/categories = %v %v", st.Official(), st.Categories())
	}

	st, err = parseStation(stationMarker{Stid: "s89532", Stname: "昭和", Kansoku: "111111",
		Title: "地点名：昭和\nカナ:ショウワ\n南緯：69度00.3分\n東経：39度34.8分\n標高：29.1m"})
	if err != nil {
		t.Fatal(err)
	}
	if st.Latitude >= 0 || *st.ElevationM != 29.1 {
		t.Errorf("southern station = %+v", st)
	}
}
