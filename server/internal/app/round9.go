package app

import (
	"context"

	"github.com/google/uuid"

	"omnigate/internal/plugin"
	"omnigate/internal/protocol"
	"omnigate/internal/subscription"
)

// pluginMeters adapts the plugin service to subscription.CustomMeters
// (billing plugins, docs/contracts/phase9-api.md §3).
type pluginMeters struct{ p *plugin.Service }

func (m pluginMeters) ResolveMeter(ctx context.Context, key, meter string) (subscription.ResolvedMeter, string, error) {
	vid, decl, problem, err := m.p.ResolveMeter(ctx, key, meter)
	return subscription.ResolvedMeter{VersionID: vid, Label: decl.Label, Unit: decl.Unit}, problem, err
}

func (m pluginMeters) Units(ctx context.Context, versionID uuid.UUID, key, meter string, usage protocol.Usage, bc subscription.BillingCtx) string {
	units, _ := m.p.MeterUnits(ctx, versionID, key, meter, usage, plugin.BillingCtx{Model: bc.Model, ServedModel: bc.ServedModel,
		ChannelID: bc.ChannelID, ChannelTier: bc.ChannelTier, UserGroup: bc.UserGroup, Inbound: bc.Inbound,
		ImageCount: bc.ImageCount, AudioSeconds: bc.AudioSeconds})
	return units
}

func (m pluginMeters) Options(ctx context.Context) ([]subscription.MeterOption, error) {
	opts, err := m.p.MeterOptions(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]subscription.MeterOption, len(opts))
	for i, o := range opts {
		out[i] = subscription.MeterOption{Meter: o.Meter, Label: o.Label, Unit: o.Unit, Plugin: &subscription.MeterPlugin{
			ID: o.PluginID, Key: o.PluginKey, Name: o.PluginName, Version: o.Version, VersionID: o.VersionID}}
	}
	return out, nil
}
