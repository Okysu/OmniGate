package app_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// Responses WebSocket mode (docs/contracts/phase16-api.md) end to end against
// an OpenAI Chat upstream: response.create runs through the regular pipeline
// (conversion, logs), events come back one per frame, previous_response_id
// chains from the connection cache even though the upstream keeps no state,
// generate:false warms up, and named lanes tag their events.
func TestResponsesWebSocket(t *testing.T) {
	e := setupGateway(t)
	var mu sync.Mutex
	var bodies []map[string]any
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		mu.Lock()
		bodies = append(bodies, req)
		n := len(bodies)
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		sse(w, fmt.Sprintf(`{"id":"c%d","object":"chat.completion.chunk","model":"m1","choices":[{"index":0,"delta":{"role":"assistant","content":"answer %d"}}]}`, n, n))
		sse(w, `{"id":"c","object":"chat.completion.chunk","model":"m1","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`)
		sse(w, `{"id":"c","object":"chat.completion.chunk","model":"m1","choices":[],"usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7}}`)
		sse(w, "[DONE]")
	}))
	t.Cleanup(up.Close)
	e.channel(e.admin, map[string]any{"name": "chat", "type": "openai", "baseUrl": up.URL + "/v1", "models": models("m1")})
	_, key := e.key(e.admin, map[string]any{"name": "ws"})
	wsURL := "ws" + strings.TrimPrefix(e.h.srv.URL, "http") + "/v1/responses"
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	// A bad key is rejected at the handshake.
	if _, resp, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer og-bad"}}}); err == nil || resp == nil || resp.StatusCode != 401 {
		t.Fatalf("bad key handshake = %v %v", resp, err)
	}
	// A plain GET explains that an upgrade is needed.
	if code, _, raw := readBody(mustGet(t, e.h.srv.URL+"/v1/responses", key)); code != 400 || !strings.Contains(raw, "WebSocket") {
		t.Fatalf("plain GET = %d %s", code, raw)
	}

	conn, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer " + key}}})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	send := func(ev map[string]any) {
		b, _ := json.Marshal(ev)
		if err := conn.Write(ctx, websocket.MessageText, b); err != nil {
			t.Fatal(err)
		}
	}
	// until reads events until one of type typ (for lane, when set) arrives.
	until := func(typ, lane string) (map[string]any, []map[string]any) {
		var seen []map[string]any
		for {
			_, data, err := conn.Read(ctx)
			if err != nil {
				t.Fatalf("read: %v (seen %v)", err, seen)
			}
			var ev map[string]any
			if err := json.Unmarshal(data, &ev); err != nil {
				t.Fatalf("frame is not JSON: %s", data)
			}
			seen = append(seen, ev)
			if ev["type"] == "error" && typ != "error" {
				t.Fatalf("error event: %v", ev)
			}
			if ev["type"] == typ && (lane == "" || ev["stream_id"] == lane) {
				return ev, seen
			}
		}
	}

	send(map[string]any{"type": "response.create", "model": "m1", "store": false, "input": "first question"})
	done, seen := until("response.completed", "")
	resp1 := done["response"].(map[string]any)
	id1 := resp1["id"].(string)
	if seen[0]["type"] != "response.created" || !strings.Contains(fmt.Sprint(seen), "response.output_text.delta") ||
		!strings.Contains(fmt.Sprint(resp1["output"]), "answer 1") {
		t.Fatalf("first turn events = %v", seen)
	}

	// Chaining: only the new input is sent, the gateway expands the history.
	send(map[string]any{"type": "response.create", "model": "m1", "store": false, "previous_response_id": id1,
		"input": []any{map[string]any{"role": "user", "content": "second question"}}})
	until("response.completed", "")
	mu.Lock()
	second := fmt.Sprint(bodies[1]["messages"])
	mu.Unlock()
	if !strings.Contains(second, "first question") || !strings.Contains(second, "answer 1") || !strings.Contains(second, "second question") {
		t.Fatalf("second turn upstream messages = %s", second)
	}

	// generate:false warms up without an upstream call; the next turn chains from it.
	send(map[string]any{"type": "response.create", "model": "m1", "generate": false, "input": "context only"})
	warm, _ := until("response.completed", "")
	warmID := warm["response"].(map[string]any)["id"].(string)
	mu.Lock()
	calls := len(bodies)
	mu.Unlock()
	if calls != 2 {
		t.Fatalf("warm-up called the upstream (%d calls)", calls)
	}
	send(map[string]any{"type": "response.create", "model": "m1", "previous_response_id": warmID, "stream_id": "lane-a", "input": "go"})
	ev, _ := until("response.completed", "lane-a")
	if ev["stream_id"] != "lane-a" {
		t.Fatalf("lane event = %v", ev)
	}
	mu.Lock()
	third := fmt.Sprint(bodies[2]["messages"])
	mu.Unlock()
	if !strings.Contains(third, "context only") || !strings.Contains(third, "go") {
		t.Fatalf("warm-up chain upstream messages = %s", third)
	}

	// Errors are events and keep the connection open.
	send(map[string]any{"type": "response.create", "model": "nope", "input": "x"})
	errEv, _ := until("error", "")
	if errEv["status"].(float64) != 404 || errEv["error"].(map[string]any)["code"] != "model_not_found" {
		t.Fatalf("error event = %v", errEv)
	}
	send(map[string]any{"type": "session.update"})
	if errEv, _ := until("error", ""); errEv["error"].(map[string]any)["code"] != "unknown_event_type" {
		t.Fatalf("unknown event = %v", errEv)
	}

	// Every turn that reached the pipeline is a regular request log.
	e.settle()
	logs := e.mustDo(e.admin, http.MethodGet, "/api/logs?model=m1", nil, 200)
	if logs["total"].(float64) != 3 || logs["items"].([]any)[0].(map[string]any)["inbound"] != "openai.responses" {
		t.Fatalf("logs = %v", logs)
	}
}

func mustGet(t *testing.T, url, key string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}
