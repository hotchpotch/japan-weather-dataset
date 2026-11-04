package obsdl

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/text/encoding/japanese"
)

// fakeServer serves the given responses for show/table in order; the last
// one repeats.
type fakeResponse struct {
	status int
	body   string
	csv    bool
}

func newFakeClient(t *testing.T, responses []fakeResponse) (*Client, *[]AccessEvent, *int) {
	t.Helper()
	var mu sync.Mutex
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/index.php" {
			return
		}
		mu.Lock()
		resp := responses[min(n, len(responses)-1)]
		n++
		mu.Unlock()
		if resp.csv {
			w.Header().Set("Content-Disposition", `attachment; filename="data.csv"`)
		} else {
			w.Header().Set("Content-Type", "text/html; charset=UTF-8")
		}
		w.WriteHeader(resp.status)
		body := resp.body
		if resp.csv {
			body, _ = japanese.ShiftJIS.NewEncoder().String(body)
		}
		io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	var events []AccessEvent
	c := NewClient(Options{
		BaseURL:          srv.URL,
		MinInterval:      time.Millisecond,
		MaxInterval:      8 * time.Millisecond,
		RetryBase:        time.Millisecond,
		RateLimitBase:    time.Millisecond,
		RateLimitMaxWait: 4 * time.Millisecond,
		MaxRateLimitHits: 3,
		MaxRetries:       2,
		Logger:           slog.New(slog.NewTextHandler(io.Discard, nil)),
		OnAccessEvent:    func(e AccessEvent) { events = append(events, e) },
	})
	return c, &events, &n
}

func kinds(events []AccessEvent) string {
	var ks []string
	for _, e := range events {
		ks = append(ks, string(e.Kind))
	}
	return strings.Join(ks, ",")
}

const okCSV = "ダウンロードした時刻：2026/11/04 15:13:22\r\n"

func TestRateLimitThenRecover(t *testing.T) {
	c, events, n := newFakeClient(t, []fakeResponse{
		{status: 429, body: "<body>ただいまアクセスが集中しています。</body>"},
		{status: 200, body: okCSV, csv: true},
	})
	if _, err := c.do(context.Background(), "show/table", nil, true); err != nil {
		t.Fatal(err)
	}
	if got := kinds(*events); got != "slowed_down,rate_limited,recovered" {
		t.Errorf("events = %s", got)
	}
	if *n != 2 || c.Interval() != 8*time.Millisecond { // raised to at least 1s, capped by MaxInterval
		t.Errorf("requests=%d interval=%v", *n, c.Interval())
	}
}

func TestRateLimitGivesUp(t *testing.T) {
	c, events, n := newFakeClient(t, []fakeResponse{{status: 503, body: "busy"}})
	_, err := c.do(context.Background(), "show/table", nil, true)
	if !errors.Is(err, ErrRateLimited) {
		t.Fatalf("err = %v", err)
	}
	if *n != 4 || !strings.HasSuffix(kinds(*events), "gave_up") {
		t.Errorf("requests=%d events=%s", *n, kinds(*events))
	}
}

func TestBlockedStopsAtOnce(t *testing.T) {
	for _, r := range []fakeResponse{
		{status: 403, body: "<body>Request blocked.</body>"},
		{status: 200, body: "<body>現在、アクセスを制限しています。</body>"},
	} {
		c, events, n := newFakeClient(t, []fakeResponse{r})
		_, err := c.do(context.Background(), "show/table", nil, true)
		if !errors.Is(err, ErrBlocked) || *n != 1 || kinds(*events) != "blocked" {
			t.Errorf("status %d: err=%v requests=%d events=%s", r.status, err, *n, kinds(*events))
		}
	}
}

func TestMaintenanceStops(t *testing.T) {
	c, events, n := newFakeClient(t, []fakeResponse{{status: 200, body: "<body>メンテナンス中です</body>"}})
	_, err := c.do(context.Background(), "show/table", nil, true)
	if !errors.Is(err, ErrMaintenance) || *n != 1 || kinds(*events) != "maintenance" {
		t.Errorf("err=%v requests=%d events=%s", err, *n, kinds(*events))
	}
}

func TestStationJSONAsHTMLIsError(t *testing.T) {
	c, _, _ := newFakeClient(t, []fakeResponse{{status: 200, body: "<body>アクセスが拒否されました</body>"}})
	_, err := c.do(context.Background(), "top/station", nil, false)
	if !errors.Is(err, ErrBlocked) {
		t.Errorf("err = %v", err)
	}
}
