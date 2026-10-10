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

	"omnigate/internal/auth"
	"omnigate/internal/authz"
	"omnigate/internal/identity"
	"omnigate/internal/money"
	"omnigate/internal/platform/db/dbtest"
	"omnigate/internal/protocol"
	"omnigate/internal/requestlog"
)

func ptr[T any](v T) *T { return &v }

// TestClientFilterAndBreakdown covers the client field of request logs, the
// client filter and the per-client summary breakdown (phase13-api.md §3–4).
func TestClientFilterAndBreakdown(t *testing.T) {
	ctx := context.Background()
	d := dbtest.Open(t)
	w := requestlog.NewWriter(d, slog.New(slog.NewTextHandler(io.Discard, nil)))
	me, other := uuid.New(), uuid.New()
	at := time.Now().UTC().Add(-time.Hour)
	add := func(user uuid.UUID, client, version *string, status int, usage protocol.Usage, aff *string, charge money.Amount) {
		at = at.Add(time.Second)
		w.Add(&requestlog.Entry{ID: uuid.Must(uuid.NewV7()), StartedAt: at, RequestID: "r", UserID: &user, Inbound: protocol.OpenAIChat,
			Model: "m1", StatusCode: status, Usage: usage, Affinity: aff, Charge: charge, Client: client, ClientVersion: version})
	}
	cached := protocol.Usage{Input: 20, CacheRead: 80, Output: 5}
	cold := protocol.Usage{Input: 90, CacheWrite: 10, Output: 5}
	add(me, ptr("claude-code"), ptr("2.0.14"), 200, cached, ptr("new"), money.Amount(1_000_000_000))
	add(me, ptr("claude-code"), ptr("2.0.14"), 200, cached, ptr("hit"), money.Amount(500_000_000))
	add(me, ptr("claude-code"), ptr("2.0.14"), 502, protocol.Usage{}, ptr("failover"), 0)
	add(me, ptr("codex"), nil, 200, cold, ptr("hit"), 0)
	add(me, ptr("codex"), nil, 200, cold, nil, 0)
	add(me, ptr("unknown"), nil, 200, cold, nil, 0)
	add(me, nil, nil, 200, cold, nil, 0) // logged before client detection
	add(other, ptr("cherry-studio"), ptr("1.7.8"), 200, cold, nil, 0)
	if err := w.Flush(ctx); err != nil {
		t.Fatal(err)
	}

	r := chi.NewRouter()
	var principal *authz.Principal
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(auth.WithPrincipal(r.Context(), principal)))
		})
	})
	requestlog.NewHandler(d).Routes(r)
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	get := func(path string, want int) map[string]any {
		t.Helper()
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&out)
		if resp.StatusCode != want {
			t.Fatalf("GET %s = %d, want %d: %v", path, resp.StatusCode, want, out)
		}
		return out
	}

	// The known client list.
	principal = &authz.Principal{UserID: me, Role: identity.RoleUser}
	items := get("/clients", 200)["items"].([]any)
	if first := items[0].(map[string]any); first["id"] != "claude-code" || first["name"] != "Claude Code" || first["kind"] != "agent" ||
		items[len(items)-1].(map[string]any)["id"] != "unknown" {
		t.Fatalf("clients = %v", items)
	}

	// List items carry the client; NULL reads as unknown. A user sees only
	// their own requests.
	all := get("/logs?pageSize=50", 200)
	if all["total"].(float64) != 7 {
		t.Fatalf("total = %v", all["total"])
	}
	byID := map[string]int{}
	for _, it := range all["items"].([]any) {
		c := it.(map[string]any)["client"].(map[string]any)
		byID[c["id"].(string)]++
		switch c["id"] {
		case "claude-code":
			if c["name"] != "Claude Code" || c["version"] != "2.0.14" {
				t.Errorf("claude-code client = %v", c)
			}
		case "codex", "unknown":
			if c["version"] != nil {
				t.Errorf("version = %v", c)
			}
		}
	}
	if byID["claude-code"] != 3 || byID["codex"] != 2 || byID["unknown"] != 2 || byID["cherry-studio"] != 0 {
		t.Fatalf("clients in list = %v", byID)
	}

	// Filter.
	for client, want := range map[string]float64{"claude-code": 3, "codex": 2, "unknown": 2, "cherry-studio": 0, "curl": 0} {
		if got := get("/logs?client="+client, 200)["total"].(float64); got != want {
			t.Errorf("client=%s: total %v, want %v", client, got, want)
		}
	}
	for _, bad := range []string{"bogus", "Claude%20Code", "CLAUDE-CODE"} {
		get("/logs?client="+bad, 422)
		get("/stats/summary?client="+bad, 422)
	}

	// Breakdown: sorted by requests; prompt = input + cache read + cache write.
	sum := get("/stats/summary", 200)
	rows := sum["byClient"].([]any)
	if len(rows) != 3 {
		t.Fatalf("byClient = %v", rows)
	}
	cc, codex, unknown := rows[0].(map[string]any), rows[1].(map[string]any), rows[2].(map[string]any)
	if cc["client"] != "claude-code" || cc["name"] != "Claude Code" || cc["requests"].(float64) != 3 || cc["errors"].(float64) != 1 ||
		cc["inputTokens"].(float64) != 200 || cc["outputTokens"].(float64) != 10 || cc["cacheReadTokens"].(float64) != 160 ||
		cc["cacheHitRate"].(float64) != 0.8 || cc["charge"] != "1.5" {
		t.Errorf("claude-code row = %v", cc)
	}
	// Affinity: hit ÷ (hit + rebound + failover + broken + strict_failed); new is not bound.
	if cc["affinityHits"].(float64) != 1 || cc["affinityBound"].(float64) != 2 || cc["affinityHitRate"].(float64) != 0.5 {
		t.Errorf("claude-code affinity = %v", cc)
	}
	if codex["client"] != "codex" || codex["requests"].(float64) != 2 || codex["cacheHitRate"].(float64) != 0 ||
		codex["affinityHitRate"].(float64) != 1 || codex["cacheWriteTokens"].(float64) != 20 {
		t.Errorf("codex row = %v", codex)
	}
	if unknown["client"] != "unknown" || unknown["name"] != "未知" || unknown["requests"].(float64) != 2 || unknown["affinityHitRate"] != nil {
		t.Errorf("unknown row = %v", unknown)
	}
	// The breakdown honours the other filters, including client.
	if rows := get("/stats/summary?client=codex", 200)["byClient"].([]any); len(rows) != 1 || rows[0].(map[string]any)["client"] != "codex" {
		t.Errorf("filtered byClient = %v", rows)
	}
	if rows := get("/stats/summary?status=error", 200)["byClient"].([]any); len(rows) != 1 || rows[0].(map[string]any)["requests"].(float64) != 1 {
		t.Errorf("errors byClient = %v", rows)
	}

	// stats.all sees every user's clients.
	principal = &authz.Principal{UserID: uuid.New(), Role: identity.RoleSystemAdmin}
	if rows := get("/stats/summary", 200)["byClient"].([]any); len(rows) != 4 {
		t.Errorf("admin byClient = %v", rows)
	}
	if got := get("/logs?client=cherry-studio", 200); got["total"].(float64) != 1 ||
		got["items"].([]any)[0].(map[string]any)["client"].(map[string]any)["version"] != "1.7.8" {
		t.Errorf("admin cherry-studio = %v", got)
	}
}
