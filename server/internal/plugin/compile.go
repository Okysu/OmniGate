package plugin

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"

	"github.com/evanw/esbuild/pkg/api"
)

// Package limits (phase2-api.md §1).
const (
	MaxFileBytes  = 256 << 10
	MaxTotalBytes = 1 << 20
	MaxFiles      = 64
)

// GlobalName is the IIFE global holding the plugin module.
const GlobalName = "__og_plugin"

var pathRe = regexp.MustCompile(`^[A-Za-z0-9._-]+(/[A-Za-z0-9._-]+)*$`)

// sdkModule is the runtime shim for "@omnigate/plugin-sdk".
const sdkModule = `export function definePlugin(p) { return p; }`

// CheckFiles validates package structure.
func CheckFiles(files map[string]string) []Diagnostic {
	var ds []Diagnostic
	if len(files) > MaxFiles {
		ds = append(ds, Diagnostic{Severity: "error", Message: fmt.Sprintf("最多 %d 个文件", MaxFiles)})
	}
	total := 0
	for p, c := range files {
		total += len(c)
		if !pathRe.MatchString(p) || strings.Contains(p, "..") {
			ds = append(ds, Diagnostic{File: p, Severity: "error", Message: "文件路径不合法"})
		}
		if len(c) > MaxFileBytes {
			ds = append(ds, Diagnostic{File: p, Severity: "error", Message: "单个文件不能超过 256 KiB"})
		}
	}
	if total > MaxTotalBytes {
		ds = append(ds, Diagnostic{Severity: "error", Message: "插件总大小不能超过 1 MiB"})
	}
	if _, ok := files["manifest.json"]; !ok {
		ds = append(ds, Diagnostic{File: "manifest.json", Severity: "error", Message: "缺少 manifest.json"})
	}
	return ds
}

// ContentHash is a stable digest of the package files.
func ContentHash(files map[string]string) string {
	keys := make([]string, 0, len(files))
	for k := range files {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	h := sha256.New()
	for _, k := range keys {
		fmt.Fprintf(h, "%d:%s\n%d:", len(k), k, len(files[k]))
		h.Write([]byte(files[k]))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Compile bundles the entry module into a single ES2017 IIFE. Only relative
// imports between package files and "@omnigate/plugin-sdk" are allowed.
func Compile(files map[string]string, entry string) (string, []Diagnostic) {
	sdk := sdkModule
	res := api.Build(api.BuildOptions{
		EntryPoints: []string{entry}, Bundle: true, Write: false,
		Format: api.FormatIIFE, GlobalName: GlobalName, Target: api.ES2017, Platform: api.PlatformNeutral,
		LogLevel: api.LogLevelSilent, Charset: api.CharsetUTF8, LegalComments: api.LegalCommentsNone,
		Plugins: []api.Plugin{{Name: "omnigate-vfs", Setup: func(b api.PluginBuild) {
			b.OnResolve(api.OnResolveOptions{Filter: ".*"}, func(a api.OnResolveArgs) (api.OnResolveResult, error) {
				if a.Path == "@omnigate/plugin-sdk" {
					return api.OnResolveResult{Path: "@omnigate/plugin-sdk", Namespace: "sdk"}, nil
				}
				if a.Kind == api.ResolveEntryPoint {
					return api.OnResolveResult{Path: a.Path, Namespace: "vfs"}, nil
				}
				if !strings.HasPrefix(a.Path, "./") && !strings.HasPrefix(a.Path, "../") {
					return api.OnResolveResult{}, fmt.Errorf("只允许相对路径导入或 @omnigate/plugin-sdk，不允许导入 %q", a.Path)
				}
				p := path.Clean(path.Join(path.Dir(a.Importer), a.Path))
				for _, cand := range []string{p, p + ".ts", p + ".js", p + "/index.ts", p + "/index.js"} {
					if _, ok := files[cand]; ok {
						return api.OnResolveResult{Path: cand, Namespace: "vfs"}, nil
					}
				}
				return api.OnResolveResult{}, fmt.Errorf("找不到模块 %q", a.Path)
			})
			b.OnLoad(api.OnLoadOptions{Filter: ".*", Namespace: "sdk"}, func(api.OnLoadArgs) (api.OnLoadResult, error) {
				return api.OnLoadResult{Contents: &sdk, Loader: api.LoaderJS}, nil
			})
			b.OnLoad(api.OnLoadOptions{Filter: ".*", Namespace: "vfs"}, func(a api.OnLoadArgs) (api.OnLoadResult, error) {
				c, ok := files[a.Path]
				if !ok {
					return api.OnLoadResult{}, fmt.Errorf("找不到文件 %q", a.Path)
				}
				loader := api.LoaderTS
				if strings.HasSuffix(a.Path, ".js") {
					loader = api.LoaderJS
				} else if strings.HasSuffix(a.Path, ".json") {
					loader = api.LoaderJSON
				}
				return api.OnLoadResult{Contents: &c, Loader: loader}, nil
			})
		}}},
	})
	var ds []Diagnostic
	for _, m := range res.Errors {
		ds = append(ds, toDiag(m, "error"))
	}
	for _, m := range res.Warnings {
		ds = append(ds, toDiag(m, "warning"))
	}
	if len(res.Errors) > 0 || len(res.OutputFiles) == 0 {
		return "", ds
	}
	return string(res.OutputFiles[0].Contents), ds
}

func toDiag(m api.Message, sev string) Diagnostic {
	d := Diagnostic{Severity: sev, Message: m.Text}
	if m.Location != nil {
		d.File, d.Line, d.Column = m.Location.File, m.Location.Line, m.Location.Column+1
		d.File = strings.TrimPrefix(strings.TrimPrefix(d.File, "vfs:"), "sdk:")
	}
	return d
}

// ---- static risk scan (advisory only; not a security guarantee) ----

type RiskFinding struct {
	Rule    string `json:"rule"`
	File    string `json:"file"`
	Line    int    `json:"line"`
	Message string `json:"message"`
}

var riskRules = []struct {
	rule string
	re   *regexp.Regexp
	msg  string
}{
	{"dynamic-code", regexp.MustCompile(`\beval\s*\(|\bnew\s+Function\s*\(|\bFunction\s*\(\s*['"]`), "动态执行代码（eval / Function）"},
	{"constructor-escape", regexp.MustCompile(`constructor\s*\.\s*constructor|\[\s*['"]constructor['"]\s*\]`), "访问构造器链，常用于沙盒逃逸尝试"},
	{"proto-pollution", regexp.MustCompile(`__proto__|Object\.setPrototypeOf|defineProperty\s*\(\s*Object\.prototype`), "修改原型链"},
	{"busy-loop", regexp.MustCompile(`while\s*\(\s*(true|1)\s*\)|for\s*\(\s*;\s*;\s*\)`), "无条件循环，可能导致超时"},
	{"obfuscation", regexp.MustCompile(`[A-Za-z0-9+/=]{800,}|(\\x[0-9a-fA-F]{2}){40,}`), "疑似混淆或内嵌大段编码数据"},
	{"global-tamper", regexp.MustCompile(`\bglobalThis\s*\[|\bog\s*=\s*|delete\s+og\b`), "修改全局对象或宿主 API"},
}

var urlLiteralRe = regexp.MustCompile(`https?://([a-zA-Z0-9.-]+)`)

// ScanRisks reports suspicious patterns and literal URLs to hosts that are not
// covered by the network permission.
func ScanRisks(files map[string]string, network []string) []RiskFinding {
	out := []RiskFinding{}
	paths := make([]string, 0, len(files))
	for p := range files {
		if strings.HasSuffix(p, ".ts") || strings.HasSuffix(p, ".js") {
			paths = append(paths, p)
		}
	}
	sort.Strings(paths)
	for _, p := range paths {
		for i, line := range strings.Split(files[p], "\n") {
			for _, r := range riskRules {
				if r.re.MatchString(line) {
					out = append(out, RiskFinding{Rule: r.rule, File: p, Line: i + 1, Message: r.msg})
				}
			}
			for _, m := range urlLiteralRe.FindAllStringSubmatch(line, -1) {
				if !HostAllowed(strings.ToLower(m[1]), network, "") {
					out = append(out, RiskFinding{Rule: "undeclared-host", File: p, Line: i + 1,
						Message: fmt.Sprintf("代码中出现未在 permissions.network 声明的地址 %s（运行时会被拒绝）", m[1])})
				}
			}
		}
	}
	return out
}

// HostAllowed matches host against network permissions ("$baseUrl" resolves
// to baseHost; "*.example.com" matches subdomains, not the apex).
func HostAllowed(host string, network []string, baseHost string) bool {
	host = strings.ToLower(host)
	for _, n := range network {
		switch {
		case n == "$baseUrl":
			if baseHost != "" && host == strings.ToLower(baseHost) {
				return true
			}
		case strings.HasPrefix(n, "*."):
			if strings.HasSuffix(host, n[1:]) && host != n[2:] {
				return true
			}
		case host == n:
			return true
		}
	}
	return false
}
