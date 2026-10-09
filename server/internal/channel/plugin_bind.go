package channel

import (
	"context"
	"errors"
	"maps"
	"slices"

	"github.com/google/uuid"

	"omnigate/internal/apperr"
	"omnigate/internal/plugin"
	"omnigate/internal/plugin/engine"
)

// pluginChange describes a version switch for the audit log.
type pluginChange struct {
	From, To string
}

// preparePlugin resolves the plugin version a create/update targets and
// fills defaults (type, base URL, models) from its manifest before Input
// validation runs.
func (s *Service) preparePlugin(ctx context.Context, c *Channel, in *Input, create bool) (*plugin.Loaded, error) {
	if s.plugins == nil {
		if in.PluginVersionID != nil || in.PluginConfig != nil || in.Secrets != nil {
			return nil, apperr.Validation("插件功能未启用", nil)
		}
		return nil, nil
	}
	var vid uuid.UUID
	switch {
	case in.PluginVersionID != nil:
		vid = *in.PluginVersionID
	case !create && c.PluginVersionID != nil:
		vid = *c.PluginVersionID
	default:
		t := c.Type
		if in.Type != nil {
			t = *in.Type
		}
		if t == TypeCustom {
			return nil, apperr.Validation("参数校验失败", map[string]any{"pluginVersionId": "自定义协议渠道必须选择实现该协议的插件版本"})
		}
		if t != TypeOpenAI && t != TypeAnthropic {
			return nil, nil // Input validation reports the bad type
		}
		id, err := s.plugins.BuiltinVersionID(ctx, t)
		if err != nil {
			return nil, err
		}
		vid = id
	}
	l, err := s.plugins.Loaded(ctx, vid)
	if err != nil {
		var ae *apperr.Error
		if errors.As(err, &ae) && ae.Kind == apperr.KindNotFound {
			return nil, apperr.Validation("参数校验失败", map[string]any{"pluginVersionId": "插件版本不存在"})
		}
		return nil, err
	}
	changing := create || c.PluginVersionID == nil || *c.PluginVersionID != vid
	if changing {
		if l.Approval != "approved" {
			return nil, apperr.New(apperr.KindConflict, "plugin_not_approved", "该插件版本尚未通过权限审批")
		}
		if !s.plugins.Enabled(l.PluginID) {
			return nil, apperr.New(apperr.KindConflict, "plugin_disabled", "该插件已停用")
		}
	}
	if !create && c.Plugin != nil && c.Plugin.ID != l.PluginID && in.PluginVersionID != nil {
		return nil, apperr.Validation("参数校验失败", map[string]any{"pluginVersionId": "只能在同一插件的版本之间切换；更换插件请新建渠道"})
	}
	ct := l.Manifest.ChannelType()
	if ct == "" {
		return nil, apperr.Validation("参数校验失败", map[string]any{"pluginVersionId": "该插件不是渠道插件（kind 不含 channel）"})
	}
	if in.Type != nil && *in.Type != ct {
		return nil, apperr.Validation("参数校验失败", map[string]any{"type": "渠道类型由插件决定（" + ct + "）"})
	}
	if create {
		in.Type = &ct
		if in.BaseURL == nil && l.Manifest.Defaults.BaseURL != "" {
			u := l.Manifest.Defaults.BaseURL
			in.BaseURL = &u
		}
		if in.Models == nil && len(l.Manifest.Defaults.Models) > 0 {
			ms := make([]ModelMap, len(l.Manifest.Defaults.Models))
			for i, m := range l.Manifest.Defaults.Models {
				ms[i] = ModelMap{Model: m.Model, UpstreamModel: m.UpstreamModel}
			}
			in.Models = &ms
		}
	} else if c.Type != ct {
		// Legacy channels or plugins that change protocol: type follows the plugin.
		c.Type = ct
	}
	return l, nil
}

// applyPlugin validates plugin config and secrets after Input validation and
// runs config migration on version changes.
func (s *Service) applyPlugin(ctx context.Context, c *Channel, in Input, l *plugin.Loaded, create bool) (map[string]string, *pluginChange, error) {
	if l == nil {
		return nil, nil, nil
	}
	var change *pluginChange
	cfg := maps.Clone(c.PluginConfig)
	if cfg == nil {
		cfg = map[string]any{}
	}
	if !create && c.PluginVersionID != nil && *c.PluginVersionID != l.VersionID {
		from := ""
		if c.Plugin != nil {
			from = c.Plugin.Version
		}
		change = &pluginChange{From: from, To: l.Version}
		migrated, err := l.MigrateConfig(ctx, plugin.ChannelEnv{ID: c.ID, Name: c.Name, BaseURL: c.BaseURL, Config: cfg}, cfg, from)
		if err != nil {
			msg := err.Error()
			var pe *engine.Error
			if errors.As(err, &pe) {
				msg = pe.Message
			}
			return nil, nil, apperr.Validation("配置迁移失败："+msg, nil)
		}
		cfg = migrated
	}
	if in.PluginConfig != nil {
		cfg = *in.PluginConfig
	}
	allowed := l.Manifest.SecretFields()
	secrets := map[string]string{}
	set := map[string]bool{}
	for k := range c.SecretFields {
		if slices.Contains(allowed, k) {
			set[k] = true
		} else {
			secrets[k] = "" // drop secrets the new version no longer declares
		}
	}
	if in.Secrets != nil {
		for k, v := range *in.Secrets {
			if !slices.Contains(allowed, k) {
				return nil, nil, apperr.Validation("参数校验失败", map[string]any{"secrets." + k: "插件未声明该敏感字段"})
			}
			secrets[k] = v
			set[k] = v != ""
		}
	}
	out, details := l.Manifest.ConfigSchema.ValidateConfig(cfg, set)
	if len(details) > 0 {
		return nil, nil, apperr.Validation("插件配置校验失败", details)
	}
	c.PluginConfig = out
	vid := l.VersionID
	c.PluginVersionID = &vid
	c.Plugin = &PluginRef{ID: l.PluginID, Key: l.Key, Name: l.Name, Version: l.Version, VersionID: l.VersionID}
	return secrets, change, nil
}
