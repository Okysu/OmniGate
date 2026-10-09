package app_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"omnigate/internal/money"
	"omnigate/internal/protocol"
)

// Round 10 (docs/contracts/phase10-api.md §1): context-length tiered prices.

// usageUpstream answers chat completions with the usage written in the last
// message: "p=<prompt tokens> c=<cached tokens> o=<completion tokens>"; a
// message containing "block" waits for release first.
func usageUpstream(t *testing.T) (*httptest.Server, chan struct{}) {
	release := make(chan struct{}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Model    string `json:"model"`
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		var p, c, o int64
		content := ""
		if n := len(req.Messages); n > 0 {
			content = req.Messages[n-1].Content
		}
		_, _ = fmt.Sscanf(content, "p=%d c=%d o=%d", &p, &c, &o)
		if strings.Contains(content, "block") {
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"id":"c1","object":"chat.completion","created":1,"model":%q,"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],`+
			`"usage":{"prompt_tokens":%d,"completion_tokens":%d,"total_tokens":%d,"prompt_tokens_details":{"cached_tokens":%d}}}`, req.Model, p, o, p+o, c)
	}))
	t.Cleanup(srv.Close)
	return srv, release
}

func TestPriceTiers(t *testing.T) {
	e := setupGateway(t)
	base := e.h.srv.URL
	up, release := usageUpstream(t)
	chID := e.platformChannel(map[string]any{"name": "plat", "type": "openai", "scope": "global", "baseUrl": up.URL + "/v1", "models": models("m1")})
	adminID := e.userID(e.admin)

	t.Run("validation", func(t *testing.T) {
		tier := func(above int, in, out string) map[string]any {
			return map[string]any{"aboveInputTokens": above, "inputPerM": in, "outputPerM": out}
		}
		six := []map[string]any{}
		for i := 1; i <= 6; i++ {
			six = append(six, tier(i*1000, "1", "1"))
		}
		for _, c := range []struct {
			tiers any
			field string
		}{
			{six, "tiers"},
			{[]map[string]any{tier(0, "1", "1")}, "tiers[0].aboveInputTokens"},
			{[]map[string]any{tier(2000, "1", "1"), tier(1000, "1", "1")}, "tiers[1].aboveInputTokens"},
			{[]map[string]any{tier(2000, "1", "1"), tier(2000, "1", "1")}, "tiers[1].aboveInputTokens"},
			{[]map[string]any{tier(1000, "", "1")}, "tiers[0].inputPerM"},
			{[]map[string]any{{"aboveInputTokens": 1000, "inputPerM": "1", "outputPerM": "1", "cacheReadPerM": "-2"}}, "tiers[0].cacheReadPerM"},
		} {
			out := e.mustDo(e.admin, http.MethodPost, "/api/admin/prices", map[string]any{"kind": "sell", "model": "m1", "inputPerM": "1",
				"outputPerM": "1", "tiers": c.tiers}, 422)
			if out["error"].(map[string]any)["details"].(map[string]any)[c.field] == nil {
				t.Fatalf("%v: %v", c.tiers, out)
			}
		}
		// An unknown tier field is rejected like any unknown field.
		e.mustDo(e.admin, http.MethodPost, "/api/admin/prices", map[string]any{"kind": "sell", "model": "m1", "inputPerM": "1", "outputPerM": "1",
			"tiers": []map[string]any{{"aboveInputTokens": 1000, "inputPerM": "1", "outputPerM": "1", "perRequest": "1"}}}, 422)
	})

	// Base 10 / 50 (cache 1 / 12.5); above 1000 prompt tokens 20 / 75, cache
	// read 2, cache write inherited (12.5).
	tiers := []map[string]any{{"aboveInputTokens": 1000, "inputPerM": "20", "outputPerM": "75", "cacheReadPerM": "2"}}
	sell := map[string]any{"kind": "sell", "model": "m1", "inputPerM": "10", "outputPerM": "50", "cacheReadPerM": "1", "cacheWritePerM": "12.5", "tiers": tiers}
	p := e.mustDo(e.admin, http.MethodPost, "/api/admin/prices", sell, 201)
	if tr := p["tiers"].([]any)[0].(map[string]any); tr["aboveInputTokens"] != float64(1000) || tr["inputPerM"] != "20" ||
		tr["cacheReadPerM"] != "2" || tr["cacheWritePerM"] != nil || tr["imageInputPerM"] != nil {
		t.Fatalf("created tiers = %v", p["tiers"])
	}
	e.mustDo(e.admin, http.MethodPost, "/api/admin/prices", map[string]any{"kind": "cost", "model": "up-m1", "channelId": chID,
		"inputPerM": "1", "outputPerM": "1", "tiers": []map[string]any{{"aboveInputTokens": 1000, "inputPerM": "2", "outputPerM": "2"}}}, 201)
	e.enforceBilling()
	e.credit(adminID, "100")
	_, key := e.key(e.admin, map[string]any{"name": "k"})
	call := func(content string) {
		t.Helper()
		body, _ := json.Marshal(map[string]any{"model": "m1", "messages": []map[string]any{{"role": "user", "content": content}}})
		if code, _, raw := readBody(gwPost(t, context.Background(), base, "/v1/chat/completions", key, string(body))); code != 200 {
			t.Fatalf("%s = %d %s", content, code, raw)
		}
	}
	expectLog := func(charge, cost string, tier any) map[string]any {
		t.Helper()
		l := e.lastLog(e.admin, "model=m1")
		if l["charge"] != charge || (cost != "" && l["cost"] != cost) || l["priceTier"] != tier {
			t.Fatalf("log = charge %v cost %v priceTier %v (%v), want %s %s %v", l["charge"], l["cost"], l["priceTier"], l["usage"], charge, cost, tier)
		}
		return l
	}

	// = threshold: base prices. 1000 × 10/M + 10 × 50/M; cost 1010 × 1/M.
	call("p=1000 c=0 o=10")
	expectLog("0.0105", "0.00101", nil)
	// > threshold: the whole request at the tier. 1001 × 20/M + 10 × 75/M;
	// cost 1011 × 2/M (cost prices are tiered by the same prompt tokens).
	call("p=1001 c=0 o=10")
	expectLog("0.02077", "0.002022", float64(1000))
	// Cached tokens count toward the threshold (401 uncached + 600 cached):
	// 401 × 20/M + 600 × 2/M + 10 × 75/M.
	call("p=1001 c=600 o=10")
	expectLog("0.00997", "", float64(1000))

	t.Run("reservation estimate uses the tier", func(t *testing.T) {
		content := "p=10 c=0 o=1 block " + strings.Repeat("x", 6000)
		body, _ := json.Marshal(map[string]any{"model": "m1", "messages": []map[string]any{{"role": "user", "content": content}}})
		est := protocol.EstimateTokens(len(body))
		if est <= 1000 {
			t.Fatalf("estimate %d does not exceed the threshold", est)
		}
		want, _ := money.TokenCost(money.MustParse("20"), est)
		done := make(chan struct{})
		go func() {
			defer close(done)
			readBody(gwPost(t, context.Background(), base, "/v1/chat/completions", key, string(body)))
		}()
		var reserved any
		for range 500 {
			if reserved = e.mustDo(e.admin, http.MethodGet, "/api/billing/wallet", nil, 200)["reserved"]; reserved != "0" {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		release <- struct{}{}
		<-done
		if reserved != want.String() {
			t.Fatalf("reserved = %v, want %s (%d estimated tokens at the tier)", reserved, want, est)
		}
		// Settled at the actual (base-tier) usage: 10 × 10/M + 1 × 50/M.
		expectLog("0.00015", "", nil)
	})

	// Schedule × group multipliers apply on top of the tier, rounded once.
	half := e.newGroup(map[string]any{"name": "Half", "priceMultiplier": "0.5"})
	e.moveUser(adminID, half)
	sell["schedule"] = []map[string]any{{"days": []int{}, "start": "00:00", "end": "24:00", "multiplier": "0.8"}}
	e.mustDo(e.admin, http.MethodPost, "/api/admin/prices", sell, 201)
	call("p=1001 c=0 o=10")
	if l := expectLog("0.008308", "", float64(1000)); l["priceMultiplier"] != "0.4" {
		t.Fatalf("multiplied log = %v", l)
	}

	t.Run("plan charge meter", func(t *testing.T) {
		plan := e.mustDo(e.admin, http.MethodPost, "/api/admin/billing/plans", map[string]any{
			"name": "C", "description": "", "duration": "30d", "models": []string{"m1"}, "stackable": false,
			"rules": []map[string]any{{"id": "c", "label": "c", "meter": "charge", "window": map[string]any{"kind": "lifetime"}, "limit": "1"}},
		}, 201)
		sub := e.mustDo(e.admin, http.MethodPost, "/api/admin/billing/subscriptions", map[string]any{"userId": adminID, "planId": plan["id"], "periods": 1}, 201)
		call("p=1001 c=0 o=10")
		if l := expectLog("0", "", float64(1000)); l["quotaCharge"] != "0.008308" {
			t.Fatalf("plan log = %v", l)
		}
		e.expectUsed(e.admin, sub["id"].(string), map[string]string{"c": "0.008308"})
		e.mustDo(e.admin, http.MethodPost, "/api/admin/billing/subscriptions/"+sub["id"].(string)+"/cancel", map[string]any{}, 200)
	})

	t.Run("price lists and plaza", func(t *testing.T) {
		find := func(path string) map[string]any {
			for _, it := range e.mustDo(e.admin, http.MethodGet, path, nil, 200)["items"].([]any) {
				if m := it.(map[string]any); m["model"] == "m1" {
					return m
				}
			}
			t.Fatalf("%s: m1 missing", path)
			return nil
		}
		// Plaza tiers are resolved: the inherited cache write price is shown.
		tier0 := func(p any) map[string]any { return p.(map[string]any)["tiers"].([]any)[0].(map[string]any) }
		if tr := tier0(find("/api/plaza/models")["price"]); tr["aboveInputTokens"] != float64(1000) || tr["inputPerM"] != "20" || tr["outputPerM"] != "75" ||
			tr["cacheReadPerM"] != "2" || tr["cacheWritePerM"] != "12.5" || tr["imageInputPerM"] != nil || tr["audioOutputPerM"] != nil {
			t.Fatalf("plaza tier = %v", tr)
		}
		mine := find("/api/plaza/mine")
		if tr, bt := tier0(mine["price"]), tier0(mine["basePrice"]); tr["inputPerM"] != "10" || tr["cacheWritePerM"] != "6.25" || bt["inputPerM"] != "20" {
			t.Fatalf("plaza mine tiers = %v / %v", tr, bt)
		}
		if tr := tier0(find("/api/models")["price"]); tr["cacheWritePerM"] != nil || tr["cacheReadPerM"] != "2" {
			t.Fatalf("/api/models tier = %v", tr)
		}
		lst := e.mustDo(e.admin, http.MethodGet, "/api/admin/prices?model=m1&kind=sell", nil, 200)["items"].([]any)
		if len(lst) != 2 || tier0(lst[0])["inputPerM"] != "20" {
			t.Fatalf("price list = %v", lst)
		}
		// A version without tiers renders tiers: null.
		e.mustDo(e.admin, http.MethodPost, "/api/admin/prices", map[string]any{"kind": "sell", "model": "m2", "inputPerM": "1", "outputPerM": "1"}, 201)
		if l := e.mustDo(e.admin, http.MethodGet, "/api/admin/prices?model=m2", nil, 200)["items"].([]any); l[0].(map[string]any)["tiers"] != nil {
			t.Fatalf("untiered = %v", l[0])
		}
	})
}
