package obsdl

import "fmt"

// Frequency is the aggregation period ("aggrgPeriod") of the obsdl form.
type Frequency string

const (
	Hourly Frequency = "hourly"
	Daily  Frequency = "daily"
)

// AggrgPeriod returns the obsdl form value for the frequency.
func (f Frequency) AggrgPeriod() string {
	switch f {
	case Hourly:
		return "9"
	case Daily:
		return "1"
	}
	panic(fmt.Sprintf("unknown frequency %q", string(f)))
}

func ParseFrequency(s string) (Frequency, error) {
	switch Frequency(s) {
	case Hourly, Daily:
		return Frequency(s), nil
	}
	return "", fmt.Errorf("unknown frequency %q (want hourly or daily)", s)
}

// ValueType tells how the primary value column of an element is parsed.
type ValueType int

const (
	Number    ValueType = iota // decimal number
	Direction                  // 16-point wind direction written in Japanese
	Code                       // integer code (hourly weather)
	Text                       // free text (daily weather summary)
)

// Category follows the obsdl "kansoku" string, which has one digit per
// category in this order: rain, wind, temperature, sunshine, snow, other.
type Category int

const (
	CatRain Category = iota
	CatWind
	CatTemp
	CatSun
	CatSnow
	CatOther
	numCategories
)

var categoryNames = [numCategories]string{"rain", "wind", "temperature", "sunshine", "snow", "other"}

func (c Category) String() string { return categoryNames[c] }

// Element is one requested obsdl element ("elementNumList" entry) and the
// columns it produces in the dataset.
type Element struct {
	Key string // column name in the dataset
	// Header is the prefix of the element name in the CSV header row,
	// used to check that columns come back in the requested order.
	Header string
	ID     string // obsdl element number
	Opt    string // obsdl element option (e.g. "1" / "10" for max precipitation)
	Type   ValueType
	// HasNoPhenomenon is true when official stations report the
	// "現象なし情報" (no phenomenon) flag for the element.
	HasNoPhenomenon bool
	// WithDirection is true when the element carries a wind direction
	// in addition to its value (e.g. maximum wind speed and its direction).
	WithDirection bool
	// AllowModifier is true when the value may carry a trailing "+" or "-"
	// (cloud amount "0+" / "10-").
	AllowModifier bool
	// OfficialOnly marks elements that only official stations (気象台等) observe.
	OfficialOnly bool
	Category     Category
	Unit         string
	NameJa       string
	Description  string
}

// HourlyElements are the elements selectable for hourly values (時別値).
var HourlyElements = []Element{
	{Key: "temperature", Header: "気温", ID: "201", Type: Number, Category: CatTemp, Unit: "degC", NameJa: "気温", Description: "Air temperature"},
	{Key: "precipitation", Header: "降水量", ID: "101", Type: Number, HasNoPhenomenon: true, Category: CatRain, Unit: "mm", NameJa: "降水量（前1時間）", Description: "Precipitation in the preceding hour"},
	{Key: "snowfall", Header: "降雪", ID: "503", Type: Number, HasNoPhenomenon: true, Category: CatSnow, Unit: "cm", NameJa: "降雪の深さ（前1時間）", Description: "Snowfall depth in the preceding hour"},
	{Key: "snow_depth", Header: "積雪", ID: "501", Type: Number, HasNoPhenomenon: true, Category: CatSnow, Unit: "cm", NameJa: "積雪の深さ", Description: "Snow depth"},
	{Key: "sunshine_duration", Header: "日照時間", ID: "401", Type: Number, HasNoPhenomenon: true, Category: CatSun, Unit: "h", NameJa: "日照時間（前1時間）", Description: "Sunshine duration in the preceding hour"},
	{Key: "wind_speed", Header: "風速", ID: "301", Type: Number, WithDirection: true, Category: CatWind, Unit: "m/s", NameJa: "風向・風速", Description: "Mean wind speed (10 min) and direction"},
	{Key: "solar_radiation", Header: "日射量", ID: "610", Type: Number, OfficialOnly: true, Category: CatSun, Unit: "MJ/m2", NameJa: "全天日射量（前1時間）", Description: "Global solar radiation in the preceding hour"},
	{Key: "local_pressure", Header: "現地気圧", ID: "601", Type: Number, OfficialOnly: true, Category: CatOther, Unit: "hPa", NameJa: "現地気圧", Description: "Station-level pressure"},
	{Key: "sea_level_pressure", Header: "海面気圧", ID: "602", Type: Number, OfficialOnly: true, Category: CatOther, Unit: "hPa", NameJa: "海面気圧", Description: "Sea-level pressure"},
	{Key: "relative_humidity", Header: "相対湿度", ID: "605", Type: Number, Category: CatOther, Unit: "%", NameJa: "相対湿度", Description: "Relative humidity"},
	{Key: "vapor_pressure", Header: "蒸気圧", ID: "604", Type: Number, Category: CatOther, Unit: "hPa", NameJa: "蒸気圧", Description: "Vapor pressure"},
	{Key: "dew_point", Header: "露点温度", ID: "612", Type: Number, Category: CatOther, Unit: "degC", NameJa: "露点温度", Description: "Dew point temperature"},
	{Key: "weather", Header: "天気", ID: "703", Type: Code, OfficialOnly: true, Category: CatOther, Unit: "code", NameJa: "天気", Description: "Present weather code (JMA code table)"},
	{Key: "cloud_cover", Header: "雲量", ID: "607", Type: Number, AllowModifier: true, OfficialOnly: true, Category: CatOther, Unit: "tenths", NameJa: "雲量", Description: "Total cloud amount (0-10)"},
	{Key: "visibility", Header: "視程", ID: "704", Type: Number, OfficialOnly: true, Category: CatOther, Unit: "km", NameJa: "視程", Description: "Visibility"},
}

// DailyElements are the elements selectable for daily values (日別値).
var DailyElements = []Element{
	{Key: "temperature_mean", Header: "平均気温", ID: "201", Type: Number, Category: CatTemp, Unit: "degC", NameJa: "平均気温", Description: "Daily mean air temperature"},
	{Key: "temperature_max", Header: "最高気温", ID: "202", Type: Number, Category: CatTemp, Unit: "degC", NameJa: "最高気温", Description: "Daily maximum air temperature"},
	{Key: "temperature_min", Header: "最低気温", ID: "203", Type: Number, Category: CatTemp, Unit: "degC", NameJa: "最低気温", Description: "Daily minimum air temperature"},
	{Key: "precipitation_total", Header: "降水量の合計", ID: "101", Type: Number, HasNoPhenomenon: true, Category: CatRain, Unit: "mm", NameJa: "降水量の合計", Description: "Daily total precipitation"},
	{Key: "precipitation_max_1h", Header: "1時間降水量の最大", ID: "105", Opt: "1", Type: Number, HasNoPhenomenon: true, Category: CatRain, Unit: "mm", NameJa: "1時間降水量の最大", Description: "Daily maximum 1-hour precipitation"},
	{Key: "precipitation_max_10min", Header: "10分間降水量の最大", ID: "105", Opt: "10", Type: Number, HasNoPhenomenon: true, Category: CatRain, Unit: "mm", NameJa: "10分間降水量の最大", Description: "Daily maximum 10-minute precipitation"},
	{Key: "sunshine_duration", Header: "日照時間", ID: "401", Type: Number, HasNoPhenomenon: true, Category: CatSun, Unit: "h", NameJa: "日照時間", Description: "Daily sunshine duration"},
	{Key: "solar_radiation_total", Header: "合計全天日射量", ID: "610", Type: Number, OfficialOnly: true, Category: CatSun, Unit: "MJ/m2", NameJa: "合計全天日射量", Description: "Daily global solar radiation"},
	{Key: "snow_depth_max", Header: "最深積雪", ID: "501", Type: Number, HasNoPhenomenon: true, Category: CatSnow, Unit: "cm", NameJa: "最深積雪", Description: "Daily maximum snow depth"},
	{Key: "snowfall_total", Header: "降雪量合計", ID: "503", Type: Number, HasNoPhenomenon: true, Category: CatSnow, Unit: "cm", NameJa: "降雪量合計", Description: "Daily total snowfall"},
	{Key: "wind_speed_mean", Header: "平均風速", ID: "301", Type: Number, Category: CatWind, Unit: "m/s", NameJa: "平均風速", Description: "Daily mean wind speed"},
	{Key: "wind_speed_max", Header: "最大風速", ID: "302", Type: Number, WithDirection: true, Category: CatWind, Unit: "m/s", NameJa: "最大風速", Description: "Daily maximum (10-min mean) wind speed and its direction"},
	{Key: "wind_gust_max", Header: "最大瞬間風速", ID: "304", Type: Number, WithDirection: true, Category: CatWind, Unit: "m/s", NameJa: "最大瞬間風速", Description: "Daily maximum instantaneous wind speed and its direction"},
	{Key: "wind_direction_mode", Header: "最多風向", ID: "305", Type: Direction, Category: CatWind, Unit: "16-point", NameJa: "最多風向", Description: "Most frequent wind direction of the day"},
	{Key: "vapor_pressure_mean", Header: "平均蒸気圧", ID: "604", Type: Number, Category: CatOther, Unit: "hPa", NameJa: "平均蒸気圧", Description: "Daily mean vapor pressure"},
	{Key: "relative_humidity_mean", Header: "平均湿度", ID: "605", Type: Number, Category: CatOther, Unit: "%", NameJa: "平均湿度", Description: "Daily mean relative humidity"},
	{Key: "relative_humidity_min", Header: "最小相対湿度", ID: "606", Type: Number, Category: CatOther, Unit: "%", NameJa: "最小相対湿度", Description: "Daily minimum relative humidity"},
	{Key: "local_pressure_mean", Header: "平均現地気圧", ID: "601", Type: Number, OfficialOnly: true, Category: CatOther, Unit: "hPa", NameJa: "平均現地気圧", Description: "Daily mean station-level pressure"},
	{Key: "sea_level_pressure_mean", Header: "平均海面気圧", ID: "602", Type: Number, OfficialOnly: true, Category: CatOther, Unit: "hPa", NameJa: "平均海面気圧", Description: "Daily mean sea-level pressure"},
	{Key: "sea_level_pressure_min", Header: "最低海面気圧", ID: "603", Type: Number, OfficialOnly: true, Category: CatOther, Unit: "hPa", NameJa: "最低海面気圧", Description: "Daily minimum sea-level pressure"},
	{Key: "cloud_cover_mean", Header: "平均雲量", ID: "607", Type: Number, OfficialOnly: true, Category: CatOther, Unit: "tenths", NameJa: "平均雲量", Description: "Daily mean total cloud amount (0-10)"},
	{Key: "weather_day", Header: "天気概況(昼", ID: "701", Type: Text, OfficialOnly: true, Category: CatOther, Unit: "text", NameJa: "天気概況（昼：06時～18時）", Description: "Weather summary 06-18 JST (Japanese text)"},
	{Key: "weather_night", Header: "天気概況(夜", ID: "702", Type: Text, OfficialOnly: true, Category: CatOther, Unit: "text", NameJa: "天気概況（夜：18時～翌日06時）", Description: "Weather summary 18-06 JST (Japanese text)"},
}

// Elements returns the full element list of a frequency.
func Elements(f Frequency) []Element {
	switch f {
	case Hourly:
		return HourlyElements
	case Daily:
		return DailyElements
	}
	panic(fmt.Sprintf("unknown frequency %q", string(f)))
}

// ElementsFor returns the elements requested for a station: official
// stations get every element, AMeDAS stations get those not marked
// OfficialOnly and whose category is in cats. A nil cats means all categories.
func ElementsFor(f Frequency, official bool, cats map[Category]bool) []Element {
	var out []Element
	for _, e := range Elements(f) {
		if official {
			out = append(out, e)
			continue
		}
		if e.OfficialOnly {
			continue
		}
		if cats != nil && !cats[e.Category] {
			continue
		}
		out = append(out, e)
	}
	return out
}

// windDirections maps the 16-point directions written by obsdl to the JMA
// code: 1=NNE, 2=NE, ..., 15=NNW, 16=N, 0=calm.
var windDirections = map[string]uint8{
	"静穏": 0, "北北東": 1, "北東": 2, "東北東": 3, "東": 4, "東南東": 5, "南東": 6, "南南東": 7,
	"南": 8, "南南西": 9, "南西": 10, "西南西": 11, "西": 12, "西北西": 13, "北西": 14, "北北西": 15, "北": 16,
}
