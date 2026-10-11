package subscription

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"omnigate/internal/money"
)

func TestQuoteUpgrade(t *testing.T) {
	m := money.MustParse
	day := 24 * time.Hour
	month := 30 * day
	now := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		name       string
		newP, oldP string
		remaining  time.Duration
		price      string
		credit     string
		periods    int
	}{
		{"right after buying", "59", "25", month, "34", "25", 1},
		{"20 days left", "59", "25", 20 * day, "42.34", "16.66", 1}, // credit 16.666… rounded down
		{"one day left costs about a new purchase", "59", "25", day, "58.17", "0.83", 1},
		{"expired source", "59", "25", 0, "59", "0", 1},
		{"no old price", "59", "0", month, "59", "0", 1},
		{"renewed three times: credit 75 > 59 → two periods", "59", "25", 3 * month, "43", "75", 2},
	}
	for _, c := range cases {
		q, err := QuoteUpgrade(m(c.newP), month, m(c.oldP), month, c.remaining, now)
		if err != nil || q.Price != m(c.price) || q.Credit != m(c.credit) || q.Periods != c.periods ||
			!q.EndsAt.Equal(now.Add(month*time.Duration(c.periods))) {
			t.Errorf("%s: %+v, %v; want price %s credit %s periods %d", c.name, q, err, c.price, c.credit, c.periods)
		}
	}
}

func TestUpgradeCheck(t *testing.T) {
	now := time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)
	price := func(s string) *money.Amount { a := money.MustParse(s); return &a }
	goPlus := &Plan{ID: uuid.New(), Name: "Go+", ListPrice: price("25"), Duration: "30d", Status: PlanActive}
	pro := &Plan{ID: uuid.New(), Name: "Pro", ListPrice: price("59"), Duration: "30d", Status: PlanActive}
	sub := &Subscription{ID: uuid.New(), PlanID: goPlus.ID, State: StatusActive, EndsAt: now.Add(10 * 24 * time.Hour)}
	q, err := UpgradeCheck(sub, goPlus, pro, []*Subscription{sub}, now)
	if err != nil || q.Price.String() != "50.67" || !q.EndsAt.Equal(now.Add(30*24*time.Hour)) { // 59 − 25 × 10/30 (8.33)
		t.Fatalf("upgrade = %+v, %v", q, err)
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
	// A weekly ¥10 plan is dearer per day than a monthly ¥30 one: not an upgrade.
	weekly := &Plan{ID: uuid.New(), Name: "周卡", ListPrice: price("10"), Duration: "7d", Status: PlanActive}
	monthly := &Plan{ID: uuid.New(), Name: "月卡", ListPrice: price("30"), Duration: "30d", Status: PlanActive}
	ws := &Subscription{ID: uuid.New(), PlanID: weekly.ID, State: StatusActive, EndsAt: now.Add(24 * time.Hour)}
	if _, err := UpgradeCheck(ws, weekly, monthly, []*Subscription{ws}, now); err == nil {
		t.Fatal("cheaper per day must not count as an upgrade")
	}
	// A granted plan without a price is credited nothing.
	free := &Plan{ID: uuid.New(), Name: "赠送", Duration: "30d", Status: PlanArchived}
	sub2 := &Subscription{ID: uuid.New(), PlanID: free.ID, State: StatusActive, EndsAt: now.Add(15 * 24 * time.Hour)}
	if q, err := UpgradeCheck(sub2, free, pro, []*Subscription{sub2}, now); err != nil || q.Price.String() != "59" {
		t.Fatalf("upgrade from free = %+v, %v", q, err)
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

func TestSortCatalog(t *testing.T) {
	price := func(s string) *money.Amount { a := money.MustParse(s); return &a }
	plans := []*Plan{{Name: "Plus", ListPrice: price("29")}, {Name: "gift"}, {Name: "Go", ListPrice: price("19")},
		{Name: "Ultra", ListPrice: price("199")}, {Name: "Aigo", ListPrice: price("30")}}
	SortCatalog(plans)
	var names []string
	for _, p := range plans {
		names = append(names, p.Name)
	}
	if got := strings.Join(names, ","); got != "Go,Plus,Aigo,Ultra,gift" {
		t.Fatalf("order = %s", got)
	}
}
