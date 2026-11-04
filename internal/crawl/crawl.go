// Package crawl plans obsdl requests and stores their results.
package crawl

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/hotchpotch/japan-weather-dataset/internal/obsdl"
	"github.com/hotchpotch/japan-weather-dataset/internal/store"
)

// MinYear is the first year requested. The oldest records of official
// stations start in 1872.
const MinYear = 1872

type Crawler struct {
	Client *obsdl.Client
	Store  *store.Store
	Log    *slog.Logger
	// Budget is the maximum request size in obsdl cells; keep it below
	// obsdl.ServerCellLimit.
	Budget int
	// MaxStations per request.
	MaxStations int
	// KeepRaw stores every CSV response gzipped under raw/.
	KeepRaw bool
	// MaxRequestsPer24h is a self-imposed limit of requests in any 24 hours
	// (counted from the request log, across runs). 0 means no limit.
	MaxRequestsPer24h int
	// Now returns the current time; the latest requestable day is the day
	// before today in JST.
	Now func() time.Time

	// limit is the request size in effect. It starts at Budget and is
	// halved whenever the server fails on a request (e.g. HTTP 504 when
	// a request takes longer than about 60 s); larger planned requests are
	// split before they are sent.
	limit int
	// fetchFunc replaces fetch in tests.
	fetchFunc func(ctx context.Context, purpose string, req obsdl.TableRequest) (*obsdl.Table, int64, error)
}

// minLimit is the smallest request size limit lowers to.
const minLimit = 1_000

func (c *Crawler) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// Yesterday is the last day obsdl serves (requests including today fail).
func (c *Crawler) Yesterday() time.Time {
	t := c.now().In(obsdl.JST)
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, -1)
}

// RefreshStations downloads the station lists of every area.
func (c *Crawler) RefreshStations(ctx context.Context) ([]obsdl.Station, error) {
	prefs, err := c.Client.Prefectures(ctx)
	if err != nil {
		return nil, err
	}
	var all []obsdl.Station
	seen := map[string]bool{}
	for i, p := range prefs {
		sts, err := c.Client.Stations(ctx, p)
		if err != nil {
			return nil, fmt.Errorf("stations of %s %s: %w", p.Code, p.Name, err)
		}
		for _, st := range sts {
			if seen[st.ID] {
				continue
			}
			seen[st.ID] = true
			all = append(all, st)
		}
		c.Log.Info("stations", "area", p.Code, "name", p.Name, "count", len(sts), "progress", fmt.Sprintf("%d/%d", i+1, len(prefs)))
	}
	if len(all) == 0 {
		return nil, errors.New("no stations found")
	}
	if err := c.Store.SaveStations(ctx, all, c.now()); err != nil {
		return nil, err
	}
	return all, nil
}

// ErrQuota means the self-imposed request limit (MaxRequestsPer24h) was reached.
var ErrQuota = errors.New("request quota for the last 24 hours reached")

// checkQuota fails with ErrQuota when the request limit is reached.
func (c *Crawler) checkQuota(ctx context.Context) error {
	if c.MaxRequestsPer24h <= 0 {
		return nil
	}
	n, err := c.Store.RequestsSince(ctx, c.now().Add(-24*time.Hour))
	if err != nil {
		return err
	}
	if n >= c.MaxRequestsPer24h {
		return fmt.Errorf("%w: %d requests (limit %d)", ErrQuota, n, c.MaxRequestsPer24h)
	}
	return nil
}

// job is one obsdl request. Rows of Req.From..Req.To outside
// [StoreFrom, StoreTo] are fetched only to detect homogeneity breaks.
type job struct {
	Purpose            string
	Req                obsdl.TableRequest
	StoreFrom, StoreTo time.Time
}

func (j job) String() string {
	return fmt.Sprintf("%s %s %s..%s stations=%d elements=%d cells=%d", j.Purpose, j.Req.Frequency,
		j.StoreFrom.Format(time.DateOnly), j.StoreTo.Format(time.DateOnly), len(j.Req.Stations), len(j.Req.Elements), j.Req.Cells())
}

// fetch sends a request and records it in the request log.
func (c *Crawler) fetch(ctx context.Context, purpose string, req obsdl.TableRequest) (*obsdl.Table, int64, error) {
	if err := c.checkQuota(ctx); err != nil {
		return nil, 0, err
	}
	id, err := c.Store.NextRequestID(ctx)
	if err != nil {
		return nil, 0, err
	}
	l := store.RequestLog{ID: id, StartedAt: c.now(), Purpose: purpose, Req: req}
	resp, err := c.Client.Table(ctx, req)
	var tab *obsdl.Table
	if err == nil {
		l.Duration, l.Bytes = resp.Duration, len(resp.Body)
		sum := sha256.Sum256(resp.Body)
		l.SHA256 = hex.EncodeToString(sum[:])
		if c.KeepRaw {
			if werr := c.saveRaw(id, req, resp.Body); werr != nil {
				c.Log.Warn("save raw response", "err", werr)
			}
		}
		tab, err = obsdl.ParseTable(resp.Body, req)
		if err != nil {
			err = fmt.Errorf("parse response of request %d: %w", id, err)
		}
	}
	l.Status = "ok"
	switch {
	case errors.Is(err, obsdl.ErrTooLarge):
		l.Status = "too_large"
	case errors.Is(err, obsdl.ErrServer):
		l.Status = "server_error"
	case errors.Is(err, obsdl.ErrRateLimited):
		l.Status = "rate_limited"
	case errors.Is(err, obsdl.ErrBlocked):
		l.Status = "blocked"
	case errors.Is(err, obsdl.ErrMaintenance):
		l.Status = "maintenance"
	case err != nil:
		l.Status = "error"
	}
	l.Err = err
	if lerr := c.Store.LogRequest(ctx, l); lerr != nil && err == nil {
		err = lerr
	}
	return tab, id, err
}

func (c *Crawler) saveRaw(id int64, req obsdl.TableRequest, body []byte) error {
	dir := filepath.Join(c.Store.Dir, "raw", string(req.Frequency), c.now().In(obsdl.JST).Format("20060102"))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	zw.Write(body)
	if err := zw.Close(); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, fmt.Sprintf("%08d.csv.gz", id)), buf.Bytes(), 0o644)
}

// fetchJob runs the request of a job and passes the result to handle. A job
// larger than the current limit is split before it is sent. When the server
// rejects the request as too large or fails on it, the limit is halved for
// the rest of the run and the job is split in two.
func (c *Crawler) fetchJob(ctx context.Context, j job, handle func(job, *obsdl.Table, int64) error) error {
	if c.limit == 0 {
		c.limit = c.Budget
	}
	if j.Req.Cells() > c.limit {
		if a, b, ok := split(j); ok {
			if err := c.fetchJob(ctx, a, handle); err != nil {
				return err
			}
			return c.fetchJob(ctx, b, handle)
		}
	}
	fetch := c.fetch
	if c.fetchFunc != nil {
		fetch = c.fetchFunc
	}
	tab, id, err := fetch(ctx, j.Purpose, j.Req)
	if errors.Is(err, obsdl.ErrTooLarge) || errors.Is(err, obsdl.ErrServer) {
		a, b, ok := split(j)
		if !ok {
			return err
		}
		if l := max(minLimit, min(c.limit, j.Req.Cells())/2); l < c.limit {
			c.limit = l
			c.Log.Warn("lowering request size", "limit", l, "err", err)
		}
		c.Log.Warn("request failed, splitting", "job", j.String(), "err", err)
		if err := c.fetchJob(ctx, a, handle); err != nil {
			return err
		}
		return c.fetchJob(ctx, b, handle)
	}
	if err != nil {
		return err
	}
	return handle(j, tab, id)
}

// run fetches a job and stores its rows.
func (c *Crawler) run(ctx context.Context, j job) error {
	return c.fetchJob(ctx, j, func(j job, tab *obsdl.Table, id int64) error {
		return c.store(ctx, j, tab, id)
	})
}

func (c *Crawler) store(ctx context.Context, j job, tab *obsdl.Table, id int64) error {
	start := time.Now()
	fetch := store.Fetch{RequestID: id, FetchedAt: c.now(), JMADownloadsAt: tab.DownloadedAt}
	var rows, warnings int
	for s := range j.Req.Stations {
		res := tab.Convert(j.Req, s, j.StoreFrom, j.StoreTo)
		warnings += len(res.Warnings)
		for i, w := range res.Warnings {
			if i >= 5 {
				c.Log.Warn("more conversion warnings", "station", res.StationID, "count", len(res.Warnings))
				break
			}
			c.Log.Warn("conversion", "detail", w)
		}
		rows += len(res.Records)
		if err := c.storeStation(ctx, j, res, fetch); err != nil {
			return err
		}
	}
	c.Log.Info("stored", "job", j.String(), "rows", rows, "warnings", warnings, "elapsed", time.Since(start).Round(time.Millisecond))
	return nil
}

// storeStation writes the rows of one station split into yearly chunks.
func (c *Crawler) storeStation(ctx context.Context, j job, res obsdl.StationResult, fetch store.Fetch) error {
	for y := j.StoreFrom.Year(); y <= j.StoreTo.Year(); y++ {
		from := maxTime(j.StoreFrom, time.Date(y, 1, 1, 0, 0, 0, 0, time.UTC))
		to := minTime(j.StoreTo, time.Date(y, 12, 31, 0, 0, 0, 0, time.UTC))
		var recs []obsdl.Record
		for i, st := range res.Stamps {
			if st.Date.Year() == y {
				recs = append(recs, res.Records[i])
			}
		}
		if err := c.Store.WriteChunk(ctx, j.Req.Frequency, res.StationID, y, from, to, recs, res.Breaks, fetch); err != nil {
			return err
		}
	}
	return nil
}

// split halves a job by stations, or by period for a single station.
func split(j job) (job, job, bool) {
	a, b := j, j
	if n := len(j.Req.Stations); n > 1 {
		a.Req.Stations = j.Req.Stations[:n/2]
		b.Req.Stations = j.Req.Stations[n/2:]
		return a, b, true
	}
	if j.Req.SameDaysEachYear {
		y0, y1 := j.Req.From.Year(), j.Req.To.Year()
		if y0 == y1 {
			return j, j, false
		}
		mid := y0 + (y1-y0)/2
		a.Req.To = j.Req.To.AddDate(mid-y1, 0, 0)
		b.Req.From = j.Req.From.AddDate(mid+1-y0, 0, 0)
		a.StoreTo, b.StoreFrom = a.Req.To, b.Req.From
		return a, b, true
	}
	days := int(j.StoreTo.Sub(j.StoreFrom).Hours() / 24)
	if days < 1 {
		return j, j, false
	}
	mid := j.StoreFrom.AddDate(0, 0, days/2)
	a.StoreTo, a.Req.To = mid, mid
	b.StoreFrom = mid.AddDate(0, 0, 1)
	b.Req.From = mid // one overlapping day for homogeneity breaks
	return a, b, true
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

// unit is one station to be requested with a set of elements.
type unit struct {
	station  string
	elements []obsdl.Element
}

func elementsKey(els []obsdl.Element) string {
	keys := make([]string, len(els))
	for i, e := range els {
		keys[i] = e.Key
	}
	return strings.Join(keys, ",")
}

// packMode tells how pack builds the request period.
type packMode int

const (
	continuous  packMode = iota // storeFrom..storeTo
	withOverlap                 // plus one leading day to detect homogeneity breaks
	sameDays                    // the month/day range of storeFrom..storeTo in every year
)

// pack groups units that share an element set into requests for the
// period [storeFrom, storeTo].
func (c *Crawler) pack(purpose string, f obsdl.Frequency, units []unit, storeFrom, storeTo time.Time, mode packMode) []job {
	return c.packWith(c.Budget, purpose, f, units, storeFrom, storeTo, mode)
}

// packWith is pack with a request size budget.
func (c *Crawler) packWith(budget int, purpose string, f obsdl.Frequency, units []unit, storeFrom, storeTo time.Time, mode packMode) []job {
	groups := map[string][]unit{}
	var keys []string
	for _, u := range units {
		k := elementsKey(u.elements)
		if _, ok := groups[k]; !ok {
			keys = append(keys, k)
		}
		groups[k] = append(groups[k], u)
	}
	sort.Strings(keys)
	reqFrom := storeFrom
	if mode == withOverlap {
		reqFrom = storeFrom.AddDate(0, 0, -1)
	}
	var jobs []job
	for _, k := range keys {
		us := groups[k]
		base := obsdl.TableRequest{Frequency: f, Elements: us[0].elements, From: reqFrom, To: storeTo,
			SameDaysEachYear: mode == sameDays, Stations: []string{"x"}}
		per := base.Cells()
		if per > budget && mode != sameDays && storeTo.After(storeFrom) {
			// One station alone is over budget: split the period up front
			// instead of letting the server time out and splitting afterwards.
			for _, p := range splitPeriod(f, len(us[0].elements), storeFrom, storeTo, mode, budget) {
				jobs = append(jobs, c.packWith(budget, purpose, f, us, p[0], p[1], mode)...)
			}
			continue
		}
		capacity := max(1, budget/per)
		capacity = min(capacity, c.MaxStations)
		for i := 0; i < len(us); i += capacity {
			req := base
			req.Stations = nil
			for _, u := range us[i:min(i+capacity, len(us))] {
				req.Stations = append(req.Stations, u.station)
			}
			jobs = append(jobs, job{Purpose: purpose, Req: req, StoreFrom: storeFrom, StoreTo: storeTo})
		}
	}
	return jobs
}

// splitPeriod divides [storeFrom, storeTo] into the fewest consecutive
// periods whose one-station request fits the budget. A period shorter than
// a day cannot be split, so a single day may still be over budget.
func splitPeriod(f obsdl.Frequency, elements int, storeFrom, storeTo time.Time, mode packMode, budget int) [][2]time.Time {
	days := int(storeTo.Sub(storeFrom).Hours()/24) + 1
	for n := 2; ; n++ {
		var parts [][2]time.Time
		fits := true
		for i := range n {
			from := storeFrom.AddDate(0, 0, days*i/n)
			to := storeFrom.AddDate(0, 0, days*(i+1)/n-1)
			req := obsdl.TableRequest{Frequency: f, Elements: make([]obsdl.Element, elements), Stations: []string{"x"}, From: from, To: to}
			if mode == withOverlap {
				req.From = from.AddDate(0, 0, -1)
			}
			fits = fits && req.Cells() <= budget
			parts = append(parts, [2]time.Time{from, to})
		}
		if fits || n >= days {
			return parts
		}
	}
}

// elementsFor returns the elements requested for a station.
func elementsFor(f obsdl.Frequency, st *store.StationRow) []obsdl.Element {
	if st.Official() {
		return obsdl.Elements(f)
	}
	cats := st.Categories()
	if cov := st.Coverage[obsdl.Daily]; cov != nil {
		for c := range cov.Categories {
			cats[c] = true
		}
	}
	return obsdl.ElementsFor(f, false, cats)
}

func (c *Crawler) runJobs(ctx context.Context, jobs []job) error {
	total := 0
	for _, j := range jobs {
		total += j.Req.Cells()
	}
	c.Log.Info("starting jobs", "jobs", len(jobs), "cells", total)
	for i, j := range jobs {
		if err := ctx.Err(); err != nil {
			return err
		}
		c.Log.Info("job", "progress", fmt.Sprintf("%d/%d", i+1, len(jobs)), "job", j.String())
		if err := c.run(ctx, j); err != nil {
			return fmt.Errorf("%s: %w", j.String(), err)
		}
	}
	return nil
}
