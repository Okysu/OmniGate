package app_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"omnigate/internal/plugin"
	"omnigate/internal/plugin/engine"
)

func switchPluginFiles(key, protocol string, required bool) map[string]string {
	requiredFields := []string{}
	if required {
		requiredFields = append(requiredFields, "shared")
	}
	manifest := map[string]any{
		"id": key, "name": key, "version": "1.0.0", "sdk": 1, "extends": protocol, "entry": "src/index.ts",
		"defaults": map[string]any{},
		"configSchema": map[string]any{"type": "object", "properties": map[string]any{
			"region": map[string]any{"type": "string", "default": "new-default"},
			"shared": map[string]any{"type": "string", "x-secret": true},
			"legacy": map[string]any{"type": "string", "x-secret": true},
		}, "required": requiredFields},
		"permissions":  map[string]any{"network": []string{}, "secrets": []string{"shared", "legacy"}, "schedule": []string{}},
		"capabilities": map[string]any{"custom.inspect": map[string]any{"output": "json", "userTriggerable": true}},
		"hooks":        []string{}, "uiContributions": []any{},
	}
	raw, _ := json.Marshal(manifest)
	return map[string]string{"manifest.json": string(raw), "src/index.ts": `import { definePlugin } from "@omnigate/plugin-sdk"
export default definePlugin({
  migrateConfig() { throw new Error("must not migrate across different plugins") },
  capabilities: { "custom.inspect"(_input, ctx) { return { region: ctx.config.region } } }
})`}
}

func TestChannelSwitchPlugin(t *testing.T) {
	e := setupGateway(t)
	up := newFakeUpstream(t)
	makePlugin := func(key, protocol string, required bool) (string, map[string]any) {
		p := e.mustDo(e.admin, http.MethodPost, "/api/plugins", map[string]any{"id": key, "name": key, "template": "blank"}, 201)
		id := p["id"].(string)
		return id, e.publishPlugin(id, switchPluginFiles(key, protocol, required), 1, true)
	}
	_, source := makePlugin("acme.source", "openai.chat", false)
	_, target := makePlugin("acme.target", "anthropic.messages", true)
	ch := e.mustDo(e.admin, http.MethodPost, "/api/channels", map[string]any{
		"name": "keep channel", "pluginVersionId": source["id"], "baseUrl": up.srv.URL, "apiKey": "sk-keep-channel-key",
		"models":       []any{map[string]any{"model": "switch-model", "upstreamModel": "up-model", "upstreamProtocol": "responses"}},
		"pluginConfig": map[string]any{"region": "old-config"}, "secrets": map[string]any{"shared": "old-shared", "legacy": "old-legacy"},
	}, 201)
	id := ch["id"].(string)
	e.mustDo(e.admin, http.MethodPost, "/api/channels/"+id+"/capabilities/custom.inspect", nil, 200)
	// Neither the same name nor matching secret schema grants a new plugin old credentials.
	resp, out := e.admin.do(http.MethodPatch, "/api/channels/"+id, map[string]any{"pluginVersionId": target["id"], "version": ch["version"]})
	if resp.StatusCode != 422 || !strings.Contains(fmt.Sprint(out), "secrets.shared") {
		t.Fatalf("missing new credential = %d %v", resp.StatusCode, out)
	}
	unchanged := e.mustDo(e.admin, http.MethodGet, "/api/channels/"+id, nil, 200)
	if unchanged["plugin"].(map[string]any)["key"] != "acme.source" || unchanged["version"] != ch["version"] {
		t.Fatal("failed switch changed stored channel")
	}
	// A different owner cannot switch this channel.
	e.mustDo(e.carol, http.MethodPatch, "/api/channels/"+id, map[string]any{"pluginVersionId": target["id"], "version": ch["version"]}, 404)
	switched := e.mustDo(e.admin, http.MethodPatch, "/api/channels/"+id, map[string]any{
		"pluginVersionId": target["id"], "version": ch["version"], "secrets": map[string]any{"shared": "new-shared"},
	}, 200)
	if switched["id"] != id || switched["type"] != "anthropic" || switched["name"] != ch["name"] || switched["baseUrl"] != ch["baseUrl"] || switched["secret"].(map[string]any)["hint"] != ch["secret"].(map[string]any)["hint"] {
		t.Fatalf("switch lost channel identity: %v", switched)
	}
	if switched["pluginConfig"].(map[string]any)["region"] != "new-default" || switched["secretFields"].(map[string]any)["legacy"] != nil {
		t.Fatalf("old config or secrets reused: %v", switched)
	}
	models := switched["models"].([]any)
	if len(models) != 1 || models[0].(map[string]any)["model"] != "switch-model" || models[0].(map[string]any)["upstreamProtocol"] == "responses" {
		t.Fatalf("invalid model mapping after protocol change: %v", models)
	}
	caps := e.mustDo(e.admin, http.MethodGet, "/api/channels/"+id+"/capabilities", nil, 200)
	if len(caps["results"].(map[string]any)) != 0 {
		t.Fatal("old plugin capability result still visible")
	}
	e.mustDo(e.admin, http.MethodPost, "/api/channels/"+id+"/capabilities/custom.inspect", nil, 200)
	// A late result from the previous version must not replace the current result.
	svc := plugin.NewService(e.pool, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), engine.Config{})
	if err := svc.SaveResult(context.Background(), uuid.MustParse(id), uuid.MustParse(source["id"].(string)), "custom.inspect", &plugin.CapabilityResult{
		OK: true, Output: json.RawMessage(`{"region":"late-old"}`), FetchedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	caps = e.mustDo(e.admin, http.MethodGet, "/api/channels/"+id+"/capabilities", nil, 200)
	if caps["results"].(map[string]any)["custom.inspect"].(map[string]any)["output"].(map[string]any)["region"] != "new-default" {
		t.Fatal("late result replaced current plugin output")
	}
	_, key := e.key(e.admin, map[string]any{"name": "switch-check"})
	time.Sleep(80 * time.Millisecond)
	code, _, raw := readBody(gwPost(t, context.Background(), e.h.srv.URL, "/v1/chat/completions", key, `{"model":"switch-model","messages":[{"role":"user","content":"hello"}]}`))
	if code != 200 || !strings.Contains(raw, "hi from claude") || !strings.Contains(fmt.Sprint(up.lastKey.Load()), "sk-keep-channel-key") {
		t.Fatalf("new protocol not usable with preserved key: %d %s key=%v", code, raw, up.lastKey.Load())
	}
	// The switch still uses optimistic concurrency.
	e.mustDo(e.admin, http.MethodPatch, "/api/channels/"+id, map[string]any{"pluginVersionId": source["id"], "version": ch["version"]}, 409)
	// Disabled / unapproved targets remain forbidden.
	targetID := switched["plugin"].(map[string]any)["id"].(string)
	p := e.mustDo(e.admin, http.MethodGet, "/api/plugins/"+targetID, nil, 200)
	e.mustDo(e.admin, http.MethodPatch, "/api/plugins/"+targetID, map[string]any{"status": "disabled", "version": p["version"]}, 200)
	back := e.mustDo(e.admin, http.MethodPatch, "/api/channels/"+id, map[string]any{"pluginVersionId": source["id"], "version": switched["version"]}, 200)
	e.mustDo(e.admin, http.MethodPatch, "/api/channels/"+id, map[string]any{"pluginVersionId": target["id"], "version": back["version"]}, 409)
}
