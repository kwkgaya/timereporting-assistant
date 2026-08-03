package web

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kwkgaya/timereporting-assistant/internal/config"
	"github.com/kwkgaya/timereporting-assistant/internal/model"
)

// putStatus changes a day's status and returns the HTTP status code.
func putStatus(t *testing.T, base, date, status string) int {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPut, base+"/api/days/"+date,
		bytes.NewBufferString(`{"status":"`+status+`"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return -1
	}
	defer resp.Body.Close()
	return resp.StatusCode
}

// pingStatus issues GET /api/status on a goroutine and reports the code.
func pingStatus(base string) chan int {
	done := make(chan int, 1)
	go func() {
		resp, err := http.Get(base + "/api/status")
		if err != nil {
			done <- -1
			return
		}
		resp.Body.Close()
		done <- resp.StatusCode
	}()
	return done
}

// A status change runs the day builder, which shells out to git, Jira, GitHub
// and the calendar and can take minutes. It must not hold the global server
// mutex while doing so, or every other request blocks and the UI freezes.
func TestSlowDayBuilderDoesNotBlockOtherRequests(t *testing.T) {
	srv, _ := makeTestServer(t)
	srv.WithConfig(config.Config{WorkdayHours: 7, LeaveIssueKey: "LEAVE-1"}, "")
	release := make(chan struct{})
	entered := make(chan struct{}, 1)
	srv.WithDayBuilder(func(_ config.Config, d time.Time) (model.DayPlan, error) {
		select {
		case entered <- struct{}{}:
		default:
		}
		<-release
		return model.DayPlan{Date: d, Status: model.StatusWorking}, nil
	})
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	defer close(release)

	// Move to a non-working status first (this path does not invoke the builder).
	putStatus(t, ts.URL, "2026-06-01", string(model.StatusFullLeave))
	// Switching back to working invokes the slow builder.
	go putStatus(t, ts.URL, "2026-06-01", string(model.StatusWorking))

	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("day builder was never invoked")
	}

	select {
	case code := <-pingStatus(ts.URL):
		if code != http.StatusOK {
			t.Fatalf("GET /api/status = %d, want 200", code)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("GET /api/status blocked while the day builder was running")
	}
}

// A panic inside a handler must not strand the server mutex. Handlers here
// unlock explicitly rather than with defer, so an unguarded panic would wedge
// every subsequent request forever.
func TestPanicInHandlerDoesNotWedgeServer(t *testing.T) {
	srv, _ := makeTestServer(t)
	srv.WithConfig(config.Config{WorkdayHours: 7, LeaveIssueKey: "LEAVE-1"}, "")
	srv.WithDayBuilder(func(_ config.Config, _ time.Time) (model.DayPlan, error) {
		panic("simulated builder panic")
	})
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	putStatus(t, ts.URL, "2026-06-01", string(model.StatusFullLeave))
	putStatus(t, ts.URL, "2026-06-01", string(model.StatusWorking))

	select {
	case code := <-pingStatus(ts.URL):
		if code != http.StatusOK {
			t.Fatalf("GET /api/status = %d, want 200", code)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("server wedged after a handler panic — mutex was never released")
	}
}

// Submitting a row releases the mutex across the Jira call. If the day is
// replaced in the meantime (the SPA rebuilds incomplete days in the background),
// the captured row index goes stale and must not be used to slice blindly.
func TestSubmitRowSurvivesConcurrentDayRebuild(t *testing.T) {
	srv, _ := makeTestServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// Shrink the day to a single suggested row while row 1 is being submitted.
	go func() {
		time.Sleep(20 * time.Millisecond)
		req, _ := http.NewRequest(http.MethodPut, ts.URL+"/api/days/2026-06-01",
			bytes.NewBufferString(`{"suggested":[{"issueKey":"EDB-100","minutes":60,"comment":"only row","category":"activity"}]}`))
		req.Header.Set("Content-Type", "application/json")
		if resp, err := http.DefaultClient.Do(req); err == nil {
			resp.Body.Close()
		}
	}()

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/days/2026-06-01/rows/1/submit", nil)
	if resp, err := http.DefaultClient.Do(req); err == nil {
		resp.Body.Close()
	}

	select {
	case code := <-pingStatus(ts.URL):
		if code != http.StatusOK {
			t.Fatalf("GET /api/status = %d, want 200", code)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("server wedged after submit-row raced with a day rebuild")
	}
}
