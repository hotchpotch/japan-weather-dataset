package store

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/hotchpotch/japan-weather-dataset/internal/obsdl"
)

const accessSchema = `
CREATE TABLE IF NOT EXISTS access_events (
	time         TIMESTAMP NOT NULL,
	kind         VARCHAR NOT NULL,
	path         VARCHAR,
	http_status  INTEGER,
	hits         INTEGER,
	wait_ms      BIGINT,
	retry_after_ms BIGINT,
	interval_ms  BIGINT,
	message      VARCHAR
);
`

// RecordAccessEvent stores a rate limit / block event.
func (s *Store) RecordAccessEvent(ctx context.Context, ev obsdl.AccessEvent) error {
	_, err := s.DB.ExecContext(ctx, `INSERT INTO access_events VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		ev.Time, string(ev.Kind), ev.Path, ev.HTTPStatus, ev.Hits, ev.Wait.Milliseconds(),
		ev.RetryAfter.Milliseconds(), ev.Interval.Milliseconds(), ev.Message)
	return err
}

// RequestsSince counts the requests logged since t.
func (s *Store) RequestsSince(ctx context.Context, t time.Time) (int, error) {
	var n int
	err := s.DB.QueryRowContext(ctx, `SELECT count(*) FROM requests WHERE started_at >= ?`, t).Scan(&n)
	return n, err
}

// AccessState is the overall state written to access_status.json.
type AccessState string

const (
	AccessOK          AccessState = "ok"
	AccessRateLimited AccessState = "rate_limited"
	AccessBlocked     AccessState = "blocked"
	AccessMaintenance AccessState = "maintenance"
	AccessQuota       AccessState = "quota_exhausted"
)

// AccessStatus is the content of <data-dir>/access_status.json, meant for
// monitoring: a non-"ok" state tells that JMA restricted access (or the
// self-imposed quota was reached) and the crawler stopped or slowed down.
type AccessStatus struct {
	State     AccessState `json:"state"`
	Since     time.Time   `json:"since"`
	UpdatedAt time.Time   `json:"updated_at"`
	Command   string      `json:"command"`
	Interval  string      `json:"interval"`
	LastEvent *EventJSON  `json:"last_event,omitempty"`
	Message   string      `json:"message,omitempty"`
}

// EventJSON is an AccessEvent with readable durations.
type EventJSON struct {
	Time       time.Time `json:"time"`
	Kind       string    `json:"kind"`
	Path       string    `json:"path"`
	HTTPStatus int       `json:"http_status,omitempty"`
	Hits       int       `json:"hits,omitempty"`
	Wait       string    `json:"wait,omitempty"`
	RetryAfter string    `json:"retry_after,omitempty"`
	Interval   string    `json:"interval"`
	Message    string    `json:"message,omitempty"`
}

func NewEventJSON(ev obsdl.AccessEvent) *EventJSON {
	d := func(v time.Duration) string {
		if v == 0 {
			return ""
		}
		return v.String()
	}
	return &EventJSON{Time: ev.Time, Kind: string(ev.Kind), Path: ev.Path, HTTPStatus: ev.HTTPStatus, Hits: ev.Hits,
		Wait: d(ev.Wait), RetryAfter: d(ev.RetryAfter), Interval: ev.Interval.String(), Message: ev.Message}
}

func (s *Store) accessStatusPath() string { return filepath.Join(s.Dir, "access_status.json") }

// ReadAccessStatus returns the last written status, if any.
func (s *Store) ReadAccessStatus() (*AccessStatus, error) {
	b, err := os.ReadFile(s.accessStatusPath())
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var st AccessStatus
	if err := json.Unmarshal(b, &st); err != nil {
		return nil, err
	}
	return &st, nil
}

// WriteAccessStatus writes the status file atomically. Since is kept while
// the state does not change.
func (s *Store) WriteAccessStatus(st AccessStatus) error {
	st.UpdatedAt = time.Now()
	if prev, err := s.ReadAccessStatus(); err == nil && prev != nil && prev.State == st.State && !prev.Since.IsZero() {
		st.Since = prev.Since
	}
	if st.Since.IsZero() {
		st.Since = st.UpdatedAt
	}
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.accessStatusPath() + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.accessStatusPath())
}
