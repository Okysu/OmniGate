package seed

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"omnigate/internal/money"
	"omnigate/internal/pricing"
	"omnigate/internal/subscription"
)

// ExportOptions selects the sections of an export.
type ExportOptions struct {
	Prices    bool
	Cost      bool // with Prices: also cost prices (channel by name)
	ModelInfo bool
	Plans     bool
}

// Export reads the current catalog: the price versions effective now, all
// model information and the active plans (catalog order: newest first), in the seed file
// format. It only reads. Warnings (e.g. ambiguous channel names) go to warn.
func Export(ctx context.Context, svc Services, opt ExportOptions, warn io.Writer) (*Catalog, error) {
	cur, err := StoredCurrency(ctx, svc.Pool)
	if err != nil {
		return nil, err
	}
	c := &Catalog{Currency: cur}
	if opt.Prices {
		if c.Prices, err = exportPrices(ctx, svc, opt.Cost, warn); err != nil {
			return nil, err
		}
	}
	if opt.ModelInfo {
		list, err := svc.Info.List(ctx)
		if err != nil {
			return nil, err
		}
		for _, i := range list {
			c.ModelInfo = append(c.ModelInfo, ModelInfoEntry{Model: i.Model, DisplayName: i.DisplayName,
				Description: i.Description, Vendor: i.Vendor, Tags: i.Tags, ContextWindow: i.ContextWindow,
				MaxOutput: i.MaxOutput, Capabilities: i.Capabilities, Hidden: i.Hidden, SortOrder: i.SortOrder})
		}
	}
	if opt.Plans {
		plans, _, err := svc.Plans.ListPlans(ctx, subscription.PlanActive, 0, 1_000_000)
		if err != nil {
			return nil, err
		}
		// Newest first, the catalog's display order: seeding creates new plans
		// last to first, so the target shows them in the same order.
		for _, p := range plans {
			e := PlanEntry{Name: p.Name, Description: p.Description, Duration: p.Duration, Models: p.Models,
				Stackable: p.Stackable, Status: p.Status, Rules: make([]subscription.Rule, len(p.Rules))}
			if p.ListPrice != nil {
				v := p.ListPrice.String()
				e.ListPrice = &v
			}
			for i, r := range p.Rules {
				r.PluginVersionID, r.MeterLabel, r.MeterUnit = nil, "", "" // re-resolved on the target
				e.Rules[i] = r
			}
			c.Plans = append(c.Plans, e)
		}
	}
	return c, nil
}

func exportPrices(ctx context.Context, svc Services, cost bool, warn io.Writer) ([]PriceEntry, error) {
	prices, err := svc.Prices.Effective(ctx, time.Now().UTC())
	if err != nil {
		return nil, err
	}
	byName, byID, err := channelsByName(ctx, svc.Pool)
	if err != nil {
		return nil, err
	}
	out := []PriceEntry{}
	for _, p := range prices {
		e := PriceEntry{Kind: p.Kind, Model: p.Model, InputPerM: p.InputPerM.String(), OutputPerM: p.OutputPerM.String(),
			CacheReadPerM: p.CacheReadPM.String(), CacheWritePerM: p.CacheWritePM.String(), PerRequest: p.PerRequest.String(),
			PerImage: p.PerImage.String(), ImageInputPerM: optString(p.ImageInputPM), AudioInputPerM: optString(p.AudioInputPM),
			AudioOutputPerM: optString(p.AudioOutputPM), PerMinute: p.PerMinute.String(), PerMCharacters: p.PerMCharacters.String(),
			Schedule: p.Schedule, ScheduleTimezone: p.ScheduleTimezone, Tiers: pricing.TierInputs(p.Tiers)}
		if p.Kind == pricing.KindCost {
			if !cost {
				continue
			}
			name, ok := byID[*p.ChannelID]
			if !ok {
				fmt.Fprintf(warn, "警告：cost 价格 %s 的渠道 %s 已不存在，已跳过\n", p.Model, p.ChannelID)
				continue
			}
			if n := len(byName[name]); n > 1 {
				fmt.Fprintf(warn, "警告：cost 价格 %s 的渠道名 %q 不唯一（%d 个），导入时会报错，请先改名\n", p.Model, name, n)
			}
			e.ChannelName = &name
		}
		out = append(out, e)
	}
	return out, nil
}

func optString(a *money.Amount) *string {
	if a == nil {
		return nil
	}
	s := a.String()
	return &s
}

// Write encodes c as indented JSON (the seed file format).
func (c *Catalog) Write(w io.Writer) error {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	return enc.Encode(c)
}
