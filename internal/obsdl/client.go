// Package obsdl talks to JMA's "過去の気象データ・ダウンロード" service
// (https://www.data.jma.go.jp/risk/obsdl/). The service has no public API
// specification; requests mirror what its web form sends.
package obsdl

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

const DefaultBaseURL = "https://www.data.jma.go.jp/risk/obsdl/"

var (
	// ErrTooLarge means the server refused the request because it exceeds
	// the per-request data limit. Split the request and try again.
	ErrTooLarge = errors.New("obsdl: request exceeds the server data limit")
	// ErrMaintenance means the service is under maintenance.
	ErrMaintenance = errors.New("obsdl: service under maintenance")
	// ErrRejected means the server returned an error page for a request
	// it did not accept (e.g. a period that includes today).
	ErrRejected = errors.New("obsdl: request rejected")
	// ErrServer means the server failed with HTTP 500/502/504 twice. Heavy
	// requests (long periods for many stations) end this way after about a
	// minute; split them instead of sending them again.
	ErrServer = errors.New("obsdl: server error")
)

type Options struct {
	BaseURL   string
	UserAgent string
	// Interval is the initial pause between the end of a response and the
	// next request. It is doubled when the server reports that it is busy
	// and lowered again by SpeedUpStep after SpeedUpAfter successful
	// requests in a row, down to MinInterval.
	Interval time.Duration
	// MinInterval is the lower bound of the adaptive interval. 0 or a value
	// >= Interval keeps the interval from going below Interval.
	MinInterval time.Duration
	// SpeedUpAfter and SpeedUpStep control how the interval comes down.
	SpeedUpAfter int
	SpeedUpStep  time.Duration
	// HitHold is how long after a rate-limited response the interval stays
	// above the interval at which it happened (plus SpeedUpStep*2).
	HitHold time.Duration
	// SlowResponse: a response slower than this resets the run of
	// successes, so the interval does not come down while the server is
	// busy.
	SlowResponse time.Duration
	// Timeout of a single HTTP request.
	Timeout time.Duration
	// MaxRetries for transient failures (network errors, 5xx, error pages).
	MaxRetries int
	// RetryBase is the first backoff; it doubles on each retry.
	RetryBase time.Duration
	// MaxInterval caps the interval raised after rate limiting.
	MaxInterval time.Duration
	// RateLimitBase is the first wait after a rate-limited response; it
	// doubles on each consecutive hit up to RateLimitMaxWait.
	RateLimitBase    time.Duration
	RateLimitMaxWait time.Duration
	// MaxRateLimitHits is the number of consecutive rate-limited responses
	// to one request after which the client gives up with ErrRateLimited.
	MaxRateLimitHits int
	// OnAccessEvent receives rate limit / block events (also logged).
	OnAccessEvent func(AccessEvent)
	Logger        *slog.Logger
}

type Client struct {
	opts Options
	http *http.Client

	mu       sync.Mutex
	last     time.Time // end of the previous response
	interval time.Duration
	okStreak int
	hitIv    time.Duration // interval at the last rate-limited response
	hitAt    time.Time
	session  bool
}

func NewClient(opts Options) *Client {
	if opts.BaseURL == "" {
		opts.BaseURL = DefaultBaseURL
	}
	if !strings.HasSuffix(opts.BaseURL, "/") {
		opts.BaseURL += "/"
	}
	if opts.Timeout == 0 {
		opts.Timeout = 5 * time.Minute
	}
	if opts.RetryBase == 0 {
		opts.RetryBase = 30 * time.Second
	}
	if opts.MaxInterval == 0 {
		opts.MaxInterval = time.Minute
	}
	if opts.MinInterval <= 0 || opts.MinInterval > opts.Interval {
		opts.MinInterval = opts.Interval
	}
	if opts.SpeedUpAfter == 0 {
		opts.SpeedUpAfter = 50
	}
	if opts.SpeedUpStep == 0 {
		opts.SpeedUpStep = 500 * time.Millisecond
	}
	if opts.HitHold == 0 {
		opts.HitHold = time.Hour
	}
	if opts.SlowResponse == 0 {
		opts.SlowResponse = 30 * time.Second
	}
	if opts.RateLimitBase == 0 {
		opts.RateLimitBase = time.Minute
	}
	if opts.RateLimitMaxWait == 0 {
		opts.RateLimitMaxWait = 30 * time.Minute
	}
	if opts.MaxRateLimitHits == 0 {
		opts.MaxRateLimitHits = 6
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	jar, _ := cookiejar.New(nil)
	return &Client{opts: opts, interval: opts.Interval, http: &http.Client{Timeout: opts.Timeout, Jar: jar}}
}

// wait blocks until the interval has passed since the end of the previous
// response. Measuring from the end keeps a pause after slow responses.
func (c *Client) wait(ctx context.Context) error {
	c.mu.Lock()
	next := c.last.Add(c.interval)
	now := time.Now()
	if next.Before(now) {
		next = now
	}
	c.last = next
	c.mu.Unlock()
	if d := time.Until(next); d > 0 {
		t := time.NewTimer(d)
		defer t.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
		}
	}
	return nil
}

func (c *Client) newRequest(ctx context.Context, method, path string, form url.Values) (*http.Request, error) {
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequestWithContext(ctx, method, c.opts.BaseURL+path, body)
	if err != nil {
		return nil, err
	}
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	u, _ := url.Parse(c.opts.BaseURL)
	req.Header.Set("Origin", u.Scheme+"://"+u.Host)
	req.Header.Set("Referer", c.opts.BaseURL+"index.php")
	if c.opts.UserAgent != "" {
		req.Header.Set("User-Agent", c.opts.UserAgent)
	}
	return req, nil
}

// ensureSession opens the top page once so that the load balancer cookies
// are set, as a browser would.
func (c *Client) ensureSession(ctx context.Context) error {
	c.mu.Lock()
	done := c.session
	c.mu.Unlock()
	if done {
		return nil
	}
	if err := c.wait(ctx); err != nil {
		return err
	}
	req, err := c.newRequest(ctx, http.MethodGet, "index.php", nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		c.done()
		return err
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	c.done()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("obsdl: GET index.php: HTTP %d", resp.StatusCode)
	}
	c.mu.Lock()
	c.session = true
	c.mu.Unlock()
	return nil
}

// done records the end of a response; the next request waits from here.
func (c *Client) done() {
	c.mu.Lock()
	if now := time.Now(); now.After(c.last) {
		c.last = now
	}
	c.mu.Unlock()
}

// slowDown doubles the interval between requests after the server reported
// that it is busy, and returns the new interval.
func (c *Client) slowDown() (time.Duration, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.okStreak = 0
	c.hitIv, c.hitAt = c.interval, time.Now()
	next := min(max(c.interval*2, time.Second), c.opts.MaxInterval)
	if next <= c.interval {
		return c.interval, false
	}
	c.interval = next
	return next, true
}

// speedUp lowers the interval by SpeedUpStep after SpeedUpAfter fast
// successful requests in a row, not below MinInterval and, within HitHold
// of the last rate-limited response, not below the interval at which it
// happened plus two steps.
func (c *Client) speedUp(dur time.Duration) (time.Duration, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if dur > c.opts.SlowResponse {
		c.okStreak = 0
		return c.interval, false
	}
	c.okStreak++
	if c.okStreak < c.opts.SpeedUpAfter {
		return c.interval, false
	}
	c.okStreak = 0
	floor := c.opts.MinInterval
	if !c.hitAt.IsZero() && time.Since(c.hitAt) < c.opts.HitHold {
		floor = max(floor, c.hitIv+2*c.opts.SpeedUpStep)
	}
	next := max(c.interval-c.opts.SpeedUpStep, floor)
	if next >= c.interval {
		return c.interval, false
	}
	c.interval = next
	return next, true
}

// Interval returns the current interval between requests.
func (c *Client) Interval() time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.interval
}

// LastRateLimit returns the interval at the last rate-limited response and
// its time (zero if none in this run).
func (c *Client) LastRateLimit() (time.Duration, time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.hitIv, c.hitAt
}

// RestoreRateLimit sets the interval and the last rate-limited response
// remembered from an earlier run, so that a restart does not start faster
// than the server allowed. The interval is clamped to [Interval, MaxInterval].
func (c *Client) RestoreRateLimit(interval, hitInterval time.Duration, hitAt time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.interval = min(max(interval, c.interval), c.opts.MaxInterval)
	if time.Since(hitAt) < c.opts.HitHold {
		c.hitIv, c.hitAt = hitInterval, hitAt
	}
}

func (c *Client) emit(ev AccessEvent) {
	ev.Time = time.Now()
	ev.Interval = c.Interval()
	attrs := []any{"event", string(ev.Kind), "path", ev.Path, "http_status", ev.HTTPStatus,
		"hits", ev.Hits, "wait", ev.Wait, "retry_after", ev.RetryAfter, "interval", ev.Interval, "message", ev.Message}
	switch ev.Kind {
	case EventBlocked, EventGaveUp, EventMaintenance:
		c.opts.Logger.Error("obsdl access restricted", attrs...)
	case EventRecovered:
		c.opts.Logger.Info("obsdl access recovered", attrs...)
	case EventSpedUp:
		c.opts.Logger.Info("obsdl interval lowered", attrs...)
	default:
		c.opts.Logger.Warn("obsdl access limited", attrs...)
	}
	if c.opts.OnAccessEvent != nil {
		c.opts.OnAccessEvent(ev)
	}
}

// Response is a successful CSV download.
type Response struct {
	Body       []byte // raw CSV in Shift_JIS (CP932)
	StatusCode int
	Duration   time.Duration
}

type httpError struct {
	status     int
	retryAfter time.Duration
	msg        string
}

func (e *httpError) Error() string { return fmt.Sprintf("obsdl: HTTP %d: %s", e.status, e.msg) }

func (e *httpError) Unwrap() error {
	if err := classifyAccess(e.status, e.msg); err != nil {
		return err
	}
	switch e.status {
	case http.StatusInternalServerError, http.StatusBadGateway, http.StatusGatewayTimeout:
		return ErrServer
	}
	return nil
}

// pageError is an HTML error page returned with HTTP 200.
type pageError struct {
	kind error
	msg  string
}

func (e *pageError) Error() string { return fmt.Sprintf("%v: %s", e.kind, e.msg) }
func (e *pageError) Unwrap() error { return e.kind }

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// do sends a POST with retries. With wantAttachment, a 200 response
// without an attachment is an error page.
//
// Access restrictions are handled apart from other failures: a rate limit
// (HTTP 429/503 or a busy page) doubles the interval between requests and
// waits from RateLimitBase, doubling up to RateLimitMaxWait (or the
// server's Retry-After), and gives up with ErrRateLimited after
// MaxRateLimitHits consecutive hits. A block (HTTP 401/403/451 or an
// access restriction page) fails at once with ErrBlocked. Both are
// reported through Options.OnAccessEvent and the log.
func (c *Client) do(ctx context.Context, path string, form url.Values, wantAttachment bool) (*Response, error) {
	if err := c.ensureSession(ctx); err != nil {
		return nil, err
	}
	var lastErr error
	serverErrors, rateHits, attempt := 0, 0, 0
	for {
		resp, err := c.once(ctx, path, form, wantAttachment)
		if err == nil {
			if rateHits > 0 {
				c.emit(AccessEvent{Kind: EventRecovered, Path: path, HTTPStatus: resp.StatusCode, Hits: rateHits})
			} else if serverErrors == 0 && attempt == 0 {
				if _, lowered := c.speedUp(resp.Duration); lowered {
					c.emit(AccessEvent{Kind: EventSpedUp, Path: path, HTTPStatus: resp.StatusCode})
				}
			}
			return resp, nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		status, msg, retryAfter := errorDetails(err)
		switch {
		case errors.Is(err, ErrBlocked):
			c.emit(AccessEvent{Kind: EventBlocked, Path: path, HTTPStatus: status, Message: msg})
			return nil, err
		case errors.Is(err, ErrMaintenance):
			c.emit(AccessEvent{Kind: EventMaintenance, Path: path, HTTPStatus: status, Message: msg})
			return nil, err
		case errors.Is(err, ErrRateLimited):
			rateHits++
			if rateHits > c.opts.MaxRateLimitHits {
				c.emit(AccessEvent{Kind: EventGaveUp, Path: path, HTTPStatus: status, Message: msg, Hits: rateHits})
				return nil, fmt.Errorf("%w: %d consecutive responses: %v", ErrRateLimited, rateHits, err)
			}
			if iv, raised := c.slowDown(); raised {
				c.emit(AccessEvent{Kind: EventSlowedDown, Path: path, HTTPStatus: status, Hits: rateHits, Interval: iv})
			}
			wait := min(c.opts.RateLimitBase<<(rateHits-1), c.opts.RateLimitMaxWait)
			wait = max(wait, retryAfter)
			c.emit(AccessEvent{Kind: EventRateLimited, Path: path, HTTPStatus: status, Message: msg,
				RetryAfter: retryAfter, Wait: wait, Hits: rateHits})
			if err := sleepCtx(ctx, wait); err != nil {
				return nil, err
			}
			continue
		case errors.Is(err, ErrTooLarge), errors.Is(err, ErrRejected):
			return nil, err
		case errors.Is(err, ErrServer):
			serverErrors++
			c.mu.Lock()
			c.okStreak = 0 // the server is struggling: do not speed up yet
			c.mu.Unlock()
			if serverErrors >= 2 {
				return nil, err
			}
		}
		lastErr = err
		attempt++
		if attempt > c.opts.MaxRetries {
			return nil, lastErr
		}
		backoff := max(c.opts.RetryBase<<(attempt-1), retryAfter)
		c.opts.Logger.Warn("retrying obsdl request", "path", path, "attempt", attempt, "backoff", backoff, "err", err)
		if err := sleepCtx(ctx, backoff); err != nil {
			return nil, err
		}
	}
}

// errorDetails extracts the HTTP status, message and Retry-After of an error.
func errorDetails(err error) (int, string, time.Duration) {
	var he *httpError
	if errors.As(err, &he) {
		return he.status, he.msg, he.retryAfter
	}
	var pe *pageError
	if errors.As(err, &pe) {
		return http.StatusOK, pe.msg, 0
	}
	return 0, err.Error(), 0
}

func (c *Client) once(ctx context.Context, path string, form url.Values, wantAttachment bool) (*Response, error) {
	if err := c.wait(ctx); err != nil {
		return nil, err
	}
	req, err := c.newRequest(ctx, http.MethodPost, path, form)
	if err != nil {
		return nil, err
	}
	start := time.Now()
	resp, err := c.http.Do(req)
	if err != nil {
		c.done()
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	c.done()
	if err != nil {
		return nil, err
	}
	dur := time.Since(start)
	if resp.StatusCode != http.StatusOK {
		he := &httpError{status: resp.StatusCode, msg: pageMessage(body)}
		if s := resp.Header.Get("Retry-After"); s != "" {
			if n, err := strconv.Atoi(s); err == nil {
				he.retryAfter = time.Duration(n) * time.Second
			} else if t, err := http.ParseTime(s); err == nil {
				he.retryAfter = time.Until(t)
			}
		}
		return nil, he
	}
	if wantAttachment {
		if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(resp.Header.Get("Content-Disposition"))), "attachment") {
			return nil, classifyErrorPage(body)
		}
	} else if strings.Contains(resp.Header.Get("Content-Type"), "text/html") {
		// JSON endpoints answering with HTML are error pages.
		return nil, classifyErrorPage(body)
	}
	return &Response{Body: body, StatusCode: resp.StatusCode, Duration: dur}, nil
}

var (
	reScript = regexp.MustCompile(`(?is)<(script|style)[^>]*>.*?</(script|style)>`)
	reTag    = regexp.MustCompile(`(?s)<[^>]+>`)
	reSpace  = regexp.MustCompile(`\s+`)
)

// pageMessage extracts the visible message of an HTML error page.
func pageMessage(body []byte) string {
	s := string(body)
	if i := strings.Index(s, "<body"); i >= 0 {
		s = s[i:]
	}
	s = reScript.ReplaceAllString(s, " ")
	s = reTag.ReplaceAllString(s, " ")
	s = reSpace.ReplaceAllString(html.UnescapeString(s), " ")
	if i := strings.Index(s, "メニューページに戻る"); i >= 0 {
		s = s[i+len("メニューページに戻る"):]
	}
	if i := strings.Index(s, "気象庁ホームページについて"); i >= 0 {
		s = s[:i]
	}
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > 300 {
		s = string(r[:300]) // whole characters: the message is Japanese
	}
	return s
}

func classifyErrorPage(body []byte) error {
	msg := pageMessage(body)
	if err := classifyAccess(0, msg); err != nil {
		return &pageError{err, msg}
	}
	switch {
	case strings.Contains(msg, "許容範囲を超過"):
		return &pageError{ErrTooLarge, msg}
	case strings.Contains(msg, "メンテナンス"):
		return &pageError{ErrMaintenance, msg}
	case strings.Contains(msg, "やり直してください"):
		return &pageError{ErrRejected, msg}
	}
	return fmt.Errorf("obsdl: unexpected response: %s", msg)
}

// TableRequest is a CSV download request.
type TableRequest struct {
	Frequency Frequency
	Stations  []string // obsdl station ids such as "s47662" or "a0371"
	Elements  []Element
	From, To  time.Time // inclusive dates (JST calendar days)
	// SameDaysEachYear requests the From..To month/day range of every year
	// between From.Year() and To.Year() (obsdl "interAnnualType=2").
	SameDaysEachYear bool
}

func (r TableRequest) Form() url.Values {
	els := make([][2]string, len(r.Elements))
	for i, e := range r.Elements {
		els[i] = [2]string{e.ID, e.Opt}
	}
	stations, _ := json.Marshal(r.Stations)
	elements, _ := json.Marshal(els)
	ymd, _ := json.Marshal([]string{
		strconv.Itoa(r.From.Year()), strconv.Itoa(r.To.Year()),
		strconv.Itoa(int(r.From.Month())), strconv.Itoa(int(r.To.Month())),
		strconv.Itoa(r.From.Day()), strconv.Itoa(r.To.Day()),
	})
	interAnnual := "1"
	if r.SameDaysEachYear {
		interAnnual = "2"
	}
	return url.Values{
		"stationNumList":  {string(stations)},
		"aggrgPeriod":     {r.Frequency.AggrgPeriod()},
		"elementNumList":  {string(elements)},
		"interAnnualType": {interAnnual},
		"ymdList":         {string(ymd)},
		"option":          {`{"list":["obs"],"nyear":0}`},
		"downloadFlag":    {"true"},
		"rmkFlag":         {"1"}, // keep values that need care, with quality information
		"disconnectFlag":  {"1"}, // keep values before homogeneity breaks, with homogeneity numbers
		"youbiFlag":       {"0"},
		"fukenFlag":       {"0"},
		"kijiFlag":        {"0"},
		"csvFlag":         {"1"}, // flags and quality information as numbers
		"jikantaiFlag":    {"0"},
		"jikantaiList":    {"[]"},
		"ymdLiteral":      {"1"},
	}
}

// Table downloads a CSV table.
func (c *Client) Table(ctx context.Context, r TableRequest) (*Response, error) {
	return c.do(ctx, "show/table", r.Form(), true)
}
