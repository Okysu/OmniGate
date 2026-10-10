package subscription

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"omnigate/internal/money"
)

func TestUpgradePrice(t *testing.T) {
	m := money.MustParse
	month := 30 * 24 * time.Hour
	cases := []struct {
		newP, oldP string
		remaining  time.Duration
		want       string
	}{
		{"59", "25", 20 * 24 * time.Hour, "22.67"}, // 34 × 20/30 = 22.666… → rounded up
		{"59", "25", month, "34"},
		{"25", "59", month, "0"},  // downgrade
		{"25", "25", month, "0"},  // same price
		{"25", "0", 15 * 24 * time.Hour, "12.5"},
		{"59", "25", time.Second, "0.01"}, // tiny remainders still cost the minimum unit
		{"59", "25", 0, "0"},
	}
	for _, c := range cases {
		got, err := UpgradePrice(m(c.newP), month, m(c.oldP), month, c.remaining)
		if err != nil || got != m(c.want) {
			t.Errorf("UpgradePrice(%s, %s, %v) = %s, %v; want %s", c.newP, c.oldP, c.remaining, got, err, c.want)
		}
	}
	// Different periods compare daily prices: ¥10/7d vs ¥30/30d.
	got, _ := UpgradePrice(m("30"), month, m("10"), 7*24*time.Hour, 7*24*time.Hour)
	if got != 0 {
		t.Errorf("weekly ¥10 → monthly ¥30 = %s, want 0 (cheaper per day)", got)
	}
}

func TestUpgradeCheck(t *testing.T) {
	now := time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)
	price := func(s string) *money.Amount { a := money.MustParse(s); return &a }
	goPlus := &Plan{ID: uuid.New(), Name: "Go+", ListPrice: price("25"), Duration: "30d", Status: PlanActive}
	pro := &Plan{ID: uuid.New(), Name: "Pro", ListPrice: price("59"), Duration: "30d", Status: PlanActive}
	sub := &Subscription{ID: uuid.New(), PlanID: goPlus.ID, State: StatusActive, EndsAt: now.Add(10 * 24 * time.Hour)}
	if p, err := UpgradeCheck(sub, goPlus, pro, []*Subscription{sub}, now); err != nil || p.String() != "11.34" {
		t.Fatalf("upgrade = %s, %v", p, err)
	}
	if _, err := UpgradeCheck(sub, pro, goPlus, []*Subscription{sub}, now); err == nil {
		t.Fatal("downgrade must be rejected")
	}
	if _, err := UpgradeCheck(sub, goPlus, goPlus, []*Subscription{sub}, now); err == nil {
		t.Fatal("same plan must be rejected")
	}
	held := &Subscription{ID: uuid.New(), PlanID: pro.ID, State: StatusActive, EndsAt: now.Add(time.Hour)}
	if _, err := UpgradeCheck(sub, goPlus, pro, []*Subscription{sub, held}, now); err == nil {
		t.Fatal("upgrading to a plan already held must be rejected")
	}
	expired := &Subscription{ID: uuid.New(), PlanID: goPlus.ID, State: StatusActive, EndsAt: now.Add(-time.Hour)}
	if _, err := UpgradeCheck(expired, goPlus, pro, nil, now); err == nil {
		t.Fatal("expired source must be rejected")
	}
	// A granted plan without a price pays the new plan's full prorated price.
	free := &Plan{ID: uuid.New(), Name: "赠送", Duration: "30d", Status: PlanArchived}
	sub2 := &Subscription{ID: uuid.New(), PlanID: free.ID, State: StatusActive, EndsAt: now.Add(15 * 24 * time.Hour)}
	if p, err := UpgradeCheck(sub2, free, pro, []*Subscription{sub2}, now); err != nil || p.String() != "29.5" {
		t.Fatalf("upgrade from free = %s, %v", p, err)
	}
}

func TestRenewTarget(t *testing.T) {
	p := &Plan{ID: uuid.New()}
	now := time.Now()
	a := &Subscription{PlanID: p.ID, EndsAt: now.Add(time.Hour)}
	b := &Subscription{PlanID: p.ID, EndsAt: now.Add(2 * time.Hour)}
	if RenewTarget([]*Subscription{a, b, {PlanID: uuid.New()}}, p) != b {
		t.Fatal("renew target must be the latest-ending subscription of the plan")
	}
	p.Stackable = true
	if RenewTarget([]*Subscription{a}, p) != nil {
		t.Fatal("stackable plans are never renewed")
	}
}
