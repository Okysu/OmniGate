package app_test

import (
	"encoding/json"
	"github.com/google/uuid"
	"net/http"
	"testing"
)

func TestBenefitCampaignRoutesAndHiddenPlan(t *testing.T) {
	e := setupGateway(t)
	body := planBody("7天活动套餐", "1", 10)
	body["hidden"] = true
	body["stock"] = 1
	body["stackable"] = true
	body["duration"] = "7d"
	plan := e.mustDo(e.admin, http.MethodPost, "/api/admin/billing/plans", body, 201)
	if plan["stock"] != float64(1) || plan["hidden"] != true {
		t.Fatal(plan)
	}
	catalog := e.mustDo(e.carol, http.MethodGet, "/api/plans", nil, 200)
	if len(catalog["items"].([]any)) != 0 {
		t.Fatalf("hidden catalog %v", catalog)
	}
	campaign := e.mustDo(e.admin, http.MethodPost, "/api/admin/benefits/campaigns", map[string]any{"title": "活动", "rewardType": "subscription", "planId": plan["id"], "price": "0", "status": "active"}, 200)
	if campaign["stock"] != float64(-1) {
		t.Fatal(campaign)
	}
	id := campaign["id"].(string)
	resp, err := http.Get(e.h.srv.URL + "/api/public/benefits/" + id)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("public activity requires session: %d", resp.StatusCode)
	}
	var public map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&public)
	if public["plan"].(map[string]any)["duration"] != "7d" {
		t.Fatal(public)
	}
	e.mustDo(e.carol, http.MethodPost, "/api/benefits/campaigns/"+id+"/claim", map[string]any{"requestId": uuid.NewString()}, 200)
	counts := e.mustDo(e.carol, http.MethodGet, "/api/benefits/claims", nil, 200)
	if counts["counts"].(map[string]any)[id] != float64(1) {
		t.Fatal(counts)
	}
	live := e.mustDo(e.carol, http.MethodGet, "/api/billing/subscriptions", nil, 200)
	if len(live["items"].([]any)) != 1 {
		t.Fatal(live)
	}
	e.mustDo(e.carol, http.MethodPost, "/api/billing/purchase", map[string]any{"planId": plan["id"], "expectedPrice": "1"}, 409)
	e.mustDo(e.admin, http.MethodPost, "/api/benefits/campaigns/"+id+"/claim", map[string]any{"requestId": uuid.NewString()}, 409)
}
