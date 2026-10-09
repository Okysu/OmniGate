package app_test

import (
	"context"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// Round 6 (docs/contracts/phase8-api.md §2, §3): request and spend limits,
// usage counters, /api/billing/limits, time-of-day price schedules.

// limitsEnv is a tiers environment where carol has a platform channel (m1,
// 1000/M: 0.012 per request) and an own channel (m2), billing enforced and a
// balance of 10.
type limitsEnv struct {
	*gwEnv
	bob     *client
	carolID string
	ck      string // carol's unlimited key
	plat    *modelUpstream
	own     *modelUpstream
}

func setupLimits(t *testing.T) *limitsEnv {
	e, bob := setupTiers(t)
	l := &limitsEnv{gwEnv: e, bob: bob, carolID: e.userID(e.carol), plat: newModelUpstream(t, 0), own: newModelUpstream(t, 0)}
	e.channel(e.admin, map[string]any{"name": "plat", "type": "openai", "baseUrl": pub(l.plat.srv.URL) + "/v1", "scope": "global", "models": models("m1")})
	e.channel(e.carol, map[string]any{"name": "carol-own", "type": "openai", "baseUrl": pub(l.own.srv.URL) + "/v1", "models": models("m2")})
	for _, m := range []string{"m1", "m2"} {
		e.mustDo(e.admin, http.MethodPost, "/api/admin/prices", map[string]any{"kind": "sell", "model": m, "inputPerM": "1000", "outputPerM": "1000"}, 201)
	}
	e.enforceBilling()
	e.credit(l.carolID, "10")
	_, l.ck = e.key(e.carol, map[string]any{"name": "ck"})
	return l
}

// call sends one chat request with key and waits for its settlement.
func (l *limitsEnv) call(key, model string) (int, map[string]any, http.Header) {
	l.t.Helper()
	resp := gwPost(l.t, context.Background(), l.h.srv.URL, "/v1/chat/completions", key, chat(model))
	code, out, _ := readBody(resp)
	if err := l.app.FlushLogs(context.Background()); err != nil {
		l.t.Fatal(err)
	}
	return code, out, resp.Header
}

func (l *limitsEnv) expect(key, model string, want int, code string) http.Header {
	l.t.Helper()
	got, out, h := l.call(key, model)
	if got != want || (code != "" && gwErrCode(out) != code) {
		l.t.Fatalf("%s = %d %v, want %d %s", model, got, out, want, code)
	}
	return h
}

// gwErrCode is the OpenAI-style error code of a gateway error.
func gwErrCode(m map[string]any) string {
	e, _ := m["error"].(map[string]any)
	c, _ := e["code"].(string)
	return c
}

func gwErrMessage(m map[string]any) string {
	e, _ := m["error"].(map[string]any)
	s, _ := e["message"].(string)
	return s
}

func (l *limitsEnv) limits(c *client) map[string]any {
	l.t.Helper()
	return l.mustDo(c, http.MethodGet, "/api/billing/limits", nil, 200)
}

// counter reads a usage counter row directly.
func (l *limitsEnv) counter(scope, id, window string) (int64, int64) {
	l.t.Helper()
	var reqs, charge int64
	err := l.app.DB().QueryRow(context.Background(), `SELECT COALESCE(sum(requests), 0), COALESCE(sum(charge_nano), 0) FROM usage_counters
		WHERE scope = $1 AND scope_id = $2 AND window_kind = $3`, scope, id, window).Scan(&reqs, &charge)
	if err != nil {
		l.t.Fatal(err)
	}
	return reqs, charge
}

func retryAfter(t *testing.T, h http.Header) int {
	t.Helper()
	n, err := strconv.Atoi(h.Get("Retry-After"))
	if err != nil {
		t.Fatalf("Retry-After = %q", h.Get("Retry-After"))
	}
	return n
}

func TestRequestLimits(t *testing.T) {
	l := setupLimits(t)

	t.Run("rpm applies to own channels", func(t *testing.T) {
		// Same timezone as the rpd group below: day windows are per timezone.
		g := l.newGroup(map[string]any{"name": "rpm", "timezone": "America/New_York", "limits": map[string]any{"rpm": 2}})
		l.moveUser(l.carolID, g)
		l.expect(l.ck, "m2", 200, "")
		l.expect(l.ck, "m2", 200, "")
		h := l.expect(l.ck, "m2", 429, "rate_limited")
		if s := retryAfter(t, h); s < 1 || s > 30 {
			t.Fatalf("Retry-After = %d", s)
		}
		if hits := l.own.hits.Load(); hits != 2 {
			t.Fatalf("upstream hits = %d", hits)
		}
	})

	t.Run("rpd counts every tier and resets on the next local day", func(t *testing.T) {
		g := l.newGroup(map[string]any{"name": "rpd", "timezone": "America/New_York", "limits": map[string]any{"rpd": 3}})
		l.moveUser(l.carolID, g)
		// The 2 own requests above already count today (the rpm denial does not).
		l.expect(l.ck, "m1", 200, "")
		_, out, h := l.call(l.ck, "m2")
		if gwErrCode(out) != "user_request_limit" || !strings.Contains(gwErrMessage(out), "daily request limit") {
			t.Fatalf("rpd denial = %v", out)
		}
		ny, _ := time.LoadLocation("America/New_York")
		now := time.Now().In(ny)
		midnight := time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, ny)
		if s := retryAfter(t, h); s < int(time.Until(midnight).Seconds())-5 || s > int(time.Until(midnight).Seconds())+5 {
			t.Fatalf("Retry-After = %d, want ≈ %v", s, time.Until(midnight))
		}
		lim := l.limits(l.carol)
		if lim["usage"].(map[string]any)["rpdUsed"] != float64(3) || lim["group"].(map[string]any)["rpd"] != float64(3) {
			t.Fatalf("limits = %v", lim)
		}
		// Rejected requests are not counted.
		if reqs, _ := l.counter("user", l.carolID, "day"); reqs != 3 {
			t.Fatalf("day requests = %d", reqs)
		}
		// The next local day starts a new window.
		if _, err := l.app.DB().Exec(context.Background(), `UPDATE usage_counters SET window_start = $1 WHERE scope = 'user' AND window_kind = 'day'`,
			time.Date(now.Year(), now.Month(), now.Day()-1, 0, 0, 0, 0, ny).UTC()); err != nil {
			t.Fatal(err)
		}
		l.expect(l.ck, "m2", 200, "")
	})

	t.Run("concurrent requests are all counted", func(t *testing.T) {
		g := l.newGroup(map[string]any{"name": "free-for-all"})
		l.moveUser(l.carolID, g)
		before, beforeCharge := l.counter("user", l.carolID, "month")
		var wg sync.WaitGroup
		var mu sync.Mutex
		codes := map[int]int{}
		for i := 0; i < 24; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				model := "m2"
				if i%2 == 0 {
					model = "m1"
				}
				resp := gwPost(t, context.Background(), l.h.srv.URL, "/v1/chat/completions", l.ck, chat(model))
				_, _ = io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
				mu.Lock()
				codes[resp.StatusCode]++
				mu.Unlock()
			}(i)
		}
		wg.Wait()
		if err := l.app.FlushLogs(context.Background()); err != nil {
			t.Fatal(err)
		}
		if codes[200] != 24 {
			t.Fatalf("codes = %v", codes)
		}
		reqs, charge := l.counter("user", l.carolID, "month")
		if reqs-before != 24 || charge-beforeCharge != 12*12_000_000 {
			t.Fatalf("month counter +%d requests, +%d charge", reqs-before, charge-beforeCharge)
		}
		var keyCharge int64
		if err := l.app.DB().QueryRow(context.Background(), `SELECT charge_nano FROM usage_counters WHERE scope = 'key' AND window_kind = 'total'`).
			Scan(&keyCharge); err != nil {
			t.Fatal(err)
		}
		if keyCharge != 13*12_000_000 { // + the m1 request of the rpd subtest
			t.Fatalf("key total charge = %d", keyCharge)
		}
		w := l.mustDo(l.carol, http.MethodGet, "/api/billing/wallet", nil, 200)
		if w["balance"] != "9.844" || w["reserved"] != "0" { // 10 − 13 × 0.012
			t.Fatalf("wallet = %v", w)
		}
	})
}

func TestSpendLimits(t *testing.T) {
	l := setupLimits(t)
	ctx := context.Background()

	t.Run("daily spend: platform blocked, own and shared still served", func(t *testing.T) {
		g := l.newGroup(map[string]any{"name": "daily", "limits": map[string]any{"dailySpend": "0.001"}})
		l.moveUser(l.carolID, g)
		l.expect(l.ck, "m1", 200, "") // 0 < 0.001: admitted, charged 0.012
		_, out, h := l.call(l.ck, "m1")
		if gwErrCode(out) != "spend_limit_exceeded" || !strings.Contains(gwErrMessage(out), "daily spend limit") ||
			!strings.Contains(gwErrMessage(out), "resets at") {
			t.Fatalf("daily denial = %v", out)
		}
		if s := retryAfter(t, h); s < 1 || s > 86400 {
			t.Fatalf("Retry-After = %d", s)
		}
		l.expect(l.ck, "m2", 200, "") // own channel
		if l.plat.hits.Load() != 1 {
			t.Fatalf("platform hits = %d", l.plat.hits.Load())
		}
		// A user-shared channel is not limited either.
		shared := newModelUpstream(t, 0)
		l.shareGroups(l.channel(l.bob, map[string]any{"name": "bob-s", "type": "openai", "baseUrl": pub(shared.srv.URL) + "/v1", "scope": "shared",
			"models": models("m3")}), g)
		l.expect(l.ck, "m3", 200, "")
		lim := l.limits(l.carol)
		if u := lim["usage"].(map[string]any); u["dailySpent"] != "0.012" || u["monthlySpent"] != "0.012" || u["rpdUsed"] != float64(3) {
			t.Fatalf("usage = %v", u)
		}
	})

	t.Run("monthly spend", func(t *testing.T) {
		g := l.newGroup(map[string]any{"name": "monthly", "limits": map[string]any{"monthlySpend": "0.012"}})
		l.moveUser(l.carolID, g)
		_, out, _ := l.call(l.ck, "m1")
		if gwErrCode(out) != "spend_limit_exceeded" || !strings.Contains(gwErrMessage(out), "monthly spend limit") {
			t.Fatalf("monthly denial = %v", out)
		}
	})

	t.Run("reservation is capped by the remaining limit", func(t *testing.T) {
		up := newFakeUpstream(t)
		up.setMode("block")
		l.channel(l.admin, map[string]any{"name": "slow", "type": "openai", "baseUrl": pub(up.srv.URL) + "/v1", "scope": "global", "models": models("m7")})
		l.mustDo(l.admin, http.MethodPost, "/api/admin/prices", map[string]any{"kind": "sell", "model": "m7", "inputPerM": "1000", "outputPerM": "1000"}, 201)
		g := l.newGroup(map[string]any{"name": "cap", "limits": map[string]any{"dailySpend": "0.062"}}) // 0.012 spent today: 0.05 left
		l.moveUser(l.carolID, g)
		done := make(chan int, 1)
		go func() {
			resp := gwPost(t, ctx, l.h.srv.URL, "/v1/chat/completions", l.ck,
				`{"model":"m7","stream":true,"max_tokens":100000,"messages":[{"role":"user","content":"hi"}]}`)
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			done <- resp.StatusCode
		}()
		var reserved any
		for i := 0; i < 200; i++ {
			reserved = l.mustDo(l.carol, http.MethodGet, "/api/billing/wallet", nil, 200)["reserved"]
			if reserved != "0" {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		close(up.release)
		if code := <-done; code != 200 {
			t.Fatalf("stream = %d", code)
		}
		// The estimate (100 000 output tokens ≈ 100) is held up to the 0.05 left.
		if reserved != "0.05" {
			t.Fatalf("reserved = %v", reserved)
		}
		l.app.FlushLogs(ctx)
		if w := l.mustDo(l.carol, http.MethodGet, "/api/billing/wallet", nil, 200); w["reserved"] != "0" {
			t.Fatalf("reservation not released: %v", w)
		}
	})

	t.Run("key spend limit", func(t *testing.T) {
		g := l.newGroup(map[string]any{"name": "nolimits"})
		l.moveUser(l.carolID, g)
		for _, bad := range []map[string]any{{"amount": "1", "window": "year"}, {"amount": "-1", "window": "day"}, {"amount": "x", "window": "day"}} {
			resp, out := l.carol.do(http.MethodPost, "/api/keys", map[string]any{"name": "bad", "policy": map[string]any{"spendLimit": bad}})
			if resp.StatusCode != 422 {
				t.Fatalf("spendLimit %v = %d %v", bad, resp.StatusCode, out)
			}
		}
		id, key := l.key(l.carol, map[string]any{"name": "capped", "policy": map[string]any{"spendLimit": map[string]any{"amount": "0.020", "window": "day"}}})
		l.expect(key, "m1", 200, "")
		l.expect(key, "m1", 200, "") // 0.012 < 0.02
		_, out, h := l.call(key, "m1")
		if gwErrCode(out) != "spend_limit_exceeded" || !strings.Contains(gwErrMessage(out), `API key "capped"`) || h.Get("Retry-After") == "" {
			t.Fatalf("key denial = %v %v", out, h)
		}
		l.expect(key, "m2", 200, "")  // own channel: not limited
		l.expect(l.ck, "m1", 200, "") // other keys: not limited
		_, total := l.key(l.carol, map[string]any{"name": "lifetime", "policy": map[string]any{"spendLimit": map[string]any{"amount": "0.012", "window": "total"}}})
		l.expect(total, "m1", 200, "")
		_, out, h = l.call(total, "m1")
		if gwErrCode(out) != "spend_limit_exceeded" || h.Get("Retry-After") != "" || !strings.Contains(gwErrMessage(out), "never resets") {
			t.Fatalf("total denial = %v %v", out, h)
		}
		lim := l.limits(l.carol)
		var capped map[string]any
		for _, k := range lim["keys"].([]any) {
			if km := k.(map[string]any); km["id"] == id {
				capped = km
			}
		}
		if capped["spent"] != "0.024" || capped["spendLimit"].(map[string]any)["amount"] != "0.02" || capped["resetsAt"] == nil {
			t.Fatalf("key limits = %v", capped)
		}
		for _, k := range lim["keys"].([]any) {
			km := k.(map[string]any)
			switch km["name"] {
			case "ck":
				if km["spendLimit"] != nil || km["spent"] != nil {
					t.Fatalf("unlimited key = %v", km)
				}
			case "lifetime":
				if km["spent"] != "0.012" || km["resetsAt"] != nil {
					t.Fatalf("lifetime key = %v", km)
				}
			}
		}
		if gr := lim["group"].(map[string]any); gr["name"] != "nolimits" || gr["priceMultiplier"] != "1" || gr["dailySpend"] != nil {
			t.Fatalf("group = %v", gr)
		}
		if r := lim["usage"].(map[string]any)["resetsAt"].(map[string]any); r["day"] == nil || r["month"] == nil {
			t.Fatalf("resetsAt = %v", r)
		}
	})
}

func TestSpendLimitNotifications(t *testing.T) {
	l := setupLimits(t)
	g := l.newGroup(map[string]any{"name": "n", "limits": map[string]any{"dailySpend": "0.1"}})
	l.moveUser(l.carolID, g)
	_, key := l.key(l.carol, map[string]any{"name": "k30", "policy": map[string]any{"spendLimit": map[string]any{"amount": "0.03", "window": "week"}}})
	// Key limit: 0.012 (40 %), 0.024 (80 %: near), 0.036 (reached), then blocked.
	l.expect(key, "m1", 200, "")
	if n := l.notifs(l.carol, "limit.spend_near,limit.spend_reached"); len(n) != 0 {
		t.Fatalf("notifications at 40%%: %v", n)
	}
	l.expect(key, "m1", 200, "")
	l.expect(key, "m1", 200, "")
	l.expect(key, "m1", 429, "spend_limit_exceeded")
	near := l.expectNotifs(l.carol, "limit.spend_near", 1)
	if d := near[0]["data"].(map[string]any); d["kind"] != "key" || d["keyName"] != "k30" || d["spent"] != "0.024" || d["window"] != "week" {
		t.Fatalf("key near = %v", near[0])
	}
	reached := l.expectNotifs(l.carol, "limit.spend_reached", 1)
	if !strings.Contains(reached[0]["title"].(string), "k30") {
		t.Fatalf("key reached = %v", reached[0])
	}
	// Group daily limit 0.1: 0.036 spent; 0.048 … 0.072 below 80 %, 0.084 and
	// 0.096 near (one notification per window), 0.108 reached.
	for i := 0; i < 6; i++ {
		l.expect(l.ck, "m1", 200, "")
	}
	l.expect(l.ck, "m1", 429, "spend_limit_exceeded")
	var daily []map[string]any
	for _, n := range l.notifs(l.carol, "limit.spend_near,limit.spend_reached") {
		if n["data"].(map[string]any)["kind"] == "daily" {
			daily = append(daily, n)
		}
	}
	if len(daily) != 2 || daily[0]["type"] != "limit.spend_reached" || daily[1]["type"] != "limit.spend_near" ||
		daily[1]["data"].(map[string]any)["spent"] != "0.084" || daily[0]["data"].(map[string]any)["limit"] != "0.1" {
		t.Fatalf("daily notifications = %v", daily)
	}
	if !strings.Contains(daily[0]["body"].(string), "自有与共享渠道不受影响") {
		t.Fatalf("reached body = %v", daily[0]["body"])
	}
}

func TestPriceSchedules(t *testing.T) {
	l := setupLimits(t)
	platID := ""
	for _, it := range l.mustDo(l.admin, http.MethodGet, "/api/channels?q=plat", nil, 200)["items"].([]any) {
		platID = it.(map[string]any)["id"].(string)
	}

	t.Run("validation", func(t *testing.T) {
		for _, c := range []struct {
			body  map[string]any
			field string
		}{
			{map[string]any{"schedule": []map[string]any{{"days": []int{}, "start": "00:30", "end": "08:30", "multiplier": "0.5"},
				{"days": []int{1}, "start": "08:00", "end": "09:00", "multiplier": "2"}}}, "schedule"},
			{map[string]any{"schedule": []map[string]any{{"days": []int{}, "start": "25:00", "end": "08:30", "multiplier": "0.5"}}}, "schedule"},
			{map[string]any{"schedule": []map[string]any{{"days": []int{}, "start": "00:00", "end": "08:30", "multiplier": "10.5"}}}, "schedule"},
			{map[string]any{"scheduleTimezone": "Nowhere/City"}, "scheduleTimezone"},
		} {
			c.body["kind"], c.body["model"], c.body["inputPerM"], c.body["outputPerM"] = "sell", "s1", "1000", "1000"
			out := l.mustDo(l.admin, http.MethodPost, "/api/admin/prices", c.body, 422)
			if out["error"].(map[string]any)["details"].(map[string]any)[c.field] == nil {
				t.Fatalf("%v: %v", c.body, out)
			}
		}
	})

	// m1: half price all day (every day, so the test never straddles a
	// boundary); cost price of the platform channel doubled all day.
	allDay := []map[string]any{{"days": []int{}, "start": "00:00", "end": "24:00", "multiplier": "0.5"}}
	p := l.mustDo(l.admin, http.MethodPost, "/api/admin/prices", map[string]any{"kind": "sell", "model": "m1", "inputPerM": "1000", "outputPerM": "1000",
		"schedule": allDay, "scheduleTimezone": "Asia/Tokyo"}, 201)
	if s := p["schedule"].([]any); len(s) != 1 || s[0].(map[string]any)["multiplier"] != "0.5" || p["scheduleTimezone"] != "Asia/Tokyo" {
		t.Fatalf("created price = %v", p)
	}
	l.mustDo(l.admin, http.MethodPost, "/api/admin/prices", map[string]any{"kind": "cost", "model": "up-m1", "channelId": platID,
		"inputPerM": "100", "outputPerM": "100", "schedule": []map[string]any{{"days": []int{}, "start": "00:00", "end": "24:00", "multiplier": "2"}}}, 201)
	// m2 (carol's own channel is free anyway) gets a schedule for another weekday only.
	other := (int(time.Now().In(time.FixedZone("CST", 8*3600)).Weekday()) + 3) % 7
	l.mustDo(l.admin, http.MethodPost, "/api/admin/prices", map[string]any{"kind": "sell", "model": "m9", "inputPerM": "1000", "outputPerM": "1000",
		"schedule": []map[string]any{{"days": []int{other}, "start": "00:00", "end": "24:00", "multiplier": "3"}}}, 201)
	m9 := newModelUpstream(t, 0)
	l.channel(l.admin, map[string]any{"name": "m9", "type": "openai", "baseUrl": pub(m9.srv.URL) + "/v1", "scope": "global", "models": models("m9")})

	g := l.newGroup(map[string]any{"name": "eighty", "priceMultiplier": "0.8"})
	l.moveUser(l.carolID, g)
	l.expect(l.ck, "m1", 200, "")
	lg := l.lastLog(l.admin, "model=m1")
	// 0.012 × 0.5 × 0.8; cost 0.0012 × 2 (never × group).
	if lg["charge"] != "0.0048" || lg["priceMultiplier"] != "0.4" || lg["cost"] != "0.0024" {
		t.Fatalf("scheduled log = %v", lg)
	}
	l.expect(l.ck, "m9", 200, "")
	if lg := l.lastLog(l.admin, "model=m9"); lg["charge"] != "0.0096" || lg["priceMultiplier"] != "0.8" {
		t.Fatalf("unmatched schedule log = %v", lg)
	}

	t.Run("plaza and price lists show the schedule", func(t *testing.T) {
		find := func(c *client, path, model string) map[string]any {
			for _, it := range l.mustDo(c, http.MethodGet, path, nil, 200)["items"].([]any) {
				if m := it.(map[string]any); m["model"] == model {
					return m
				}
			}
			t.Fatalf("%s: %s missing", path, model)
			return nil
		}
		pm := find(l.carol, "/api/plaza/models", "m1")["price"].(map[string]any)
		if pm["inputPerM"] != "1000" || pm["currentMultiplier"] != "0.5" || pm["scheduleTimezone"] != "Asia/Tokyo" || len(pm["schedule"].([]any)) != 1 {
			t.Fatalf("plaza models price = %v", pm)
		}
		mine := find(l.carol, "/api/plaza/mine", "m1")
		if p := mine["price"].(map[string]any); p["inputPerM"] != "800" || p["currentMultiplier"] != "0.5" || mine["priceMultiplier"] != "0.8" ||
			mine["basePrice"].(map[string]any)["inputPerM"] != "1000" {
			t.Fatalf("plaza mine = %v", mine)
		}
		if p := find(l.carol, "/api/plaza/models", "m9")["price"].(map[string]any); p["currentMultiplier"] != "1" {
			t.Fatalf("m9 plaza = %v", p)
		}
		lst := l.mustDo(l.admin, http.MethodGet, "/api/admin/prices?model=m1&kind=sell", nil, 200)["items"].([]any)
		if first := lst[0].(map[string]any); first["schedule"] == nil || first["scheduleTimezone"] != "Asia/Tokyo" {
			t.Fatalf("price list = %v", first)
		}
		if last := lst[len(lst)-1].(map[string]any); last["schedule"] != nil || last["scheduleTimezone"] != "Asia/Shanghai" {
			t.Fatalf("unscheduled version = %v", last)
		}
		if m := find(l.carol, "/api/models", "m1")["price"].(map[string]any); m["schedule"] == nil {
			t.Fatalf("/api/models price = %v", m)
		}
	})
}
