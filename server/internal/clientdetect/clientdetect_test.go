package clientdetect

import (
	"net/http"
	"slices"
	"testing"
)

// hdr builds a header set from name/value pairs.
func hdr(kv ...string) http.Header {
	h := http.Header{}
	for i := 0; i+1 < len(kv); i += 2 {
		h.Set(kv[i], kv[i+1])
	}
	return h
}

func TestDetect(t *testing.T) {
	chromeUA := "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0.0.0 Safari/537.36"
	stainlessJS := []string{"X-Stainless-Lang", "js", "X-Stainless-Package-Version", "0.60.0", "X-Stainless-Os", "MacOS",
		"X-Stainless-Runtime", "node", "X-Stainless-Runtime-Version", "v22.11.0"}
	cases := []struct {
		name    string
		h       http.Header
		id, ver string
	}{
		// Claude Code: claude-cli UA over the Anthropic SDK's Stainless headers.
		{"claude code", hdr(append([]string{"User-Agent", "claude-cli/2.0.14 (external, cli)", "X-App", "cli",
			"Anthropic-Version", "2023-06-01", "Anthropic-Beta", "claude-code-20250219"}, stainlessJS...)...), "claude-code", "2.0.14"},
		{"claude code inside desktop", hdr("User-Agent", "claude-cli/2.1.187 (external, claude-desktop-3p, agent-sdk/0.3.187)"), "claude-code", "2.1.187"},
		{"claude code session header", hdr("User-Agent", "Mozilla/5.0", "X-Claude-Code-Session-Id", "4f1c"), "claude-code", ""},
		{"claude code x-app", hdr(append([]string{"X-App", "cli", "Anthropic-Version", "2023-06-01"}, stainlessJS...)...), "claude-code", ""},
		// Codex: originator header and codex_* User-Agent.
		{"codex cli", hdr("User-Agent", "codex_cli_rs/0.46.0 (Mac OS 15.6.1; arm64) iTerm.app/3.6.1", "Originator", "codex_cli_rs",
			"Session_id", "0199a"), "codex", "0.46.0"},
		{"codex vscode", hdr("User-Agent", "codex_vscode/0.4.15 (Windows 10.0.26100; x86_64) unknown", "Originator", "codex_vscode"), "codex", "0.4.15"},
		{"codex desktop", hdr("User-Agent", "Codex Desktop/0.142.2 (Mac OS 15.7.3; arm64) unknown (Codex Desktop; 26.623.31921)"), "codex", "0.142.2"},
		{"codex originator only", hdr("User-Agent", "reqwest/0.12", "Originator", "codex_exec"), "codex", ""},
		{"codex turn header", hdr("User-Agent", "Mozilla/5.0", "X-Codex-Turn-Metadata", "{}"), "codex", ""},
		{"gemini cli", hdr("User-Agent", "GeminiCLI/0.1.5 (linux; x64)"), "gemini-cli", "0.1.5"},
		{"opencode", hdr("User-Agent", "opencode/0.15.2 ai-sdk/provider-utils/3.0.9 runtime/node.js/22"), "opencode", "0.15.2"},
		{"opencode local build", hdr("User-Agent", "opencode/local ai-sdk/provider-utils/4.0.23 runtime/node.js/24"), "opencode", ""},
		{"copilot ua", hdr("User-Agent", "GitHubCopilotChat/0.22.4", "Editor-Version", "vscode/1.95.0"), "github-copilot", "0.22.4"},
		{"copilot integration header", hdr("User-Agent", "node-fetch", "Copilot-Integration-Id", "vscode-chat"), "github-copilot", ""},
		{"kilo code", hdr(append([]string{"User-Agent", "Kilo-Code/4.80.0", "X-Title", "Kilo Code", "HTTP-Referer", "https://kilocode.example"},
			stainlessJS...)...), "kilo-code", "4.80.0"},
		{"roo code", hdr(append([]string{"User-Agent", "RooCode/3.25.0", "X-Title", "Roo Code"}, stainlessJS...)...), "roo-code", "3.25.0"},
		{"cline title", hdr(append([]string{"User-Agent", "OpenAI/JS 4.96.0", "X-Title", "Cline", "HTTP-Referer", "https://cline.example"},
			stainlessJS...)...), "cline", ""},
		{"continue", hdr("User-Agent", "Continue/1.2.3"), "continue", "1.2.3"},
		{"aider title", hdr("User-Agent", "OpenAI/Python 1.99.1", "X-Stainless-Lang", "python", "X-Title", "Aider"), "aider", ""},
		{"cursor", hdr("User-Agent", "Mozilla/5.0 (Macintosh) AppleWebKit/537.36 (KHTML, like Gecko) Cursor/1.4.5 Chrome/132.0 Electron/34.5.1 Safari/537.36"),
			"cursor", "1.4.5"},
		{"zed", hdr("User-Agent", "Zed/0.199.4+stable.211 (macos; aarch64)"), "zed", "0.199.4+stable.211"},
		// Chat applications.
		{"cherry studio", hdr(append([]string{"User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) " +
			"CherryStudio/1.7.8 Chrome/140.0.7339.249 Electron/38.7.0 Safari/537.36", "X-Title", "Cherry Studio"}, stainlessJS...)...), "cherry-studio", "1.7.8"},
		{"cherry studio title only", hdr(append([]string{"User-Agent", chromeUA, "X-Title", "Cherry Studio"}, stainlessJS...)...), "cherry-studio", ""},
		{"chatbox", hdr("User-Agent", "Mozilla/5.0 (Macintosh) AppleWebKit/537.36 (KHTML, like Gecko) xyz.chatboxapp.app/1.21.1 Chrome/134.0 Electron/35.7.5 Safari/537.36"),
			"chatbox", "1.21.1"},
		{"lobechat title", hdr(append([]string{"User-Agent", "OpenAI/JS 4.104.0", "X-Title", "LobeHub"}, stainlessJS...)...), "lobechat", ""},
		{"nextchat", hdr("User-Agent", "NextChat/2.16.0", "X-Title", "NextChat"), "nextchat", "2.16.0"},
		{"rikkahub", hdr("User-Agent", "RikkaHub-Android/2.2.6"), "rikkahub", "2.2.6"},
		{"kelivo", hdr("User-Agent", "Kelivo"), "kelivo", ""},
		{"claude desktop", hdr("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Claude/1.15962.1 "+
			"Chrome/148.0.7778.254 Electron/42.4.0 Safari/537.36"), "claude-desktop", "1.15962.1"},
		// SDKs.
		{"openai python", hdr("User-Agent", "OpenAI/Python 1.99.1", "X-Stainless-Lang", "python", "X-Stainless-Package-Version", "1.99.1",
			"X-Stainless-Runtime", "CPython"), "openai-sdk-python", "1.99.1"},
		{"openai js", hdr(append([]string{"User-Agent", "OpenAI/JS 5.12.0"}, "X-Stainless-Lang", "js", "X-Stainless-Package-Version", "5.12.0")...),
			"openai-sdk-js", "5.12.0"},
		{"openai js in browser", hdr("User-Agent", chromeUA, "X-Stainless-Lang", "js", "X-Stainless-Package-Version", "5.12.0"), "openai-sdk-js", "5.12.0"},
		{"openai go", hdr("User-Agent", "OpenAI/Go 1.12.0", "X-Stainless-Lang", "go", "X-Stainless-Package-Version", "1.12.0"), "openai-sdk", "1.12.0"},
		{"anthropic python", hdr("User-Agent", "Anthropic/Python 0.64.0", "X-Stainless-Lang", "python", "X-Stainless-Package-Version", "0.64.0",
			"Anthropic-Version", "2023-06-01"), "anthropic-sdk-python", "0.64.0"},
		{"anthropic js", hdr(append([]string{"User-Agent", "Anthropic/JS 0.60.0", "Anthropic-Version", "2023-06-01"}, stainlessJS...)...),
			"anthropic-sdk-js", "0.60.0"},
		{"anthropic js in browser", hdr(append([]string{"User-Agent", chromeUA, "Anthropic-Version", "2023-06-01",
			"Anthropic-Dangerous-Direct-Browser-Access", "true"}, stainlessJS...)...), "anthropic-sdk-js", "0.60.0"},
		{"anthropic java", hdr("User-Agent", "Anthropic/Java 2.5.0", "X-Stainless-Lang", "java", "X-Stainless-Package-Version", "2.5.0"),
			"anthropic-sdk", "2.5.0"},
		{"vercel ai sdk", hdr("User-Agent", "ai-sdk/openai-compatible/1.0.11 ai-sdk/provider-utils/3.0.9 runtime/node.js/22"), "ai-sdk", ""},
		// Generic HTTP tools.
		{"curl", hdr("User-Agent", "curl/8.7.1"), "curl", "8.7.1"},
		{"python-requests", hdr("User-Agent", "python-requests/2.32.3"), "python-requests", "2.32.3"},
		{"python-httpx", hdr("User-Agent", "python-httpx/0.28.1"), "python-httpx", "0.28.1"},
		{"aiohttp", hdr("User-Agent", "Python/3.12 aiohttp/3.10.5"), "aiohttp", "3.10.5"},
		{"postman", hdr("User-Agent", "PostmanRuntime/7.43.0"), "postman", "7.43.0"},
		{"go http", hdr("User-Agent", "Go-http-client/1.1"), "go-http", ""},
		{"node fetch", hdr("User-Agent", "node"), "node", ""},
		{"undici", hdr("User-Agent", "undici"), "node", ""},

		// Negatives.
		{"no headers", http.Header{}, Unknown, ""},
		{"browser", hdr("User-Agent", chromeUA), Unknown, ""},
		{"node.js runtime substring", hdr("User-Agent", "someapp/1.0 runtime/node.js/24"), Unknown, ""},
		{"zed inside another word", hdr("User-Agent", "Optimized/1.0 Authorized/2"), Unknown, ""},
		{"other vendor stainless sdk", hdr("User-Agent", "Groq/Python 0.9.0", "X-Stainless-Lang", "python", "X-Stainless-Package-Version", "0.9.0"),
			Unknown, ""},
		{"x-app without anthropic", hdr("User-Agent", "custom/1.0", "X-App", "cli"), Unknown, ""},
		{"x-title of another app", hdr("User-Agent", "custom/1.0", "X-Title", "My Cline Fork"), Unknown, ""},
		{"curl not at start", hdr("User-Agent", "wrapper (like curl/8.0)"), Unknown, ""},
		{"electron without claude product", hdr("User-Agent", "Mozilla/5.0 MyClaude/1.0 Electron/30.0"), Unknown, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := Detect(tc.h)
			if c.ID != tc.id || c.Version != tc.ver {
				t.Fatalf("Detect() = %+v, want id %q version %q", c, tc.id, tc.ver)
			}
			if c.Name != Name(tc.id) || c.Name == "" {
				t.Errorf("name = %q, want %q", c.Name, Name(tc.id))
			}
		})
	}
}

// Every known client except the catch-all ones has a positive case above,
// and ids are unique.
func TestKnown(t *testing.T) {
	seen := map[string]bool{}
	for _, i := range Known {
		if seen[i.ID] {
			t.Errorf("duplicate id %q", i.ID)
		}
		seen[i.ID] = true
		if i.Name == "" || !slices.Contains([]string{KindAgent, KindChat, KindSDK, KindTool, KindUnknown}, i.Kind) {
			t.Errorf("bad info %+v", i)
		}
	}
	if Known[len(Known)-1].ID != Unknown || !IsKnown(Unknown) || !IsKnown("claude-code") || IsKnown("Claude-Code") || IsKnown("") {
		t.Error("IsKnown / Unknown last")
	}
	if Name("gone-client") != "gone-client" || Name("codex") != "Codex" {
		t.Error("Name fallback")
	}
}

func TestCleanVersion(t *testing.T) {
	for in, want := range map[string]string{
		"1.2.3 (x)": "1.2.3", "local": "", "": "", "0.199.4+stable.211": "0.199.4+stable.211", "v1.0": "",
		"1.0.0-beta_2;": "1.0.0-beta_2", "12345678901234567890123456789012345": "12345678901234567890123456789012",
		"1.0<script>": "1.0",
	} {
		if got := cleanVersion(in); got != want {
			t.Errorf("cleanVersion(%q) = %q, want %q", in, got, want)
		}
	}
}

func BenchmarkDetect(b *testing.B) {
	h := hdr("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 Chrome/140.0 Safari/537.36",
		"Content-Type", "application/json", "Authorization", "Bearer x", "Accept", "*/*")
	b.ReportAllocs()
	for b.Loop() {
		Detect(h)
	}
}
