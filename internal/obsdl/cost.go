package obsdl

import "time"

// ServerCellLimit is the data amount the obsdl form allows in one request,
// counted as stations x elements x time steps (the "100%" of the gauge in
// the web form). Requests above it are rejected with an error page.
const ServerCellLimit = 200_000

// Days returns the number of calendar days in the request period.
func (r TableRequest) Days() int {
	if r.SameDaysEachYear {
		from := time.Date(2001, r.From.Month(), r.From.Day(), 0, 0, 0, 0, time.UTC)
		to := time.Date(2001, r.To.Month(), r.To.Day(), 0, 0, 0, 0, time.UTC)
		per := int(to.Sub(from).Hours()/24) + 1
		return per * (r.To.Year() - r.From.Year() + 1)
	}
	return int(r.To.Sub(r.From).Hours()/24) + 1
}

// Cells estimates the request size in the unit of ServerCellLimit.
func (r TableRequest) Cells() int {
	steps := r.Days()
	if r.Frequency == Hourly {
		steps *= 24
	}
	return len(r.Stations) * len(r.Elements) * steps
}
