package plugin

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The bundled DeepSeek FIM plugin (looked up by id, so other bundled plugins
// don't matter) is a custom-protocol plugin whose tests all pass, and its
// buildRequest hook answers Chat requests the way the README documents.
func TestBundledDeepSeekFIM(t *testing.T) {
	bundled, err := BundledFiles()
	if err != nil {
		t.Fatal(err)
	}
	files, ok := bundled["community.deepseek-fim"]
	if !ok {
		t.Fatal("community.deepseek-fim is not bundled")
	}
	r := Build(files, false)
	if !r.OK || !r.Manifest.CustomProtocol() || r.Manifest.Defaults.BaseURL != "https://api.deepseek.com" {
		t.Fatalf("build = %+v", r.Diagnostics)
	}
	for _, h := range []string{"buildRequest", "parseResponse", "parseStream", "endStream", "normalizeError"} {
		if !r.Manifest.HasHook(h) {
			t.Errorf("hook %s not declared", h)
		}
	}
	s := testService()
	cases := 0
	for name := range files {
		if strings.HasPrefix(name, "tests/") {
			cases++
		}
	}
	if cases < 15 {
		t.Errorf("only %d test cases", cases)
	}
	// Direct check of the hole-marker split and the resolved URL.
	res, err := s.RunTest(context.Background(), files, TestCase{Hook: "buildRequest", Secrets: map[string]string{"apiKey": "k"},
		Request: []byte(`{"model":"deepseek-flash","messages":[{"role":"user","content":"a<|fim_hole|>b"}]}`)})
	if err != nil || !res.OK {
		t.Fatalf("buildRequest: %v %+v", err, res)
	}
	out := string(res.Output)
	for _, want := range []string{`"prompt":"a"`, `"suffix":"b"`, `"url":"https://api.deepseek.com/beta/completions"`, `"Authorization":"Bearer k"`} {
		if !strings.Contains(out, want) {
			t.Errorf("buildRequest output %s lacks %s", out, want)
		}
	}
}

// The plugin type-checks against the SDK declarations with the editor's
// compiler options (needs web/node_modules).
func TestDeepSeekFIMTypeCheck(t *testing.T) {
	tsc, _ := filepath.Abs("../../../web/node_modules/.bin/tsc")
	if _, err := os.Stat(tsc); err != nil {
		t.Skip("tsc not installed (web/node_modules)")
	}
	bundled, _ := BundledFiles()
	dir := t.TempDir()
	files := map[string]string{"sdk.d.ts": SDKTypes}
	for k, v := range bundled["community.deepseek-fim"] {
		files[k] = v
	}
	files["tsconfig.json"] = `{"compilerOptions":{"target":"ES2017","module":"ESNext","moduleResolution":"node","strict":true,"noEmit":true,
	  "lib":["es2020"],"types":[],"ignoreDeprecations":"6.0"},"files":["sdk.d.ts","src/index.ts","src/fim.ts"]}`
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
