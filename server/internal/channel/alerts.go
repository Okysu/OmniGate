package channel

import (
	"context"
	"encoding/json"
	"sort"
	"time"

	"github.com/google/uuid"

	"omnigate/internal/authz"
	"omnigate/internal/money"
	"omnigate/internal/plugin"
)

// BalanceReading is a successful result of a plugin capability whose output
// kind is "balance" (docs/contracts/phase6-api.md §5).
type BalanceReading struct {
	ChannelID uuid.UUID
	Currency  string
	Total     string
	Available bool
	FetchedAt time.Time
}

// balanceOutput is the BalanceOutput of the plugin SDK.
type balanceOutput struct {
	Currency  string `json:"currency"`
	Total     string `json:"total"`
	Available bool   `json:"available"`
}

// parseBalance extracts a reading from a stored capability result.
func parseBalance(channelID uuid.UUID, res *plugin.CapabilityResult) (BalanceReading, bool) {
	if res == nil || !res.OK || res.Unsupported || len(res.Output) == 0 {
		return BalanceReading{}, false
	}
	var out balanceOutput
	if json.Unmarshal(res.Output, &out) != nil || out.Total == "" {
		return BalanceReading{}, false
	}
	if _, err := money.Parse(out.Total); err != nil {
		return BalanceReading{}, false
	}
	return BalanceReading{ChannelID: channelID, Currency: out.Currency, Total: out.Total, Available: out.Available,
		FetchedAt: res.FetchedAt.UTC()}, true
}

// afterResult reports balance readings of freshly saved capability results.
func (s *Service) afterResult(ctx context.Context, channelID uuid.UUID, l *plugin.Loaded, name string, res *plugin.CapabilityResult) {
	if s.OnBalance == nil || l == nil || l.Manifest == nil {
		return
	}
	if decl, ok := l.Manifest.Capabilities[name]; !ok || decl.Output != "balance" {
		return
	}
	if r, ok := parseBalance(channelID, res); ok {
		s.OnBalance(ctx, r)
	}
}

// AlertCounts are the health counts of the managed, enabled channels.
type AlertCounts struct {
	Total    int `json:"total"`
	Healthy  int `json:"healthy"`
	Degraded int `json:"degraded"`
	Down     int `json:"down"`
}

// UpstreamBalance is the latest balance reading of one managed channel.
type UpstreamBalance struct {
	ChannelID   uuid.UUID `json:"channelId"`
	ChannelName string    `json:"channelName"`
	Currency    string    `json:"currency"`
	Total       string    `json:"total"`
	Available   bool      `json:"available"`
	Threshold   *string   `json:"threshold"`
	Low         bool      `json:"low"`
	CheckedAt   time.Time `json:"checkedAt"`
}

// BalanceLow reports whether total is below threshold (both decimal text).
func BalanceLow(total string, threshold *string) bool {
	if threshold == nil {
		return false
	}
	t, err1 := money.Parse(total)
	th, err2 := money.Parse(*threshold)
	return err1 == nil && err2 == nil && t < th
}

// Alerts summarizes the health and upstream balances of the channels p can
// manage (owned channels; every channel with channels.manage).
func (s *Service) Alerts(ctx context.Context, p *authz.Principal) (AlertCounts, []UpstreamBalance, error) {
	q := `SELECT id, name, status, plugin_version_id, alert_balance_below FROM channels`
	var args []any
	if !p.Can(authz.ChannelsManage) {
		q += ` WHERE owner_id = $1`
		args = append(args, p.UserID)
	}
	rows, err := s.store.pool.Query(ctx, q+` ORDER BY created_at`, args...)
	if err != nil {
		return AlertCounts{}, nil, err
	}
	type row struct {
		id        uuid.UUID
		name      string
		status    string
		pv        *uuid.UUID
		threshold *string
	}
	var list []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.name, &r.status, &r.pv, &r.threshold); err != nil {
			rows.Close()
			return AlertCounts{}, nil, err
		}
		list = append(list, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return AlertCounts{}, nil, err
	}
	var counts AlertCounts
	balances := []UpstreamBalance{}
	for _, r := range list {
		if r.status == "enabled" {
			counts.Total++
			switch s.reg.Breaker.Health(r.id).State {
			case "healthy":
				counts.Healthy++
			case "degraded":
				counts.Degraded++
			default:
				counts.Down++
			}
		}
		if r.pv == nil || s.plugins == nil {
			continue
		}
		l, err := s.plugins.Loaded(ctx, *r.pv)
		if err != nil || l.Builtin || l.Manifest == nil {
			continue
		}
		var names []string
		for name, decl := range l.Manifest.Capabilities {
			if decl.Output == "balance" {
				names = append(names, name)
			}
		}
		if len(names) == 0 {
			continue
		}
		sort.Strings(names)
		results, err := s.plugins.Results(ctx, r.id)
		if err != nil {
			return AlertCounts{}, nil, err
		}
		for _, name := range names {
			b, ok := parseBalance(r.id, results[name])
			if !ok {
				continue
			}
			balances = append(balances, UpstreamBalance{ChannelID: r.id, ChannelName: r.name, Currency: b.Currency, Total: b.Total,
				Available: b.Available, Threshold: r.threshold, Low: BalanceLow(b.Total, r.threshold), CheckedAt: b.FetchedAt})
			break
		}
	}
	return counts, balances, nil
}
