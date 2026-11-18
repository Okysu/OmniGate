package app_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"omnigate/internal/control"
)

func TestControlActionsAndExactIdentity(t *testing.T) {
	for _, action := range []string{"audit", "block", "notify", "ban"} {
		t.Run(action, func(t *testing.T) {
			e := setupGateway(t)
			ctx := context.Background()
			up := newFakeUpstream(t)
			ch := e.channel(e.admin, map[string]any{"name": "受控渠道", "type": "openai", "scope": "global", "baseUrl": up.srv.URL + "/v1", "models": models("m1")})
			uid := e.userID(e.carol)
			kid, key := e.key(e.carol, map[string]any{"name": "测试 Key"})
			_, key2 := e.key(e.carol, map[string]any{"name": "其他 Key"})
			var hits atomic.Int64
			jev := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				if r.URL.Path != "/custom/v1/systemone" || r.Header.Get("Authorization") != "Bearer jev-test-secret" {
					t.Errorf("invalid Jev request %s", r.URL.Path)
				}
				var in map[string]any
				if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
					t.Error(err)
				}
				if !strings.Contains(fmt.Sprint(in["state"]), "hi") {
					t.Error("missing text")
				}
				fmt.Fprint(w, `{"answers":{"c0":{"noul":0.98}}}`)
			}))
			defer jev.Close()
			cfg := control.DefaultConfig()
			cfg.Enabled = true
			cfg.BaseURL = jev.URL + "/custom/v1"
			cfg.Categories = []string{"jailbreak | 是否请求绕过规则？"}
			cfg.Rules = []control.Rule{{ID: uuid.NewString(), Name: "测试规则", Enabled: true, ChannelIDs: []string{ch}, Models: []string{`re:^m1$`}, Action: action, NotifyAdmins: true, NotifyUser: true}}
			secret := "jev-test-secret"
			saved := e.mustDo(e.admin, http.MethodPut, "/api/admin/control/settings", control.SaveInput{Config: cfg, APIKey: &secret}, 200)
			if saved["apiKey"] != nil || saved["apiKeySet"] != true || strings.Contains(stringify(saved), secret) {
				t.Fatal("key leaked")
			}
			code, _, raw := readBody(gwPost(t, ctx, e.h.srv.URL, "/v1/chat/completions", key, chatBody))
			want := 200
			if action == "ban" || action == "block" {
				want = 403
			}
			if code != want {
				t.Fatalf("gateway=%d %s", code, raw)
			}
			if action == "ban" || action == "block" {
				if up.hits.Load() != 0 {
					t.Fatal("blocked request reached upstream")
				}
			}
			if err := e.app.FlushLogs(ctx); err != nil {
				t.Fatal(err)
			}
			events := e.mustDo(e.admin, http.MethodGet, "/api/admin/control/events?userId="+uid, nil, 200)
			items := events["items"].([]any)
			if len(items) != 1 {
				t.Fatalf("events=%v", events)
			}
			ev := items[0].(map[string]any)
			if ev["userId"] != uid || ev["keyId"] != kid || ev["channelId"] != ch || ev["action"] != action || ev["actionState"] != "completed" || ev["actionError"] != "" || !strings.EqualFold(fmt.Sprint(ev["userName"]), "carol") {
				t.Fatalf("identity=%v", ev)
			}
			logs := e.mustDo(e.admin, http.MethodGet, "/api/logs?userId="+uid, nil, 200)["items"].([]any)
			if len(logs) != 1 || logs[0].(map[string]any)["requestId"] != ev["requestId"] {
				t.Fatalf("request association logs=%v event=%v", logs, ev)
			}
			adminMsgs := e.notifs(e.admin, "admin.message")
			if len(adminMsgs) != 1 || !strings.Contains(fmt.Sprint(adminMsgs[0]["body"]), uid) {
				t.Fatalf("admin notification=%v", adminMsgs)
			}
			detail := e.user(uid)
			status := detail["user"].(map[string]any)["status"]
			if action == "ban" {
				if status != "disabled" || detail["sessions"].(map[string]any)["active"] != float64(0) {
					t.Fatalf("suspension=%v", detail)
				}
				for _, k := range []string{key, key2} {
					code, b, raw := readBody(gwPost(t, ctx, e.h.srv.URL, "/v1/chat/completions", k, chatBody))
					if code != 403 || b["error"].(map[string]any)["code"] != "account_disabled" {
						t.Fatalf("key remains usable %d %s", code, raw)
					}
				}
			} else if status != "active" {
				t.Fatalf("unexpected ban %s", action)
			}
			if hits.Load() != 1 {
				t.Fatalf("Jev called %d times", hits.Load())
			}
			var notices int
			if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE user_id=$1 AND type='admin.message'`, uid).Scan(&notices); err != nil || notices != 1 {
				t.Fatalf("user notice=%d %v", notices, err)
			}
		})
	}
}

func TestControlRetryDoesNotRepeatScreenOrNotification(t *testing.T) {
	e := setupGateway(t)
	ctx := context.Background()
	up := newFakeUpstream(t)
	up.setMode("500")
	ch := e.channel(e.admin, map[string]any{"name": "重试渠道", "type": "openai", "scope": "global", "baseUrl": up.srv.URL + "/v1", "models": models("m1")})
	_, key := e.key(e.carol, map[string]any{"name": "retry"})
	var calls atomic.Int64
	jev := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		fmt.Fprint(w, `{"answers":{"c0":{"noul":0.98}}}`)
	}))
	defer jev.Close()
	cfg := control.DefaultConfig()
	cfg.Enabled = true
	cfg.BaseURL = jev.URL
	cfg.Categories = []string{"custom"}
	cfg.Rules = []control.Rule{{ID: uuid.NewString(), Name: "审计", Enabled: true, ChannelIDs: []string{ch}, Action: "audit", NotifyAdmins: true}}
	secret := "test"
	e.mustDo(e.admin, http.MethodPut, "/api/admin/control/settings", control.SaveInput{Config: cfg, APIKey: &secret}, 200)
	code, _, _ := readBody(gwPost(t, ctx, e.h.srv.URL, "/v1/chat/completions", key, chatBody))
	if code < 500 || up.hits.Load() < 2 || calls.Load() != 1 {
		t.Fatalf("retry status=%d upstream=%d classifier=%d", code, up.hits.Load(), calls.Load())
	}
	if msgs := e.notifs(e.admin, "admin.message"); len(msgs) != 1 {
		t.Fatalf("repeated notifications=%v", msgs)
	}
	if events := e.mustDo(e.admin, http.MethodGet, "/api/admin/control/events", nil, 200); events["total"] != float64(1) {
		t.Fatalf("repeated records=%v", events)
	}
}

func TestControlThresholdAndAdminProtection(t *testing.T) {
	e := setupGateway(t)
	ctx := context.Background()
	up := newFakeUpstream(t)
	ch := e.channel(e.admin, map[string]any{"name": "渠道", "type": "openai", "scope": "global", "baseUrl": up.srv.URL + "/v1", "models": models("m1")})
	_, key := e.key(e.admin, map[string]any{"name": "admin key"})
	var probability atomic.Value
	probability.Store("0.69")
	jev := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"answers":{"c0":{"noul":%s}}}`, probability.Load())
	}))
	defer jev.Close()
	cfg := control.DefaultConfig()
	cfg.Enabled = true
	cfg.BaseURL = jev.URL
	cfg.Categories = []string{"custom"}
	cfg.StoreExcerpt = false
	cfg.Rules = []control.Rule{{ID: uuid.NewString(), Name: "阈值测试", Enabled: true, ChannelIDs: []string{ch}, Action: "ban", NotifyAdmins: true}}
	secret := "test"
	input := control.SaveInput{Config: cfg, APIKey: &secret}
	bad := input
	bad.Rules = append([]control.Rule{}, input.Rules...)
	bad.Rules[0].Models = []string{"re:["}
	e.mustDo(e.admin, http.MethodPut, "/api/admin/control/settings", bad, 422)
	e.mustDo(e.admin, http.MethodPut, "/api/admin/control/settings", input, 200)
	code, _, raw := readBody(gwPost(t, ctx, e.h.srv.URL, "/v1/chat/completions", key, chatBody))
	if code != 200 {
		t.Fatalf("below threshold=%d %s", code, raw)
	}
	if got := e.mustDo(e.admin, http.MethodGet, "/api/admin/control/events", nil, 200); got["total"] != float64(0) {
		t.Fatalf("below threshold stored=%v", got)
	}
	probability.Store("0.70")
	code, _, _ = readBody(gwPost(t, ctx, e.h.srv.URL, "/v1/chat/completions", key, chatBody))
	if code != 403 {
		t.Fatalf("threshold not inclusive: %d", code)
	}
	got := e.mustDo(e.admin, http.MethodGet, "/api/admin/control/events", nil, 200)
	items := got["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("threshold events=%v", got)
	}
	event := items[0].(map[string]any)
	if event["excerpt"] != "" || event["actionState"] != "failed" || event["actionError"] == "" {
		t.Fatalf("admin safeguard=%v", event)
	}
	if e.user(e.userID(e.admin))["user"].(map[string]any)["status"] != "active" {
		t.Fatal("system administrator suspended")
	}
}

func TestControlFailureScopeAndSettingsSecurity(t *testing.T) {
	e := setupGateway(t)
	ctx := context.Background()
	up := newFakeUpstream(t)
	ch := e.channel(e.admin, map[string]any{"name": "受控渠道", "type": "openai", "scope": "global", "baseUrl": up.srv.URL + "/v1", "models": models("m1", "m2")})
	uid := e.userID(e.carol)
	_, key := e.key(e.carol, map[string]any{"name": "Key"})
	var hits atomic.Int64
	jev := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1); http.Error(w, "private upstream body", 500) }))
	defer jev.Close()
	cfg := control.DefaultConfig()
	cfg.Enabled = true
	cfg.BaseURL = jev.URL
	cfg.Categories = []string{"check"}
	cfg.Rules = []control.Rule{{ID: uuid.NewString(), Name: "自动处理", Enabled: true, ChannelIDs: []string{ch}, Models: []string{"m1"}, Action: "ban", NotifyAdmins: true}}
	secret := "secret-value"
	input := control.SaveInput{Config: cfg, APIKey: &secret}
	e.mustDo(e.carol, http.MethodGet, "/api/admin/control/settings", nil, 403)
	e.mustDo(e.carol, http.MethodPut, "/api/admin/control/settings", input, 403)
	e.mustDo(e.carol, http.MethodGet, "/api/admin/control/events", nil, 403)
	saved := e.mustDo(e.admin, http.MethodPut, "/api/admin/control/settings", input, 200)
	code, _, _ := readBody(gwPost(t, ctx, e.h.srv.URL, "/v1/chat/completions", key, `{"model":"m2","messages":[{"role":"user","content":"hi"}]}`))
	if code != 200 || hits.Load() != 0 {
		t.Fatal("model exclusion failed")
	}
	code, _, _ = readBody(gwPost(t, ctx, e.h.srv.URL, "/v1/chat/completions", key, chatBody))
	if code != 200 {
		t.Fatalf("fail-open=%d", code)
	}
	input.APIKey = nil
	input.Version = int(saved["version"].(float64))
	input.FailClosed = true
	e.mustDo(e.admin, http.MethodPut, "/api/admin/control/settings", input, 200)
	code, _, _ = readBody(gwPost(t, ctx, e.h.srv.URL, "/v1/chat/completions", key, chatBody))
	if code != 403 {
		t.Fatalf("fail-closed=%d", code)
	}
	if e.user(uid)["user"].(map[string]any)["status"] != "active" {
		t.Fatal("classifier failure banned user")
	}
	if msgs := e.notifs(e.admin, "admin.message"); len(msgs) != 0 {
		t.Fatalf("failure falsely reported as user hit: %v", msgs)
	}
	events := e.mustDo(e.admin, http.MethodGet, "/api/admin/control/events?userId="+uid, nil, 200)
	if events["total"] != float64(2) {
		t.Fatalf("failure events=%v", events)
	}
	for _, item := range events["items"].([]any) {
		ev := item.(map[string]any)
		if ev["failure"] != true || ev["reason"] != "http_500" || strings.Contains(stringify(ev), "private upstream") {
			t.Fatalf("failure=%v", ev)
		}
	}
	e.mustDo(e.admin, http.MethodPut, "/api/admin/control/settings", input, 409)
	var raw string
	if err := e.pool.QueryRow(ctx, `SELECT secret FROM control_settings WHERE id=1`).Scan(&raw); err != nil || strings.Contains(raw, secret) {
		t.Fatalf("unencrypted secret %v", err)
	}
	e.mustDo(e.admin, http.MethodDelete, "/api/admin/control/events", nil, 204)
	if got := e.mustDo(e.admin, http.MethodGet, "/api/admin/control/events", nil, 200); got["total"] != float64(0) {
		t.Fatal("clear failed")
	}
}

func TestControlSnapshotsAndReviewOverrides(t *testing.T) {
	e := setupGateway(t)
	ctx := context.Background()
	up := newFakeUpstream(t)
	ch := e.channel(e.admin, map[string]any{"name": "受控渠道", "type": "openai", "scope": "global", "baseUrl": up.srv.URL + "/v1", "models": models("m1")})
	uid := e.userID(e.carol)
	_, key := e.key(e.carol, map[string]any{"name": "Key"})
	jev := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"answers":{"c0":{"noul":0.98}}}`)
	}))
	defer jev.Close()
	var reviewCalls atomic.Int64
	reviewer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reviewCalls.Add(1)
		var req struct {
			Model    string
			Messages []struct{ Content string }
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
		}
		if req.Model != "up-reviewer" || len(req.Messages) != 2 || !strings.Contains(req.Messages[1].Content, "原文快照") ||
			strings.Contains(req.Messages[1].Content, "binarySecret") {
			t.Error("unexpected review input")
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"review","object":"chat.completion","model":"reviewer","choices":[{"index":0,"message":{"role":"assistant","content":"{\"probability\":0.6,\"reason\":\"测试复核结果\"}"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":10,"total_tokens":20}}`)
	}))
	defer reviewer.Close()
	e.channel(e.admin, map[string]any{"name": "复核渠道", "type": "openai", "scope": "global", "baseUrl": reviewer.URL + "/v1", "models": models("reviewer")})
	cfg := control.DefaultConfig()
	cfg.Enabled = true
	cfg.BaseURL = jev.URL
	cfg.Categories = []string{"检查"}
	cfg.Rules = []control.Rule{{ID: uuid.NewString(), Name: "审计", Enabled: true, ChannelIDs: []string{ch}, Action: "audit"}}
	secret := "test"
	input := control.SaveInput{Config: cfg, APIKey: &secret}
	saved := e.mustDo(e.admin, http.MethodPut, "/api/admin/control/settings", input, 200)
	first := fmt.Sprintf(`{"model":"m1","messages":[{"role":"user","content":[{"type":"text","text":"%s"},{"type":"image_url","image_url":{"url":"data:image/png;base64,binarySecret"}}]}]}`, strings.Repeat("原文快照", 80))
	for _, body := range []string{first, chatBody} {
		if code, _, raw := readBody(gwPost(t, ctx, e.h.srv.URL, "/v1/chat/completions", key, body)); code != 200 {
			t.Fatalf("gateway=%d %s", code, raw)
		}
	}
	list := func() []any {
		return e.mustDo(e.admin, http.MethodGet, "/api/admin/control/events?userId="+uid, nil, 200)["items"].([]any)
	}
	items := list()
	if len(items) != 2 {
		t.Fatalf("events=%v", items)
	}
	latest, original := items[0].(map[string]any), items[1].(map[string]any)
	eventID, originalID := fmt.Sprint(latest["id"]), fmt.Sprint(original["id"])
	eventURL := func(id string) string { return "/api/admin/control/events/" + id }
	snap := e.mustDo(e.admin, http.MethodGet, eventURL(eventID)+"/snapshot", nil, 200)
	if latest["snapshotId"] == nil || latest["snapshotId"] != original["snapshotId"] || snap["requestId"] != original["requestId"] ||
		strings.Contains(stringify(snap), "binarySecret") || len([]rune(fmt.Sprint(original["excerpt"]))) <= 180 {
		t.Fatalf("snapshot or excerpt mismatch: %v %v", latest, original)
	}
	e.mustDo(e.carol, http.MethodGet, eventURL(eventID)+"/snapshot", nil, 403)
	e.mustDo(e.admin, http.MethodPost, "/api/admin/control/review", map[string]any{"eventId": eventID}, 422)
	if got := e.mustDo(e.admin, http.MethodGet, "/api/admin/control/review-models", nil, 200); !strings.Contains(stringify(got), "reviewer") {
		t.Fatalf("reviewer absent: %v", got)
	}
	review := e.mustDo(e.admin, http.MethodPost, "/api/admin/control/review", map[string]any{"eventId": eventID, "model": "reviewer"}, 200)
	token, _ := review["applyToken"].(string)
	if token == "" || review["effectiveHit"] != false || review["probability"] != 0.6 || review["threshold"] != 0.7 || reviewCalls.Load() != 1 {
		t.Fatalf("review result: %v", review)
	}
	if pending := list()[0].(map[string]any); pending["reviewProbability"] != nil || pending["effectiveHit"] != true {
		t.Fatalf("review changed event before apply: %v", pending)
	}
	input.APIKey = nil
	input.Version = int(saved["version"].(float64))
	input.Threshold = 0.1
	e.mustDo(e.admin, http.MethodPut, "/api/admin/control/settings", input, 200)
	body := map[string]any{"token": token}
	e.mustDo(e.admin, http.MethodPost, eventURL(originalID)+"/apply-review", body, 422)
	e.mustDo(e.admin, http.MethodPost, eventURL(eventID)+"/apply-review", map[string]any{"token": token, "threshold": 1.1}, 422)
	decision := e.mustDo(e.admin, http.MethodPost, eventURL(eventID)+"/apply-review", body, 200)
	if decision["effectiveHit"] != false || decision["reviewThreshold"] != 0.7 {
		t.Fatalf("original event threshold changed: %v", decision)
	}
	decision = e.mustDo(e.admin, http.MethodPost, eventURL(eventID)+"/apply-review", map[string]any{"token": token, "threshold": 0.6}, 200)
	if decision["effectiveHit"] != true || decision["reviewThreshold"] != 0.6 {
		t.Fatalf("review threshold not applied: %v", decision)
	}
	if checked := list()[0].(map[string]any); checked["probability"] != 0.98 || checked["threshold"] != 0.7 ||
		checked["reviewProbability"] != 0.6 || checked["reviewThreshold"] != 0.6 || checked["effectiveHit"] != true || checked["action"] != "audit" {
		t.Fatalf("effective decision or original Jev record changed: %v", checked)
	}
	if err := e.app.FlushLogs(ctx); err != nil {
		t.Fatal(err)
	}
	if logs := e.mustDo(e.admin, http.MethodGet, "/api/logs?model=reviewer", nil, 200); logs["total"] != float64(0) {
		t.Fatalf("review persisted to ordinary gateway logs: %v", logs)
	}
	snapshotID := fmt.Sprint(snap["id"])
	if _, err := e.pool.Exec(ctx, `UPDATE control_snapshots SET created_at=$2 WHERE id=$1`, snapshotID, time.Now().UTC().Add(-10*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if code, _, raw := readBody(gwPost(t, ctx, e.h.srv.URL, "/v1/chat/completions", key, chatBody)); code != 200 {
		t.Fatalf("gateway=%d %s", code, raw)
	}
	if next := list()[0].(map[string]any); next["snapshotId"] == snapshotID {
		t.Fatal("snapshot reused outside the configured time window")
	}
}
