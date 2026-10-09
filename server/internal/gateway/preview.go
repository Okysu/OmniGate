package gateway

import (
	"context"
	"math"
	"slices"
	"time"

	"github.com/google/uuid"

	"omnigate/internal/channel"
	"omnigate/internal/identity"
	"omnigate/internal/protocol"
	"omnigate/internal/routing"
)

// Preview reports how a request for model by userID (with role) through the
// inbound protocol would be routed: the matching rule and the candidates in
// attempt order (POST /api/admin/routes/preview) — own → shared → platform,
// each candidate tagged with its tier. It uses the same candidate selection
// and ordering as live traffic but no API key policy; random strategies yield
// one sample order and round robin peeks at the next turn.
func (g *Gateway) Preview(ctx context.Context, userID uuid.UUID, role identity.Role, model, inbound string) (*routing.Preview, error) {
	maxAttempts := g.maxAttempts(ctx)
	out := &routing.Preview{Strategy: routing.StrategyPriority, MaxAttempts: maxAttempts,
		Candidates: []routing.PreviewCandidate{}, FallbackModels: []string{}, TierOrder: []string{}}
	for _, t := range channel.Tiers {
		out.TierOrder = append(out.TierOrder, string(t))
	}
	rule := g.match(model, role)
	if rule != nil {
		out.Rule = &routing.RuleRef{ID: rule.ID, Name: rule.Name}
		out.Strategy, out.MaxAttempts = rule.Strategy, rule.Retry.MaxAttempts
		out.FallbackModels = slices.Clone(rule.FallbackModels)
	}
	now := time.Now().UTC()
	usable, unsupported := g.candidates(userID, nil, model, inbound, rule)
	seen := map[uuid.UUID]bool{}
	add := func(c routeCand, skipped string) {
		seen[c.rt.ID] = true
		pc := routing.PreviewCandidate{ChannelID: c.rt.ID, ChannelName: c.rt.Name, ChannelType: c.rt.Type, Tier: string(c.tier), Priority: c.priority,
			Weight: c.weight, UpstreamDialect: c.upstream, ConversionHops: c.hops, Breaker: g.reg.Breaker.State(c.rt.ID)}
		if v := g.latencyOf(c.rt.ID, model); v != nil {
			ms := int64(math.Round(*v))
			pc.LatencyMs = &ms
		}
		if cost := g.costOf(ctx, c.rt, model, now); cost != nil {
			s := cost.String()
			pc.CostPerM = &s
		}
		if skipped == "" && pc.Breaker == "open" {
			skipped = "熔断中，冷却期内跳过"
		}
		if skipped != "" {
			pc.Skipped = &skipped
		}
		out.Candidates = append(out.Candidates, pc)
	}
	for _, c := range g.arrange(ctx, usable, model, rule, now, false) {
		add(c, "")
	}
	for _, c := range unsupported {
		msg := "渠道不支持该入口协议"
		if protocol.OpenAIOnly(inbound) {
			msg = "该接口仅由 OpenAI 兼容渠道提供"
		}
		add(c, msg)
	}
	if rule != nil {
		// Targets the user cannot use are listed so administrators see why.
		for _, t := range rule.Targets {
			if seen[t.ChannelID] {
				continue
			}
			rt, ok := g.reg.Get(t.ChannelID)
			if !ok {
				continue
			}
			if _, serves := rt.UpstreamModel(model); !serves {
				continue
			}
			c := routeCand{rt: rt, tier: rt.TierFor(userID), priority: rt.Priority, weight: rt.Weight, upstream: rt.Dialect(inbound, model)}
			c.hops = protocol.ConversionHops(inbound, c.upstream)
			if t.Priority != nil {
				c.priority = *t.Priority
			}
			if t.Weight != nil {
				c.weight = *t.Weight
			}
			add(c, "该用户无权使用此渠道")
		}
	}
	return out, nil
}
