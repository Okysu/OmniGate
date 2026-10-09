package plugin

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"omnigate/internal/plugin/engine"
)

func testService() *Service {
	return NewService(nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), engine.Config{})
}

// Every bundled plugin builds, and every tests/*.json case passes.
func TestBundledPluginsPassTheirTests(t *testing.T) {
	bundled, err := BundledFiles()
	if err != nil || len(bundled) == 0 {
		t.Fatalf("bundled = %v %v", len(bundled), err)
	}
	s := testService()
	for id, files := range bundled {
		r := Build(files, false)
		s.checkExports(r)
		if !r.OK {
			t.Fatalf("%s does not build: %+v", id, r.Diagnostics)
		}
		if len(r.Risk) != 0 {
			t.Errorf("%s has risk findings: %+v", id, r.Risk)
		}
		cases := 0
		for name := range files {
			if !strings.HasPrefix(name, "tests/") {
				continue
			}
			cases++
			res, err := s.RunTest(context.Background(), files, TestCase{Case: name})
			if err != nil || !res.OK {
				t.Errorf("%s %s: err=%v result=%+v expectation=%v error=%v", id, name, err, res, deref(res.Expectation), deref(res.Error))
			}
		}
		if cases == 0 {
			t.Errorf("%s has no tests", id)
		}
	}
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func TestTemplatesBuild(t *testing.T) {
	s := testService()
	for _, tpl := range []string{"openai-compatible", "anthropic-compatible", "blank", "custom-protocol"} {
		files, ok := templateFiles(tpl, "acme.test", "测试")
		if !ok {
			t.Fatal(tpl)
		}
		r := Build(files, false)
		s.checkExports(r)
		if !r.OK {
			t.Fatalf("template %s: %+v", tpl, r.Diagnostics)
		}
		for name := range files {
			if strings.HasPrefix(name, "tests/") {
				res, err := s.RunTest(context.Background(), files, TestCase{Case: name})
				if err != nil || !res.OK {
					t.Errorf("template %s %s: %v %+v expectation=%v error=%v", tpl, name, err, res, deref(res.Expectation), deref(res.Error))
				}
			}
		}
	}
}

func TestManifestValidation(t *testing.T) {
	files := map[string]string{"src/index.ts": "export default {}"}
	bad := `{"id":"Bad_ID","name":"","version":"1","sdk":2,"extends":"grpc","homepage":"http://x",
	  "permissions":{"network":["10.0.0.1","api.x.com:443"],"secrets":["apiKey"],"schedule":["custom.y"],"dangerous":["exec"]},
	  "capabilities":{"balance.get":{"output":"money","schedule":{"minInterval":"10s"}},"nope":{"output":"json"}},
	  "hooks":["parseStream"],
	  "configSchema":{"type":"object","properties":{"token":{"type":"string","x-secret":true},"apiKey":{"type":"string"}}},
	  "uiContributions":[{"slot":"page.header","component":{"type":"iframe","bind":"other.cap:/x"}}]}`
	_, ds := ParseManifest([]byte(bad), files, false)
	text := ""
	for _, d := range ds {
		text += d.Message + "\n"
	}
	for _, want := range []string{"id 必须", "name 长度", "语义化版本", "sdk 必须", "https 地址", "extends", "10.0.0.1", "api.x.com:443",
		"危险权限", "output 必须", "不少于 1m", "permissions.schedule 未包含", "能力名 \"nope\"", "custom.y", "parseStream",
		"x-secret", "apiKey 为渠道主密钥保留", "page.header", "iframe", "未声明的能力"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing diagnostic %q in:\n%s", want, text)
		}
	}
	if _, ds := ParseManifest([]byte(`{"id":"builtin.x","name":"x","version":"1.0.0","sdk":1,"extends":"openai.chat","permissions":{}}`), files, false); len(ds) == 0 {
		t.Error("builtin. prefix must be reserved")
	}
	if _, ds := ParseManifest([]byte(`{"id":"a.b","unknown":1}`), files, false); len(ds) == 0 || !strings.Contains(ds[0].Message, "未知字段") {
		t.Error("unknown fields must be rejected")
	}
}

func TestCompileRejectsForeignImports(t *testing.T) {
	_, ds := Compile(map[string]string{"src/index.ts": `import fs from "fs"; export default { fs }`}, "src/index.ts")
	if len(ds) == 0 || !strings.Contains(ds[0].Message, "只允许相对路径导入") || ds[0].File != "src/index.ts" || ds[0].Line != 1 {
		t.Fatalf("diagnostics = %+v", ds)
	}
	_, ds = Compile(map[string]string{"src/index.ts": "export default {\n  x: (1 +\n}"}, "src/index.ts")
	if len(ds) == 0 || ds[0].Line != 3 {
		t.Fatalf("syntax error location = %+v", ds)
	}
}

func TestScanRisks(t *testing.T) {
	files := map[string]string{"src/a.ts": "const f = new Function('return 1')\nog.fetch('https://evil.example.org/x')\nwhile (true) {}\nx.__proto__ = y"}
	got := map[string]bool{}
	for _, f := range ScanRisks(files, []string{"api.good.com"}) {
		got[f.Rule] = true
	}
	for _, r := range []string{"dynamic-code", "undeclared-host", "busy-loop", "proto-pollution"} {
		if !got[r] {
			t.Errorf("rule %s not reported (%v)", r, got)
		}
	}
}

func TestReadZip(t *testing.T) {
	mk := func(names ...string) []byte {
		var buf bytes.Buffer
		zw := zip.NewWriter(&buf)
		for _, n := range names {
			w, _ := zw.Create(n)
			_, _ = w.Write([]byte("{}"))
		}
		_ = zw.Close()
		return buf.Bytes()
	}
	files, err := ReadZip(mk("pkg/manifest.json", "pkg/src/index.ts"))
	if err != nil || files["manifest.json"] == "" || files["src/index.ts"] == "" {
		t.Fatalf("wrapped dir: %v %v", files, err)
	}
	if _, err := ReadZip(mk("manifest.json", "../../etc/passwd")); err == nil {
		t.Fatal("zip slip accepted")
	}
	if _, err := ReadZip([]byte("not a zip")); err == nil {
		t.Fatal("garbage accepted")
	}
}

func TestPermissionDiffAndSemver(t *testing.T) {
	a := Permissions{Network: []string{"a.com"}, Secrets: []string{"apiKey"}}
	b := Permissions{Network: []string{"a.com", "b.com"}, Secrets: []string{"apiKey"}}
	d := DiffPermissions(&a, b)
	if len(d.Added) != 1 || d.Added[0] != "network:b.com" || len(d.Removed) != 0 {
		t.Fatalf("diff = %+v", d)
	}
	if !DiffPermissions(&a, a).Empty() {
		t.Fatal("same permissions must diff empty")
	}
	for _, c := range []struct {
		a, b string
		want int
	}{{"1.0.0", "1.0.1", -1}, {"1.10.0", "1.9.9", 1}, {"1.0.0-rc.1", "1.0.0", -1}, {"2.0.0", "2.0.0", 0}} {
		if got := CompareSemver(c.a, c.b); got != c.want {
			t.Errorf("CompareSemver(%s, %s) = %d", c.a, c.b, got)
		}
	}
}

func TestConfigValidation(t *testing.T) {
	min0 := 0.0
	s := &ConfigSchema{Type: "object", Required: []string{"region", "token"}, Properties: map[string]SchemaProperty{
		"region": {Type: "string", Enum: []any{"cn", "global"}},
		"limit":  {Type: "integer", Minimum: &min0, Default: 5.0},
		"token":  {Type: "string", Secret: true},
	}}
	_, details := s.ValidateConfig(map[string]any{"region": "eu", "limit": 1.5, "extra": true}, map[string]bool{})
	b, _ := json.Marshal(details)
	for _, want := range []string{"pluginConfig.region", "pluginConfig.limit", "pluginConfig.extra", "secrets.token"} {
		if !strings.Contains(string(b), want) {
			t.Errorf("details missing %s: %s", want, b)
		}
	}
	out, details := s.ValidateConfig(map[string]any{"region": "cn"}, map[string]bool{"token": true})
	if len(details) != 0 || out["limit"] != 5.0 {
		t.Fatalf("defaults: %v %v", out, details)
	}
}

func TestManifestCustomProtocolAndBilling(t *testing.T) {
	files := map[string]string{"src/index.ts": "export default {}"}
	diag := func(m string) string {
		_, ds := ParseManifest([]byte(m), files, false)
		var sb strings.Builder
		for _, d := range ds {
			sb.WriteString(d.Message + "\n")
		}
		return sb.String()
	}
	head := `"id":"a.b","name":"x","version":"1.0.0","sdk":1,"permissions":{}`
	cases := map[string][]string{
		`{` + head + `,"protocol":"custom","extends":"openai.chat","hooks":["buildRequest","parseResponse","parseStream"]}`: {"互斥"},
		`{` + head + `,"protocol":"grpc"}`:                                                                         {"protocol 只能是"},
		`{` + head + `,"protocol":"custom","hooks":["buildRequest","transformRequest"]}`:                           {"transformRequest", "必须实现并在 hooks 中声明 parseResponse", "parseStream"},
		`{` + head + `,"extends":"openai.chat","hooks":["parseStream"]}`:                                           {"只用于 protocol"},
		`{` + head + `,"kind":["billing","chat"],"billing":{"meters":{}}}`:                                         {"kind 中的 \"chat\"", "至少一个计量"},
		`{` + head + `,"kind":["billing"],"extends":"openai.chat","billing":{"meters":{"Bad-Name":{"label":""}}}}`: {"不能声明 extends", "计量名 \"Bad-Name\"", "label"},
		`{` + head + `,"extends":"openai.chat","billing":{"meters":{"m":{"label":"x"}}}}`:                          {"kind 不包含 billing"},
	}
	for m, wants := range cases {
		got := diag(m)
		for _, w := range wants {
			if !strings.Contains(got, w) {
				t.Errorf("%s: missing %q in\n%s", m, w, got)
			}
		}
	}
	ok := []string{
		`{` + head + `,"protocol":"custom","hooks":["buildRequest","parseResponse","parseStream","endStream","normalizeError","signRequest"]}`,
		`{` + head + `,"kind":["billing"],"billing":{"meters":{"weighted_tokens":{"label":"x","unit":"token"}}}}`,
		`{` + head + `,"kind":["channel","billing"],"extends":"openai.chat","billing":{"meters":{"m":{"label":"x"}}}}`,
	}
	for _, m := range ok {
		if got := diag(m); got != "" {
			t.Errorf("%s: unexpected diagnostics %s", m, got)
		}
	}
	m, _ := ParseManifest([]byte(ok[0]), files, false)
	if m.ChannelType() != ChannelTypeCustom || !m.CustomProtocol() || fmt.Sprint(m.Kind) != "[channel]" {
		t.Fatalf("custom manifest = %+v", m)
	}
	m, _ = ParseManifest([]byte(ok[1]), files, false)
	if m.ChannelType() != "" || !m.HasKind(KindBilling) {
		t.Fatalf("billing manifest = %+v", m)
	}
}

func TestParseUnits(t *testing.T) {
	for in, want := range map[string]string{`3`: "3", `0.5`: "0.5", `"12.25"`: "12.25", `1e3`: "1000", `0.1234567891`: "0.123456789", `0`: "0"} {
		if got, msg := parseUnits(json.RawMessage(in)); got != want || msg != "" {
			t.Errorf("parseUnits(%s) = %q %q", in, got, msg)
		}
	}
	for _, in := range []string{`-1`, `"abc"`, `"1e3"`, `null`, `{}`, `"-2"`} {
		if _, msg := parseUnits(json.RawMessage(in)); msg == "" {
			t.Errorf("parseUnits(%s) accepted", in)
		}
	}
}

func TestStreamTestCallsAndChatChunks(t *testing.T) {
	s := testService()
	files, _ := templateFiles("custom-protocol", "acme.x", "x")
	res, err := s.RunTest(context.Background(), files, TestCase{Case: "tests/parse-stream.json"})
	if err != nil || !res.OK {
		t.Fatalf("%v %+v", err, res)
	}
	if len(res.Calls) != 5 || res.Calls[0].Hook != "parseStream" || *res.Calls[3].Chunk != 3 || res.Calls[4].Hook != "endStream" ||
		res.Calls[4].Chunk != nil || len(res.Calls[2].Events) != 1 || len(res.Calls[1].Events) != 1 {
		b, _ := json.Marshal(res.Calls)
		t.Fatalf("calls = %s", b)
	}
	chunks := fmt.Sprint(len(res.ChatChunks)) + string(bytes.Join(func() [][]byte {
		var out [][]byte
		for _, c := range res.ChatChunks {
			out = append(out, c)
		}
		return out
	}(), []byte("\n")))
	for _, want := range []string{`"role":"assistant"`, `"reasoning_content":"查一下"`, `"content":"好的"`, `"name":"get_weather"`,
		`"finish_reason":"tool_calls"`, `"prompt_tokens":12`, `"object":"chat.completion.chunk"`} {
		if !strings.Contains(chunks, want) {
			t.Errorf("chat chunks missing %s:\n%s", want, chunks)
		}
	}
	// An error event ends the client stream with an error chunk; an unknown
	// event type fails the case.
	src := `import { definePlugin } from "@omnigate/plugin-sdk"
export default definePlugin({
  buildRequest() { return { url: "/" } },
  parseResponse() { return { choices: [] } },
  parseStream(chunk: Uint8Array) {
    const t = new TextDecoder().decode(chunk)
    if (t === "err") return [{ type: "error", message: "quota gone", status: 429 }]
    if (t === "bad") return [{ type: "bogus" }]
    return [{ type: "delta", content: t }]
  },
})`
	files = map[string]string{"src/index.ts": src, "manifest.json": `{"id":"a.b","name":"x","version":"1.0.0","sdk":1,"protocol":"custom",
	  "permissions":{},"hooks":["buildRequest","parseResponse","parseStream"]}`}
	res, err = s.RunTest(context.Background(), files, TestCase{Hook: "parseStream", Chunks: []string{"hi", "err", "late"}})
	if err != nil || !res.OK || len(res.Calls) != 3 || !strings.Contains(string(res.ChatChunks[len(res.ChatChunks)-1]), "upstream: quota gone") {
		t.Fatalf("error event: %v %+v", err, res)
	}
	res, _ = s.RunTest(context.Background(), files, TestCase{Hook: "parseStream", Chunks: []string{"hi", "bad", "never"}})
	if res.OK || len(res.Calls) != 2 || res.Calls[1].Error == nil || !strings.Contains(deref(res.Error), "bogus") {
		t.Fatalf("bad event: %+v", res)
	}
}

// The editor template and the bundled billing plugin type-check against the
// SDK declarations with the editor's compiler options (needs web/node_modules).
func TestTemplatesTypeCheck(t *testing.T) {
	tsc, _ := filepath.Abs("../../../web/node_modules/.bin/tsc")
	if _, err := os.Stat(tsc); err != nil {
		t.Skip("tsc not installed (web/node_modules)")
	}
	dir := t.TempDir()
	files, _ := templateFiles("custom-protocol", "acme.x", "x")
	bundled, _ := BundledFiles()
	for k, v := range bundled["community.billing-examples"] {
		files["billing/"+k] = v
	}
	files["sdk.d.ts"] = SDKTypes
	files["tsconfig.json"] = `{"compilerOptions":{"target":"ES2017","module":"ESNext","moduleResolution":"node","strict":true,"noEmit":true,
	  "lib":["es2020"],"types":[],"ignoreDeprecations":"6.0"},"files":["sdk.d.ts","src/index.ts","src/acme.ts","billing/src/index.ts"]}`
	for k, v := range files {
		p := filepath.Join(dir, k)
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(v), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if out, err := exec.Command(tsc, "-p", dir).CombinedOutput(); err != nil {
		t.Fatalf("tsc: %v\n%s", err, out)
	}
}
