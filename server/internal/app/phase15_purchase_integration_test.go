package app_test

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Round 15 (docs/contracts/phase15-api.md): buying, renewing and upgrading a
// plan with the wallet, and referral rebates.

func planBody(name, price string, limit int) map[string]any {
	return map[string]any{
		"name": name, "description": "", "duration": "30d", "models": []string{"c1"}, "listPrice": price,
		"rules": []map[string]any{
			{"id": "5h", "label": "5 小时", "meter": "requests", "window": map[string]any{"kind": "session", "duration": "5h"}, "limit": strconv.Itoa(limit)},
			{"id": "monthly", "label": "月度", "meter": "requests", "window": map[string]any{"kind": "period", "every": "30d"}, "limit": strconv.Itoa(4 * limit)},
		},
	}
}

func optionFor(opts map[string]any, planID string) map[string]any {
	for _, p := range opts["plans"].([]any) {
		o := p.(map[string]any)
		if o["plan"].(map[string]any)["id"] == planID {
			return o
		}
	}
	return nil
}

func TestWalletPurchase(t *testing.T) {
	e := setupGateway(t)
	ctx := context.Background()
	base := e.h.srv.URL
	up := newFakeUpstream(t)
	e.platformChannel(map[string]any{"name": "plat", "type": "openai", "scope": "global", "baseUrl": up.srv.URL + "/v1", "models": models("c1")})
	goPlus := e.mustDo(e.admin, http.MethodPost, "/api/admin/billing/plans", planBody("Go+", "25", 10), 201)["id"].(string)
	pro := e.mustDo(e.admin, http.MethodPost, "/api/admin/billing/plans", planBody("Pro", "59", 20), 201)["id"].(string)
	noPrice := planBody("赠送", "", 5)
	noPrice["listPrice"] = nil
	gift := e.mustDo(e.admin, http.MethodPost, "/api/admin/billing/plans", noPrice, 201)["id"].(string)
	carolID := e.userID(e.carol)
	e.credit(carolID, "100")

	opts := e.mustDo(e.carol, http.MethodGet, "/api/billing/purchase/options", nil, 200)
	if opts["available"] != "100" || len(opts["plans"].([]any)) != 3 {
		t.Fatalf("options = %v", opts)
	}
	if o := optionFor(opts, goPlus); o["action"] != "new" || o["price"] != "25" || o["purchasable"] != true || len(o["upgrades"].([]any)) != 0 {
		t.Fatalf("Go+ option = %v", o)
	}
	if o := optionFor(opts, gift); o["purchasable"] != false || o["price"] != nil {
		t.Fatalf("gift option = %v", o)
	}
	resp, out := e.carol.do(http.MethodPost, "/api/billing/purchase", map[string]any{"planId": gift, "expectedPrice": "0"})
	if resp.StatusCode != 409 || errCode(out) != "plan_not_for_sale" {
		t.Fatalf("buy gift = %d %v", resp.StatusCode, out)
	}
	resp, out = e.carol.do(http.MethodPost, "/api/billing/purchase", map[string]any{"planId": goPlus, "expectedPrice": "20"})
	if resp.StatusCode != 409 || errCode(out) != "price_changed" {
		t.Fatalf("stale price = %d %v", resp.StatusCode, out)
	}

	// Buy Go+.
	r := e.mustDo(e.carol, http.MethodPost, "/api/billing/purchase", map[string]any{"planId": goPlus, "expectedPrice": "25"}, 200)
	sub := r["subscription"].(map[string]any)
	subID := sub["id"].(string)
	if r["action"] != "new" || r["price"] != "25" || r["wallet"].(map[string]any)["available"] != "75" || sub["source"] != "purchase" {
		t.Fatalf("purchase = %v", r)
	}
	endsAt := sub["endsAt"].(string)

	// Use it once, then upgrade: same subscription, end unchanged, usage kept.
	_, key := e.key(e.carol, map[string]any{"name": "c"})
	if code, _, body := readBody(gwPost(t, ctx, base, "/v1/chat/completions", key, `{"model":"c1","messages":[{"role":"user","content":"hi"}]}`)); code != 200 {
		t.Fatalf("gateway = %d %s", code, body)
	}
	e.settle()
	opts = e.mustDo(e.carol, http.MethodGet, "/api/billing/purchase/options", nil, 200)
	if subs := opts["subscriptions"].([]any); len(subs) != 1 || subs[0].(map[string]any)["id"] != subID || subs[0].(map[string]any)["planName"] != "Go+" {
		t.Fatalf("active subscriptions = %v", opts["subscriptions"])
	}
	if o := optionFor(opts, goPlus); o["action"] != "renew" || o["renewSubscriptionId"] != subID {
		t.Fatalf("Go+ renew option = %v", o)
	}
	ups := optionFor(opts, pro)["upgrades"].([]any)
	if len(ups) != 1 {
		t.Fatalf("Pro upgrades = %v", ups)
	}
	u := ups[0].(map[string]any)
	price, _ := strconv.ParseFloat(u["price"].(string), 64)
	if u["fromSubscriptionId"] != subID || u["fromPlanName"] != "Go+" || u["fromPrice"] != "25" || price < 33.98 || price > 34.02 || u["credit"] == "0" {
		t.Fatalf("upgrade option = %v", u)
	}
	resp, out = e.carol.do(http.MethodPost, "/api/billing/purchase", map[string]any{"planId": goPlus, "fromSubscriptionId": subID, "expectedPrice": "100"})
	if resp.StatusCode != 409 || errCode(out) != "not_an_upgrade" {
		t.Fatalf("upgrade to same plan = %d %v", resp.StatusCode, out)
	}
	r = e.mustDo(e.carol, http.MethodPost, "/api/billing/purchase", map[string]any{"planId": pro, "fromSubscriptionId": subID, "expectedPrice": u["price"]}, 200)
	sub = r["subscription"].(map[string]any)
	if r["action"] != "upgrade" || sub["id"] != subID || sub["plan"].(map[string]any)["name"] != "Pro" || sub["endsAt"] == nil {
		t.Fatalf("upgrade = %v", r)
	}
	// The upgrade starts a new 30-day term of Pro from now.
	bought, _ := time.Parse(time.RFC3339Nano, endsAt)
	upgradedEnds, _ := time.Parse(time.RFC3339Nano, sub["endsAt"].(string))
	if upgradedEnds.Before(bought) || time.Until(upgradedEnds) < 29*24*time.Hour {
		t.Fatalf("upgraded endsAt = %v (bought until %v)", upgradedEnds, bought)
	}
	endsAt = sub["endsAt"].(string)
	for _, rule := range sub["rules"].([]any) {
		ru := rule.(map[string]any)
		if ru["used"] != "1" || (ru["id"] == "5h" && ru["limit"] != "20") {
			t.Fatalf("usage after upgrade = %v", ru)
		}
	}
	subs := e.mustDo(e.carol, http.MethodGet, "/api/billing/subscriptions", nil, 200)["items"].([]any)
	if len(subs) != 1 {
		t.Fatalf("upgrade must not create a subscription: %v", subs)
	}

	// Pro is now held: buying it renews; downgrading is refused.
	e.credit(carolID, "100")
	r = e.mustDo(e.carol, http.MethodPost, "/api/billing/purchase", map[string]any{"planId": pro, "expectedPrice": "59"}, 200)
	wantEnds, _ := time.Parse(time.RFC3339Nano, endsAt)
	gotEnds, _ := time.Parse(time.RFC3339Nano, r["subscription"].(map[string]any)["endsAt"].(string))
	if r["action"] != "renew" || !gotEnds.Equal(wantEnds.Add(30*24*time.Hour)) {
		t.Fatalf("renew = %v", r)
	}
	resp, out = e.carol.do(http.MethodPost, "/api/billing/purchase", map[string]any{"planId": goPlus, "fromSubscriptionId": subID, "expectedPrice": "100"})
	if resp.StatusCode != 409 || errCode(out) != "not_an_upgrade" {
		t.Fatalf("downgrade = %d %v", resp.StatusCode, out)
	}
	// Plan groups (phase17-api.md): upgrades only within a group.
	other := planBody("国模", "500", 100)
	other["group"] = "国模"
	otherPlan := e.mustDo(e.admin, http.MethodPost, "/api/admin/billing/plans", other, 201)
	if otherPlan["group"] != "国模" {
		t.Fatalf("group not saved: %v", otherPlan)
	}
	opts = e.mustDo(e.carol, http.MethodGet, "/api/billing/purchase/options", nil, 200)
	if o := optionFor(opts, otherPlan["id"].(string)); o == nil || len(o["upgrades"].([]any)) != 0 || o["plan"].(map[string]any)["group"] != "国模" {
		t.Fatalf("cross-group upgrade offered: %v", o)
	}
	resp, out = e.carol.do(http.MethodPost, "/api/billing/purchase", map[string]any{"planId": otherPlan["id"], "fromSubscriptionId": subID, "expectedPrice": "1000"})
	if resp.StatusCode != 409 || errCode(out) != "not_an_upgrade" {
		t.Fatalf("cross-group upgrade = %d %v", resp.StatusCode, out)
	}
	big := e.mustDo(e.admin, http.MethodPost, "/api/admin/billing/plans", planBody("Max", "1000", 50), 201)["id"].(string)
	resp, out = e.carol.do(http.MethodPost, "/api/billing/purchase", map[string]any{"planId": big, "expectedPrice": "1000"})
	if resp.StatusCode != 403 || errCode(out) != "insufficient_balance" {
		t.Fatalf("insufficient = %d %v", resp.StatusCode, out)
	}
	// Someone else's subscription is not found.
	resp, out = e.admin.do(http.MethodPost, "/api/billing/purchase", map[string]any{"planId": big, "fromSubscriptionId": subID, "expectedPrice": "1000"})
	if resp.StatusCode != 404 {
		t.Fatalf("foreign subscription = %d %v", resp.StatusCode, out)
	}

	list := e.mustDo(e.carol, http.MethodGet, "/api/billing/purchases", nil, 200)
	items := list["items"].([]any)
	if list["total"] != float64(3) || items[0].(map[string]any)["action"] != "renew" ||
		items[1].(map[string]any)["action"] != "upgrade" || items[1].(map[string]any)["fromPlanName"] != "Go+" {
		t.Fatalf("purchases = %v", list)
	}
	ledger := e.mustDo(e.carol, http.MethodGet, "/api/billing/ledger", nil, 200)["items"].([]any)
	purchases := 0
	for _, it := range ledger {
		l := it.(map[string]any)
		if l["refType"] == "purchase" {
			purchases++
			if !strings.HasPrefix(l["amount"].(string), "-") || l["kind"] != "charge" {
				t.Fatalf("purchase entry = %v", l)
			}
		}
	}
	if purchases != 3 {
		t.Fatalf("purchase entries = %d", purchases)
	}
	w := e.mustDo(e.carol, http.MethodGet, "/api/billing/wallet", nil, 200)
	if b, _ := strconv.ParseFloat(w["balance"].(string), 64); b < 81.97 || b > 82.03 { // 200 − 25 − ~34 − 59
		t.Fatalf("wallet = %v", w)
	}
	if n, _ := e.auditCount("subscription.purchase"); n != 3 {
		t.Fatalf("audit = %d", n)
	}

	// Archived plans are not for sale.
	e.mustDo(e.admin, http.MethodPatch, "/api/admin/billing/plans/"+goPlus, map[string]any{"status": "archived", "version": 1}, 200)
	resp, out = e.carol.do(http.MethodPost, "/api/billing/purchase", map[string]any{"planId": goPlus, "expectedPrice": "25"})
	if resp.StatusCode != 409 || errCode(out) != "plan_archived" {
		t.Fatalf("archived = %d %v", resp.StatusCode, out)
	}
}

// loginInvited logs a new client in as user with an invite code.
func (e *gwEnv) loginInvited(user, invite string) *client {
	e.t.Helper()
	c := e.h.newClient()
	resp, _ := c.do(http.MethodGet, "/api/auth/github/login?redirect=/&invite="+url.QueryEscape(invite), nil)
	if resp.StatusCode != http.StatusFound {
		e.t.Fatalf("login status %d", resp.StatusCode)
	}
	authURL, _ := url.Parse(resp.Header.Get("Location"))
	resp, _ = c.do(http.MethodGet, "/api/auth/github/callback?code="+user+"&state="+url.QueryEscape(authURL.Query().Get("state")), nil)
	if resp.StatusCode != http.StatusFound {
		e.t.Fatalf("callback status %d", resp.StatusCode)
	}
	return c
}

func TestReferralRebates(t *testing.T) {
	e, _ := eventsEnv(t)
	info := e.mustDo(e.carol, http.MethodGet, "/api/billing/referral", nil, 200)
	code := info["code"].(string)
	if info["enabled"] != false || info["rate"] != "10" || len(code) != 8 ||
		info["link"] != "http://localhost:8080/login?invite="+code || info["invitedCount"] != float64(0) {
		t.Fatalf("referral = %v", info)
	}
	if again := e.mustDo(e.carol, http.MethodGet, "/api/billing/referral", nil, 200); again["code"] != code {
		t.Fatal("invite code must be stable")
	}
	bob := e.loginInvited("dave", strings.ToLower(code))
	bobID := e.userID(bob)
	// An existing account logging in with someone's code is not rebound.
	e.loginInvited("alice", code)

	batch := e.mustDo(e.admin, http.MethodPost, "/api/admin/billing/redeem-batches", map[string]any{"kind": "wallet_credit", "amount": "50", "count": 3, "perUserLimit": 3}, 201)
	codes := batch["codes"].([]any)
	redeem := func(i int) {
		e.mustDo(bob, http.MethodPost, "/api/billing/redeem", map[string]any{"code": codes[i]}, 200)
		e.settle()
	}
	// Disabled: the invitee is bound but no rebate is paid.
	redeem(0)
	info = e.mustDo(e.carol, http.MethodGet, "/api/billing/referral", nil, 200)
	if info["invitedCount"] != float64(1) || info["rebateTotal"] != "0" {
		t.Fatalf("referral while disabled = %v", info)
	}
	inv := info["invitees"].([]any)[0].(map[string]any)
	if strings.Count(inv["displayName"].(string), "*") != 2 {
		t.Fatalf("invitee = %v", inv)
	}

	st := e.mustDo(e.admin, http.MethodGet, "/api/admin/settings", nil, 200)
	e.mustDo(e.admin, http.MethodPatch, "/api/admin/settings", map[string]any{"version": st["version"], "settings": map[string]any{
		"billing": map[string]any{"referralEnabled": true, "referralRate": "12.5", "referralMinRecharge": "60"}}}, 200)
	if si := e.mustDo(e.carol, http.MethodGet, "/api/system/info", nil, 200); si["referralEnabled"] != true {
		t.Fatalf("system info = %v", si)
	}
	// Below the minimum recharge: still nothing.
	redeem(1)
	if info = e.mustDo(e.carol, http.MethodGet, "/api/billing/referral", nil, 200); info["rebateTotal"] != "0" {
		t.Fatalf("below minimum = %v", info)
	}
	st = e.mustDo(e.admin, http.MethodGet, "/api/admin/settings", nil, 200)
	e.mustDo(e.admin, http.MethodPatch, "/api/admin/settings", map[string]any{"version": st["version"], "settings": map[string]any{
		"billing": map[string]any{"referralMinRecharge": "50"}}}, 200)
	redeem(2)
	info = e.mustDo(e.carol, http.MethodGet, "/api/billing/referral", nil, 200)
	if info["rebateTotal"] != "6.25" || info["invitees"].([]any)[0].(map[string]any)["rebateTotal"] != "6.25" {
		t.Fatalf("rebate = %v", info)
	}
	rebates := e.mustDo(e.carol, http.MethodGet, "/api/billing/referral/rebates", nil, 200)
	rb := rebates["items"].([]any)[0].(map[string]any)
	if rebates["total"] != float64(1) || rb["recharge"] != "50" || rb["rate"] != "12.5" || rb["rebate"] != "6.25" || rb["inviteeName"] != inv["displayName"] {
		t.Fatalf("rebates = %v", rebates)
	}
	l := e.mustDo(e.carol, http.MethodGet, "/api/billing/ledger", nil, 200)["items"].([]any)[0].(map[string]any)
	if l["refType"] != "referral" || l["kind"] != "grant" || l["amount"] != "6.25" {
		t.Fatalf("ledger = %v", l)
	}
	n := e.expectNotifs(e.carol, "wallet.credited", 1)[0]
	if !strings.Contains(stringify(n), "邀请返利") {
		t.Fatalf("notification = %v", n)
	}
	// Bob's own balance is unaffected by the rebate.
	if w := e.mustDo(bob, http.MethodGet, "/api/billing/wallet", nil, 200); w["balance"] != "150" {
		t.Fatalf("bob wallet = %v", w)
	}
	// Self-invites are ignored; a user has no referral of their own.
	var n2 int
	if err := e.pool.QueryRow(context.Background(), `SELECT count(*) FROM referrals WHERE invitee_id = $1`, bobID).Scan(&n2); err != nil || n2 != 1 {
		t.Fatalf("bob referrals = %d %v", n2, err)
	}
}
