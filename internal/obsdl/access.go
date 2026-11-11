package obsdl

import (
	"errors"
	"net/http"
	"strings"
	"time"
)

var (
	// ErrRateLimited means the server keeps answering "too many requests"
	// (HTTP 429/503 or its busy page) even after backing off. The run should
	// stop and be retried later with a longer interval.
	ErrRateLimited = errors.New("obsdl: rate limited")
	// ErrBlocked means the server refuses access (HTTP 401/403/451 or an
	// access restriction page). Requests are not retried: sending more would
	// only make the restriction worse. Check the service before resuming.
	ErrBlocked = errors.New("obsdl: access blocked")
)

// AccessEventKind classifies events about access restrictions.
type AccessEventKind string

const (
	// EventRateLimited: the server answered "too many requests"; the client waits and retries.
	EventRateLimited AccessEventKind = "rate_limited"
	// EventSlowedDown: the interval between requests was raised.
	EventSlowedDown AccessEventKind = "slowed_down"
	// EventSpedUp: the interval between requests was lowered after a run of successes.
	EventSpedUp AccessEventKind = "sped_up"
	// EventRecovered: a request succeeded after being rate limited.
	EventRecovered AccessEventKind = "recovered"
	// EventGaveUp: rate limiting lasted too long; the request failed with ErrRateLimited.
	EventGaveUp AccessEventKind = "gave_up"
	// EventBlocked: access was refused; the request failed with ErrBlocked.
	EventBlocked AccessEventKind = "blocked"
	// EventMaintenance: the service is under maintenance; the request failed with ErrMaintenance.
	EventMaintenance AccessEventKind = "maintenance"
)

// AccessEvent reports a change of the access state. Clients pass them to
// Options.OnAccessEvent so that they can be logged and recorded.
type AccessEvent struct {
	Time       time.Time
	Kind       AccessEventKind
	Path       string
	HTTPStatus int    // 0 when the restriction came as an HTML page
	Message    string // text of the server's response
	RetryAfter time.Duration
	Wait       time.Duration // how long the client waits before the next attempt
	Interval   time.Duration // interval between requests after the event
	Hits       int           // consecutive rate-limited responses for this request
}

// rateLimitWords and blockWords are matched against error pages that come
// with HTTP 200 or without a telling status code.
var (
	rateLimitWords = []string{"アクセスが集中", "混み合", "too many requests", "rate limit"}
	blockWords     = []string{"アクセス制限", "アクセスを制限", "アクセスが制限", "アクセスを拒否", "アクセスが拒否",
		"アクセスを遮断", "access denied", "request blocked", "forbidden"}
)

// classifyAccess returns ErrRateLimited or ErrBlocked when an HTTP status
// or page text indicates an access restriction, or nil.
func classifyAccess(status int, msg string) error {
	switch status {
	case http.StatusTooManyRequests, http.StatusServiceUnavailable:
		return ErrRateLimited
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusUnavailableForLegalReasons:
		return ErrBlocked
	}
	lower := strings.ToLower(msg)
	for _, w := range blockWords {
		if strings.Contains(lower, w) {
			return ErrBlocked
		}
	}
	for _, w := range rateLimitWords {
		if strings.Contains(lower, w) {
			return ErrRateLimited
		}
	}
	return nil
}
