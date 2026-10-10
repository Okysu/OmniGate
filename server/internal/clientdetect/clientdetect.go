// Package clientdetect recognises the client application that sent a gateway
// request (Claude Code, Codex, Cherry Studio, the official SDKs, curl …) from
// its distinctive request headers and User-Agent (docs/contracts/phase13-api.md
// §1). Only the resulting stable id and a parsed version are kept; the raw
// User-Agent and other headers are never stored.
//
// Detect runs on every data-plane request: it is pure, allocation-light and
// checks an ordered rule list, most specific first (client-specific headers
// and User-Agent products before the SDKs those clients are built on, SDKs
// before generic HTTP tools).
package clientdetect

import (
	"net/http"
	"strings"
)

// Client is the detected client of a request.
type Client struct {
	// ID is a stable identifier (Known); Unknown when no rule matched.
	ID string `json:"id"`
	// Name is the display name.
	Name string `json:"name"`
	// Version is the client version when it is cheaply parseable ("" = none).
	Version string `json:"version,omitempty"`
}

// Kinds group clients for display.
const (
	KindAgent   = "agent"   // coding agents and IDE assistants
	KindChat    = "chat"    // chat applications
	KindSDK     = "sdk"     // official SDKs and client libraries
	KindTool    = "tool"    // generic HTTP tools and runtimes
	KindUnknown = "unknown" // not recognised
)

// Info describes one known client.
type Info struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Kind string `json:"kind"`
}

// Unknown is the id of requests no rule recognised.
const Unknown = "unknown"

// maxVersionLen bounds a parsed version.
const maxVersionLen = 32

// req is what the rules see of a request: the headers and the User-Agent in
// original and lower case (computed once; versions are parsed from the lower
// case form).
type req struct {
	h       http.Header
	ua, lua string
}

// rule recognises one client; version (optional) parses its version.
type rule struct {
	Info
	match   func(r *req) bool
	version func(r *req) string
}

// rules in match order: the first matching rule wins.
var rules = []rule{
	// Coding agents. Claude Code is built on the Anthropic SDK and many tools
	// on the OpenAI SDK, so these come before the SDK rules.
	{Info{"claude-code", "Claude Code", KindAgent}, func(r *req) bool {
		return strings.HasPrefix(r.lua, "claude-cli/") || r.h.Get("X-Claude-Code-Session-Id") != "" ||
			strings.EqualFold(r.h.Get("X-App"), "cli") && r.h.Get("Anthropic-Version") != ""
	}, uaVersion("claude-cli")},
	{Info{"codex", "Codex", KindAgent}, func(r *req) bool {
		// Originator: codex_cli_rs, codex_vscode, codex_exec …; Codex Desktop
		// and the CLI also name themselves first in the User-Agent.
		return strings.HasPrefix(strings.ToLower(r.h.Get("Originator")), "codex") || strings.HasPrefix(r.lua, "codex") ||
			hasHeaderPrefix(r.h, "X-Codex-") || r.h.Get("X-Openai-Subagent") != ""
	}, func(r *req) string {
		if strings.HasPrefix(r.lua, "codex") {
			return leadingVersion(r.lua)
		}
		return ""
	}},
	{Info{"gemini-cli", "Gemini CLI", KindAgent}, uaPrefix("geminicli/"), uaVersion("geminicli")},
	{Info{"opencode", "opencode", KindAgent}, uaPrefix("opencode/"), uaVersion("opencode")},
	{Info{"github-copilot", "GitHub Copilot", KindAgent}, func(r *req) bool {
		return strings.Contains(r.lua, "githubcopilot") || r.h.Get("Copilot-Integration-Id") != "" || r.h.Get("X-Onbehalf-Extension-Id") != ""
	}, uaVersion("githubcopilotchat")},
	// Kilo Code is a fork of Roo Code, which is a fork of Cline: each sends
	// its own X-Title (OpenRouter app attribution) and User-Agent product.
	{Info{"kilo-code", "Kilo Code", KindAgent}, func(r *req) bool { return strings.HasPrefix(r.lua, "kilo-code/") || title(r, "kilo code") },
		uaVersion("kilo-code")},
	{Info{"roo-code", "Roo Code", KindAgent}, func(r *req) bool { return strings.HasPrefix(r.lua, "roocode/") || title(r, "roo code") },
		uaVersion("roocode")},
	{Info{"cline", "Cline", KindAgent}, func(r *req) bool { return strings.HasPrefix(r.lua, "cline/") || title(r, "cline") },
		uaVersion("cline")},
	{Info{"continue", "Continue", KindAgent}, uaPrefix("continue/"), uaVersion("continue")},
	{Info{"aider", "aider", KindAgent}, func(r *req) bool { return strings.HasPrefix(r.lua, "aider/") || title(r, "aider") },
		uaVersion("aider")},
	{Info{"cursor", "Cursor", KindAgent}, product("cursor"), productVersion("cursor")},
	{Info{"zed", "Zed", KindAgent}, product("zed"), productVersion("zed")},

	// Chat applications (most are Electron or mobile apps naming themselves
	// in the User-Agent).
	{Info{"cherry-studio", "Cherry Studio", KindChat}, func(r *req) bool {
		return strings.Contains(r.lua, "cherrystudio") || title(r, "cherry studio")
	}, uaVersion("cherrystudio")},
	{Info{"chatbox", "Chatbox", KindChat}, uaToken("chatboxapp"), uaVersion("chatboxapp")},
	{Info{"lobechat", "LobeChat", KindChat}, func(r *req) bool {
		t := strings.ToLower(strings.TrimSpace(r.h.Get("X-Title")))
		return strings.Contains(r.lua, "lobechat") || t == "lobechat" || t == "lobe chat" || t == "lobehub"
	}, uaVersion("lobechat")},
	{Info{"nextchat", "NextChat", KindChat}, func(r *req) bool { return product("nextchat")(r) || title(r, "nextchat") },
		productVersion("nextchat")},
	{Info{"rikkahub", "RikkaHub", KindChat}, uaToken("rikkahub"), uaVersion("rikkahub")},
	{Info{"kelivo", "Kelivo", KindChat}, uaToken("kelivo"), uaVersion("kelivo")},
	// The Claude desktop app (Electron); Claude Code inside it sends
	// claude-cli and matched above.
	{Info{"claude-desktop", "Claude Desktop", KindChat}, func(r *req) bool {
		return strings.Contains(r.ua, " Claude/") && product("electron")(r)
	}, productVersion("claude")},

	// Official SDKs. The Stainless-generated OpenAI and Anthropic SDKs send
	// X-Stainless-Lang / X-Stainless-Package-Version and a User-Agent such as
	// "OpenAI/Python 1.51.0" or "Anthropic/JS 0.32.1" (browser builds keep the
	// browser's User-Agent: anthropic-version then tells the two apart).
	{Info{"anthropic-sdk-python", "Anthropic SDK (Python)", KindSDK}, stainless(true, "python"), sdkVersion},
	{Info{"anthropic-sdk-js", "Anthropic SDK (JS)", KindSDK}, stainless(true, "js"), sdkVersion},
	{Info{"anthropic-sdk", "Anthropic SDK", KindSDK}, stainless(true, ""), sdkVersion},
	{Info{"openai-sdk-python", "OpenAI SDK (Python)", KindSDK}, stainless(false, "python"), sdkVersion},
	{Info{"openai-sdk-js", "OpenAI SDK (JS)", KindSDK}, stainless(false, "js"), sdkVersion},
	{Info{"openai-sdk", "OpenAI SDK", KindSDK}, stainless(false, ""), sdkVersion},
	// Vercel AI SDK ("ai-sdk/openai/2.0.1", "ai-sdk/provider-utils/3.0.0
	// runtime/node.js/22"): many agents built on it name themselves first and
	// matched above.
	{Info{"ai-sdk", "Vercel AI SDK", KindSDK}, uaToken("ai-sdk/"), nil},

	// Generic HTTP tools and runtimes.
	{Info{"curl", "curl", KindTool}, uaPrefix("curl/"), uaVersion("curl")},
	{Info{"python-requests", "python-requests", KindTool}, uaPrefix("python-requests/"), uaVersion("python-requests")},
	{Info{"python-httpx", "python-httpx", KindTool}, uaPrefix("python-httpx/"), uaVersion("python-httpx")},
	{Info{"aiohttp", "aiohttp", KindTool}, product("aiohttp"), productVersion("aiohttp")},
	{Info{"postman", "Postman", KindTool}, uaPrefix("postmanruntime/"), uaVersion("postmanruntime")},
	// Go's default client ("Go-http-client/1.1": the number is the HTTP
	// version, not a client version).
	{Info{"go-http", "Go net/http", KindTool}, uaPrefix("go-http-client/"), nil},
	// Node's built-in fetch sends exactly "node" (older releases "undici").
	{Info{"node", "Node.js fetch", KindTool}, func(r *req) bool { return r.lua == "node" || r.lua == "undici" }, nil},
}

// unknownInfo is the entry of unrecognised requests.
var unknownInfo = Info{Unknown, "未知", KindUnknown}

// Known lists every client id in display order, ending with Unknown
// (GET /api/clients; request log filter and affinity client_include values).
var Known = func() []Info {
	out := make([]Info, 0, len(rules)+1)
	for _, r := range rules {
		out = append(out, r.Info)
	}
	return append(out, unknownInfo)
}()

var byID = func() map[string]Info {
	m := make(map[string]Info, len(Known))
	for _, i := range Known {
		m[i.ID] = i
	}
	return m
}()

// IsKnown reports whether id is a client id (including Unknown).
func IsKnown(id string) bool {
	_, ok := byID[id]
	return ok
}

// Name returns the display name of id (the id itself when it is not known,
// e.g. a client a later release no longer recognises).
func Name(id string) string {
	if i, ok := byID[id]; ok {
		return i.Name
	}
	return id
}

// Detect recognises the client that sent a request with headers h. It never
// fails: unrecognised requests are Unknown.
func Detect(h http.Header) Client {
	ua := strings.TrimSpace(h.Get("User-Agent"))
	r := &req{h: h, ua: ua, lua: strings.ToLower(ua)}
	for i := range rules {
		rl := &rules[i]
		if rl.match(r) {
			c := Client{ID: rl.ID, Name: rl.Name}
			if rl.version != nil {
				c.Version = rl.version(r)
			}
			return c
		}
	}
	return Client{ID: Unknown, Name: unknownInfo.Name}
}

// uaPrefix matches a lower-case User-Agent prefix.
func uaPrefix(p string) func(r *req) bool {
	return func(r *req) bool { return strings.HasPrefix(r.lua, p) }
}

// uaToken matches a lower-case substring anywhere in the User-Agent (for
// distinctive names only).
func uaToken(t string) func(r *req) bool {
	return func(r *req) bool { return strings.Contains(r.lua, t) }
}

// productAt returns the index just after the User-Agent product name (lower
// case) followed by "/", at the start or after a space; -1 when absent. Short
// names such as "zed" then never match inside other words ("optimized/1").
func productAt(lua, name string) int {
	if strings.HasPrefix(lua, name+"/") {
		return len(name)
	}
	if i := strings.Index(lua, " "+name+"/"); i >= 0 {
		return i + 1 + len(name)
	}
	return -1
}

// product matches a User-Agent product (productAt).
func product(name string) func(r *req) bool {
	return func(r *req) bool { return productAt(r.lua, name) >= 0 }
}

// productVersion parses the version of a User-Agent product.
func productVersion(name string) func(r *req) string {
	return func(r *req) string { return versionAfter(r.lua, productAt(r.lua, name)) }
}

// title matches the X-Title header (OpenRouter app attribution, also sent to
// other OpenAI-compatible endpoints), trimmed and case-insensitively.
func title(r *req, name string) bool {
	return strings.EqualFold(strings.TrimSpace(r.h.Get("X-Title")), name)
}

// stainless matches a Stainless-generated SDK: X-Stainless-Lang present
// (lang "" = any language other than python and js), told apart by the
// User-Agent product, or by anthropic-version when the User-Agent is not the
// SDK's own (browser builds). Other vendors' Stainless SDKs ("Groq/Python
// 0.9.0") are not claimed.
func stainless(anthropic bool, lang string) func(r *req) bool {
	return func(r *req) bool {
		l := strings.ToLower(strings.TrimSpace(r.h.Get("X-Stainless-Lang")))
		if l == "" {
			return false
		}
		if lang == "" {
			if l == "python" || l == "js" {
				return false
			}
		} else if l != lang {
			return false
		}
		switch {
		case strings.HasPrefix(r.lua, "anthropic/"):
			return anthropic
		case strings.HasPrefix(r.lua, "openai/"):
			return !anthropic
		}
		if i := strings.IndexByte(r.lua, '/'); i > 0 && strings.HasPrefix(r.lua[i+1:], l+" ") {
			return false
		}
		return anthropic == (r.h.Get("Anthropic-Version") != "")
	}
}

func sdkVersion(r *req) string { return cleanVersion(r.h.Get("X-Stainless-Package-Version")) }

// uaVersion returns a parser of the version following a distinctive
// lower-case token in the User-Agent ("cherrystudio" in "… CherryStudio/1.7.8
// …"). Versions are parsed from the lower-case User-Agent.
func uaVersion(token string) func(r *req) string {
	return func(r *req) string {
		i := strings.Index(r.lua, token)
		if i < 0 {
			return ""
		}
		return versionAfter(r.lua, i+len(token))
	}
}

// leadingVersion returns the version of the first User-Agent product, whose
// name may contain spaces ("codex_cli_rs/0.46.0 (mac os …)" → "0.46.0",
// "codex desktop/0.142.2 …" → "0.142.2").
func leadingVersion(ua string) string {
	i := strings.IndexByte(ua, '/')
	if i < 0 || strings.ContainsAny(ua[:i], "(;") {
		return ""
	}
	return cleanVersion(ua[i+1:])
}

// versionAfter parses the version of the product whose name continues at
// ua[i:]: the rest of the name up to "/" ("rikkahub-android/2.2.6",
// "xyz.chatboxapp.app/1.21.1") and then the version. "" when there is no
// "/" before a space or the version does not start with a digit.
func versionAfter(ua string, i int) string {
	if i < 0 || i > len(ua) {
		return ""
	}
	rest := ua[i:]
	j := strings.IndexAny(rest, "/ ;()")
	if j < 0 || rest[j] != '/' {
		return ""
	}
	return cleanVersion(rest[j+1:])
}

// cleanVersion keeps the leading version characters (digits, letters, ".",
// "-", "+", "_") of s, at most maxVersionLen; "" unless it starts with a digit
// (so "opencode/local" has no version).
func cleanVersion(s string) string {
	s = strings.TrimSpace(s)
	n := 0
	for n < len(s) && n < maxVersionLen {
		c := s[n]
		if c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '.' || c == '-' || c == '+' || c == '_' {
			n++
			continue
		}
		break
	}
	if n == 0 || s[0] < '0' || s[0] > '9' {
		return ""
	}
	return s[:n]
}

// hasHeaderPrefix reports whether a header name starts with prefix (names
// are canonical in http.Header).
func hasHeaderPrefix(h http.Header, prefix string) bool {
	for k := range h {
		if strings.HasPrefix(k, prefix) {
			return true
		}
	}
	return false
}
