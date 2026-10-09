package plugin

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"path"
	"strings"

	"github.com/google/uuid"

	"omnigate/internal/platform/db"
)

//go:embed all:bundled
var bundledFS embed.FS

//go:embed sdk/omnigate-plugin-sdk.d.ts
var SDKTypes string

// Built-in plugin keys (Go-native protocol support, no JavaScript).
const (
	BuiltinOpenAI    = "builtin.openai"
	BuiltinAnthropic = "builtin.anthropic"
)

func builtinManifests() []Manifest {
	return []Manifest{
		{ID: BuiltinOpenAI, Name: "OpenAI 兼容", Version: "1.0.0", SDK: SDKVersion, Author: "OmniGate",
			Description: "内置：OpenAI Chat Completions（可选 Responses）兼容的上游，适用于 OpenAI、DeepSeek、通义、vLLM、Ollama 等。",
			Extends:     "openai.chat", Defaults: Defaults{BaseURL: "https://api.openai.com/v1"},
			Permissions:  Permissions{Network: []string{}, Secrets: []string{}, Schedule: []string{}, Dangerous: []string{}},
			Capabilities: map[string]Capability{}, Hooks: []string{}, UIContributions: []json.RawMessage{}},
		{ID: BuiltinAnthropic, Name: "Anthropic 兼容", Version: "1.0.0", SDK: SDKVersion, Author: "OmniGate",
			Description: "内置：Anthropic Messages 兼容的上游（透传 anthropic-version / anthropic-beta）。",
			Extends:     "anthropic.messages", Defaults: Defaults{BaseURL: "https://api.anthropic.com"},
			Permissions:  Permissions{Network: []string{}, Secrets: []string{}, Schedule: []string{}, Dangerous: []string{}},
			Capabilities: map[string]Capability{}, Hooks: []string{}, UIContributions: []json.RawMessage{}},
	}
}

// BundledFiles returns the embedded example plugins keyed by plugin id.
func BundledFiles() (map[string]map[string]string, error) {
	out := map[string]map[string]string{}
	err := fs.WalkDir(bundledFS, "bundled", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel := strings.TrimPrefix(p, "bundled/")
		id, file, ok := strings.Cut(rel, "/")
		if !ok {
			return nil
		}
		b, err := bundledFS.ReadFile(p)
		if err != nil {
			return err
		}
		if out[id] == nil {
			out[id] = map[string]string{}
		}
		out[id][path.Clean(file)] = string(b)
		return nil
	})
	return out, err
}

// Seed installs built-in plugins and bundled examples. Bundled plugins are
// trusted (shipped with the binary) and auto-approved; channels are never
// moved to a new version automatically.
func (s *Service) Seed(ctx context.Context) error {
	for _, m := range builtinManifests() {
		if err := s.seedVersion(ctx, m, "builtin", nil, ""); err != nil {
			return fmt.Errorf("seed %s: %w", m.ID, err)
		}
	}
	bundled, err := BundledFiles()
	if err != nil {
		return err
	}
	for id, files := range bundled {
		r := Build(files, false)
		s.checkExports(r)
		if !r.OK {
			return fmt.Errorf("bundled plugin %s does not build: %+v", id, r.Diagnostics)
		}
		if err := s.seedVersion(ctx, *r.Manifest, "bundled", files, r.bundle); err != nil {
			return fmt.Errorf("seed %s: %w", id, err)
		}
	}
	return s.RefreshStatus(ctx)
}

func (s *Service) seedVersion(ctx context.Context, m Manifest, source string, files map[string]string, bundle string) error {
	return db.InTx(ctx, s.pool, func(tx db.Tx) error {
		var pid uuid.UUID
		var curSource string
		err := tx.QueryRow(ctx, `SELECT id, source FROM plugins WHERE plugin_key = $1 FOR UPDATE`, m.ID).Scan(&pid, &curSource)
		switch {
		case db.IsNoRows(err):
			pid = uuid.Must(uuid.NewV7())
			if _, err := tx.Exec(ctx, `INSERT INTO plugins (id, plugin_key, name, description, author, homepage, source)
				VALUES ($1, $2, $3, $4, $5, $6, $7)`, pid, m.ID, m.Name, m.Description, m.Author, m.Homepage, source); err != nil {
				return err
			}
		case err != nil:
			return err
		case curSource != source:
			return nil // replaced by an uploaded/edited plugin with the same id: leave it alone
		}
		// Only install versions newer than anything already present (including
		// versions published from the editor on top of a bundled plugin).
		rows, err := tx.Query(ctx, `SELECT version FROM plugin_versions WHERE plugin_id = $1`, pid)
		if err != nil {
			return err
		}
		for rows.Next() {
			var v string
			if err := rows.Scan(&v); err != nil {
				rows.Close()
				return err
			}
			if CompareSemver(v, m.Version) >= 0 {
				rows.Close()
				return nil
			}
		}
		rows.Close()
		if files == nil {
			files = map[string]string{}
		}
		manifest, _ := json.Marshal(m)
		filesJSON, _ := json.Marshal(files)
		var b *string
		if bundle != "" {
			b = &bundle
		}
		note := "随 OmniGate 分发，自动批准"
		hash := ContentHash(files)
		if source == "builtin" {
			hash = "builtin:" + m.ID + "@" + m.Version
		}
		if _, err := tx.Exec(ctx, `INSERT INTO plugin_versions (id, plugin_id, version, manifest, files, bundle, content_hash, risk,
			approval, approved_at, approval_note) VALUES ($1, $2, $3, $4, $5, $6, $7, '[]', 'approved', now(), $8)`,
			uuid.Must(uuid.NewV7()), pid, m.Version, manifest, filesJSON, b, hash, note); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE plugins SET name = $2, description = $3, author = $4, homepage = $5, updated_at = now() WHERE id = $1`,
			pid, m.Name, m.Description, m.Author, m.Homepage)
		return err
	})
}

// BuiltinVersionID returns the approved version id of a built-in plugin for a channel type.
func (s *Service) BuiltinVersionID(ctx context.Context, channelType string) (uuid.UUID, error) {
	key := BuiltinOpenAI
	if channelType == "anthropic" {
		key = BuiltinAnthropic
	}
	var id uuid.UUID
	err := s.pool.QueryRow(ctx, `SELECT v.id FROM plugin_versions v JOIN plugins p ON p.id = v.plugin_id
		WHERE p.plugin_key = $1 AND v.approval = 'approved' ORDER BY v.published_at DESC LIMIT 1`, key).Scan(&id)
	return id, err
}

// ---- templates for new editor plugins ----

func templateFiles(template, id, name string) (map[string]string, bool) {
	ext := "openai.chat"
	base := "https://api.example.com/v1"
	modelsPath := "/models"
	authHeader := "Authorization: `Bearer ${og.secret(\"apiKey\")}`"
	switch template {
	case "openai-compatible", "blank":
	case "anthropic-compatible":
		ext, base, modelsPath = "anthropic.messages", "https://api.example.com", "/v1/models"
		authHeader = `"x-api-key": og.secret("apiKey"), "anthropic-version": "2023-06-01"`
	case "custom-protocol":
		return customProtocolTemplate(id, name), true
	default:
		return nil, false
	}
	manifest := map[string]any{
		"id": id, "name": name, "version": "0.1.0", "sdk": SDKVersion, "description": "", "extends": ext, "entry": "src/index.ts",
		"defaults":     map[string]any{"baseUrl": base, "models": []any{}},
		"permissions":  map[string]any{"network": []string{"$baseUrl"}, "secrets": []string{"apiKey"}, "schedule": []string{}},
		"capabilities": map[string]any{}, "hooks": []string{}, "uiContributions": []any{},
	}
	src := "import { definePlugin } from \"@omnigate/plugin-sdk\"\n\nexport default definePlugin({\n  capabilities: {},\n})\n"
	files := map[string]string{}
	if template != "blank" {
		manifest["capabilities"] = map[string]any{
			"models.list":  map[string]any{"output": "models", "userTriggerable": true, "label": "模型列表"},
			"health.check": map[string]any{"output": "health", "userTriggerable": true, "label": "健康检查"},
		}
		manifest["uiContributions"] = []any{map[string]any{
			"slot": "channel.detail.capabilities", "title": "上游模型",
			"component": map[string]any{"type": "table", "rowsBind": "models.list:/models", "columns": []any{map[string]any{"key": "id", "label": "模型 ID"}}},
			"actions":   []any{map[string]any{"label": "同步模型列表", "capability": "models.list"}},
		}}
		src = fmt.Sprintf(`import { definePlugin, type Ctx } from "@omnigate/plugin-sdk"

async function listModels(ctx: Ctx) {
  const res = await og.fetch(ctx.channel.baseUrl + %q, {
    headers: { %s },
  })
  if (!res.ok) throw new Error("上游返回 " + res.status)
  return res.json()
}

export default definePlugin({
  capabilities: {
    async "models.list"(_input, ctx) {
      const j = await listModels(ctx)
      return { models: (j.data ?? []).map((m: { id: string }) => ({ id: m.id })) }
    },
    async "health.check"(_input, ctx) {
      const started = Date.now()
      await listModels(ctx)
      return { ok: true, latencyMs: Date.now() - started }
    },
  },
})
`, modelsPath, authHeader)
		files["tests/models.json"] = fmt.Sprintf(`{
  "name": "模型列表",
  "capability": "models.list",
  "secrets": { "apiKey": "sk-test" },
  "fetch": [
    { "match": { "url": %q }, "response": { "status": 200, "json": { "data": [{ "id": "model-a" }] } } }
  ],
  "expect": { "output": { "models": [{ "id": "model-a" }] } }
}
`, base+modelsPath)
	}
	mj, _ := json.MarshalIndent(manifest, "", "  ")
	files["manifest.json"] = string(mj) + "\n"
	files["src/index.ts"] = src
	files["README.md"] = "# " + name + "\n\n在这里描述插件的用途、配置项与权限。\n"
	return files, true
}
