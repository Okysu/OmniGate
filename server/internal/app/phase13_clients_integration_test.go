package app_test

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// Realistic header sets of the clients the test sends as (phase13-api.md §1).
var (
	claudeCodeHeaders = []string{"User-Agent", "claude-cli/2.0.14 (external, cli)", "X-App", "cli", "Anthropic-Version", "2023-06-01",
		"Anthropic-Beta", "claude-code-20250219", "X-Stainless-Lang", "js", "X-Stainless-Package-Version", "0.60.0", "X-Stainless-Runtime", "node"}
	codexHeaders  = []string{"User-Agent", "codex_cli_rs/0.46.0 (Mac OS 15.6.1; arm64) iTerm.app/3.6.1", "Originator", "codex_cli_rs"}
	cherryHeaders = []string{"User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) CherryStudio/1.7.8 " +
		"Chrome/140.0.7339.249 Electron/38.7.0 Safari/537.36", "X-Title", "Cherry Studio", "X-Stainless-Lang", "js", "X-Stainless-Package-Version", "5.12.0"}
	curlHeaders = []string{"User-Agent", "curl/8.7.1"}
)

// send posts body to a gateway path with the given headers and returns the
// status.
func (a *affinityEnv) send(path, body string, headers ...string) int {
	a.t.Helper()
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, a.h.srv.URL+path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+a.key)
	req.Header.Set("Content-Type", "application/json")
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		a.t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return resp.StatusCode
}

func TestClientDetection(t *testing.T) {
	a := setupAffinity(t)

	// One rule limited to Cherry Studio (client_include): the same session
	// header from curl does not apply it.
	a.setAffinity(map[string]any{"enabled": true, "max_entries": 1000, "default_ttl_seconds": 600, "rules": []any{
		map[string]any{"name": "cherry only", "client_include": []string{"cherry-studio"},
			"key_sources": []any{map[string]any{"type": "request_header", "key": "X-Conv"}}},
	}})
	time.Sleep(20 * time.Millisecond)
	st := a.mustDo(a.admin, http.MethodGet, "/api/admin/settings", nil, 200)
	rule := st["settings"].(map[string]any)["gateway"].(map[string]any)["affinity"].(map[string]any)["rules"].([]any)[0].(map[string]any)
	if inc := rule["client_include"].([]any); len(inc) != 1 || inc[0] != "cherry-studio" {
		t.Fatalf("saved client_include = %v", rule["client_include"])
	}

	chat := `{"model":"m1","messages":[{"role":"user","content":"hi"}]}`
	for i := 0; i < 2; i++ {
		if code := a.send("/v1/messages", `{"model":"m1","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`, claudeCodeHeaders...); code != 200 {
			t.Fatalf("claude code = %d", code)
		}
		if code := a.send("/v1/responses", `{"model":"m1","input":"hi"}`, codexHeaders...); code != 200 {
			t.Fatalf("codex = %d", code)
		}
	}
	for i := 0; i < 3; i++ {
		if code := a.send("/v1/chat/completions", chat, append([]string{"X-Conv", "conv-1"}, cherryHeaders...)...); code != 200 {
			t.Fatalf("cherry studio = %d", code)
		}
	}
	if code := a.send("/v1/chat/completions", chat, append([]string{"X-Conv", "conv-1"}, curlHeaders...)...); code != 200 {
		t.Fatalf("curl = %d", code)
	}
	if code := a.send("/v1/chat/completions", chat); code != 200 { // Go's client: "Go-http-client/1.1"
		t.Fatalf("go client = %d", code)
	}
	if err := a.app.FlushLogs(context.Background()); err != nil {
		t.Fatal(err)
	}

	// Each log carries the client id, name and parsed version.
	logs := a.mustDo(a.admin, http.MethodGet, "/api/logs?pageSize=50", nil, 200)
	got := map[string]int{}
	for _, it := range logs["items"].([]any) {
		l := it.(map[string]any)
		c := l["client"].(map[string]any)
		id := c["id"].(string)
		got[id]++
		want := map[string][2]any{"claude-code": {"Claude Code", "2.0.14"}, "codex": {"Codex", "0.46.0"}, "cherry-studio": {"Cherry Studio", "1.7.8"},
			"curl": {"curl", "8.7.1"}, "go-http": {"Go net/http", nil}}[id]
		if c["name"] != want[0] || c["version"] != want[1] {
			t.Errorf("client = %v, want %v", c, want)
		}
		// client_include: only Cherry Studio requests get the rule.
		if (id == "cherry-studio") != (l["affinityRule"] == "cherry only") {
			t.Errorf("%s: affinity rule %v", id, l["affinityRule"])
		}
	}
	if got["claude-code"] != 2 || got["codex"] != 2 || got["cherry-studio"] != 3 || got["curl"] != 1 || got["go-http"] != 1 {
		t.Fatalf("clients = %v", got)
	}

	// Filter.
	for client, want := range map[string]float64{"claude-code": 2, "codex": 2, "cherry-studio": 3, "curl": 1, "unknown": 0} {
		if out := a.mustDo(a.admin, http.MethodGet, "/api/logs?client="+client, nil, 200); out["total"].(float64) != want {
			t.Errorf("client=%s: %v, want %v", client, out["total"], want)
		}
	}
	if resp, _ := a.admin.do(http.MethodGet, "/api/logs?client=claude", nil); resp.StatusCode != 422 {
		t.Fatalf("bad client filter = %d", resp.StatusCode)
	}

	// Stats breakdown: 80 of 100 prompt tokens were cache reads everywhere;
	// Cherry Studio's session: new, hit, hit → affinity hit rate 1.
	sum := a.mustDo(a.admin, http.MethodGet, "/api/stats/summary", nil, 200)
	rows := map[string]map[string]any{}
	for _, it := range sum["byClient"].([]any) {
		r := it.(map[string]any)
		rows[r["client"].(string)] = r
	}
	if len(rows) != 5 || sum["byClient"].([]any)[0].(map[string]any)["client"] != "cherry-studio" {
		t.Fatalf("byClient = %v", sum["byClient"])
	}
	for id, r := range rows {
		if rate := r["cacheHitRate"].(float64); rate < 0.79 || rate > 0.81 || r["errors"].(float64) != 0 || r["inputTokens"].(float64) != 100*r["requests"].(float64) {
			t.Errorf("%s row = %v", id, r)
		}
	}
	if c := rows["cherry-studio"]; c["requests"].(float64) != 3 || c["affinityHits"].(float64) != 2 || c["affinityHitRate"].(float64) != 1 {
		t.Errorf("cherry row = %v", c)
	}
	if c := rows["curl"]; c["affinityHitRate"] != nil || c["name"] != "curl" {
		t.Errorf("curl row = %v", c)
	}

	// The raw User-Agent is never stored, only the id and version.
	var leaked int
	if err := a.pool.QueryRow(context.Background(), `SELECT count(*) FROM request_logs WHERE client_version LIKE '%(%' OR client_version LIKE '%Mozilla%'
		OR client LIKE '%/%'`).Scan(&leaked); err != nil || leaked != 0 {
		t.Fatalf("raw user agent logged: %d %v", leaked, err)
	}

	// The known client list; unknown ids in client_include are refused.
	clients := a.mustDo(a.carol, http.MethodGet, "/api/clients", nil, 200)["items"].([]any)
	if len(clients) < 20 || clients[0].(map[string]any)["id"] != "claude-code" {
		t.Fatalf("clients = %v", clients)
	}
	st = a.mustDo(a.admin, http.MethodGet, "/api/admin/settings", nil, 200)
	resp, out := a.admin.do(http.MethodPatch, "/api/admin/settings", map[string]any{"version": st["version"],
		"settings": map[string]any{"gateway": map[string]any{"affinity": map[string]any{"rules": []any{map[string]any{"name": "x",
			"client_include": []string{"cherry"}, "key_sources": []any{map[string]any{"type": "anchor"}}}}}}}})
	msg, _ := out["error"].(map[string]any)["details"].(map[string]any)["gateway.affinity"].(string)
	if resp.StatusCode != 422 || !strings.Contains(msg, "rules[0].client_include") {
		t.Fatalf("invalid client_include = %d %v", resp.StatusCode, out)
	}
}
