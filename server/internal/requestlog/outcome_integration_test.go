package requestlog_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"

	"omnigate/internal/auth"
	"omnigate/internal/authz"
	"omnigate/internal/identity"
	"omnigate/internal/platform/db/dbtest"
	"omnigate/internal/requestlog"
)

func TestHTTPOutcomesAcrossHistoryFiltersAndMetrics(t *testing.T) {
	d := dbtest.Open(t)
	w := requestlog.NewWriter(d, slog.New(slog.NewTextHandler(io.Discard, nil)))
	user := uuid.New()
	// A unique inbound label isolates this test from other process counters.
	inbound := "test." + uuid.NewString()
	statuses := []int{200, 304, 400, 401, 402, 403, 404, 405, 413, 429, 499, 500, 502, 504, 599, 200}
	for i, code := range statuses {
		e := &requestlog.Entry{ID: uuid.New(), RequestID: uuid.NewString(), StartedAt: time.Now().UTC().Add(-24 * time.Hour),
			UserID: &user, Inbound: inbound, Model: "history", StatusCode: code}
		if i == len(statuses)-1 {
			e.ErrorClass = ptr("upstream_invalid_response")
		} // in-band stream error remains HTTP 200
		w.Add(e)
	}
	if err := w.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p := &authz.Principal{UserID: user, Role: identity.RoleUser}
			next.ServeHTTP(w, r.WithContext(auth.WithPrincipal(r.Context(), p)))
		})
	})
	requestlog.NewHandler(d).Routes(r)
	get := func(path string) map[string]any {
		t.Helper()
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		if rec.Code != 200 {
			t.Fatalf("%s: %d %s", path, rec.Code, rec.Body.String())
		}
		var out map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	for filter, count := range map[string]float64{"success": 3, "rejected": 9, "error": 4} {
		out := get("/logs?status=" + filter)
		if out["total"] != count {
			t.Errorf("%s: %v", filter, out)
		}
		summary := get("/stats/summary?status=" + filter)["totals"].(map[string]any)
		if summary["requests"] != count {
			t.Errorf("filtered totals: %v", summary)
		}
	}
	summary := get("/stats/summary")
	totals := summary["totals"].(map[string]any)
	if totals["success"] != float64(3) || totals["successRate"] != 3.0/16 || totals["failureRate"] != 4.0/16 {
		t.Fatalf("success/rejected/failure mixed: %v", totals)
	}
	check := func(row map[string]any) {
		t.Helper()
		if row["requests"] != float64(16) || row["errors"] != float64(4) || row["rejected"] != float64(9) {
			t.Fatalf("wrong breakdown: %v", row)
		}
	}
	check(totals)
	for _, group := range []string{"daily", "byModel", "byChannel", "byClient"} {
		rows := summary[group].([]any)
		if len(rows) != 1 {
			t.Fatalf("%s: %v", group, rows)
		}
		check(rows[0].(map[string]any))
	}
	metrics, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]float64{}
	for _, family := range metrics {
		if family.GetName() != "omnigate_gateway_requests_total" {
			continue
		}
		for _, m := range family.Metric {
			var found, outcome string
			for _, label := range m.Label {
				if label.GetName() == "inbound" {
					found = label.GetValue()
				}
				if label.GetName() == "outcome" {
					outcome = label.GetValue()
				}
			}
			if found == inbound {
				counts[outcome] = m.GetCounter().GetValue()
			}
		}
	}
	if counts["success"] != 3 || counts["rejected"] != 9 || counts["error"] != 4 {
		t.Fatalf("Prometheus outcomes: %v", counts)
	}
}
