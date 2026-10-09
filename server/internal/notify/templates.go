package notify

import (
	"bytes"
	"html/template"
	"strings"
	texttemplate "text/template"
	"time"
)

// mailItem is one notification shown in an email.
type mailItem struct {
	Title     string
	Body      string
	Link      string // absolute URL ("" = none)
	CreatedAt time.Time
}

// mailView is the data of the email templates.
type mailView struct {
	Site        string
	Heading     string
	Intro       string
	Items       []mailItem
	SettingsURL string
	Unsubscribe string
	UnsubLabel  string
	Code        string
	Loc         *time.Location
}

func (v mailView) When(t time.Time) string {
	loc := v.Loc
	if loc == nil {
		loc = time.UTC
	}
	return t.In(loc).Format("2006-01-02 15:04 MST")
}

func paragraphs(s string) []string { return strings.Split(strings.TrimSpace(s), "\n") }

var funcs = map[string]any{"paragraphs": paragraphs}

var htmlTmpl = template.Must(template.New("mail").Funcs(funcs).Parse(`<!doctype html>
<html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width"><title>{{.Heading}}</title></head>
<body style="margin:0;padding:0;background:#f4f5f7;font-family:-apple-system,'PingFang SC','Microsoft YaHei',Helvetica,Arial,sans-serif;color:#1f2328">
<table role="presentation" width="100%" cellpadding="0" cellspacing="0" style="background:#f4f5f7;padding:24px 0"><tr><td align="center">
<table role="presentation" width="600" cellpadding="0" cellspacing="0" style="max-width:600px;width:100%;background:#ffffff;border-radius:8px;overflow:hidden">
<tr><td style="padding:20px 28px;background:#111827;color:#ffffff;font-size:16px;font-weight:600">{{.Site}}</td></tr>
<tr><td style="padding:24px 28px">
<h1 style="margin:0 0 12px;font-size:20px;line-height:1.4">{{.Heading}}</h1>
{{if .Intro}}<p style="margin:0 0 16px;font-size:14px;line-height:1.7;color:#57606a">{{.Intro}}</p>{{end}}
{{if .Code}}<p style="margin:16px 0;font-size:32px;letter-spacing:8px;font-weight:700">{{.Code}}</p>{{end}}
{{range .Items}}<div style="margin:0 0 16px;padding:12px 16px;border:1px solid #e5e7eb;border-radius:6px">
{{if gt (len $.Items) 1}}<div style="font-size:15px;font-weight:600;margin-bottom:6px">{{.Title}}</div>{{end}}
{{range paragraphs .Body}}<p style="margin:0 0 6px;font-size:14px;line-height:1.7">{{.}}</p>{{end}}
<div style="font-size:12px;color:#8c959f;margin-top:6px">{{$.When .CreatedAt}}{{if .Link}} · <a href="{{.Link}}" style="color:#0969da">查看详情</a>{{end}}</div>
</div>{{end}}
</td></tr>
<tr><td style="padding:16px 28px;border-top:1px solid #e5e7eb;font-size:12px;line-height:1.7;color:#8c959f">
这是一封由 {{.Site}} 自动发送的邮件，请勿直接回复。
{{if .SettingsURL}}<a href="{{.SettingsURL}}" style="color:#0969da">管理通知设置</a>{{end}}
{{if .Unsubscribe}} · <a href="{{.Unsubscribe}}" style="color:#0969da">{{.UnsubLabel}}</a>{{end}}
</td></tr></table></td></tr></table></body></html>
`))

var textTmpl = texttemplate.Must(texttemplate.New("mail").Parse(`{{.Heading}}
{{if .Intro}}
{{.Intro}}
{{end}}{{if .Code}}
验证码：{{.Code}}
{{end}}{{range .Items}}
{{if gt (len $.Items) 1}}■ {{.Title}}
{{end}}{{.Body}}
{{$.When .CreatedAt}}{{if .Link}}
查看详情：{{.Link}}{{end}}
{{end}}
--
这是一封由 {{.Site}} 自动发送的邮件，请勿直接回复。
{{if .SettingsURL}}管理通知设置：{{.SettingsURL}}
{{end}}{{if .Unsubscribe}}{{.UnsubLabel}}：{{.Unsubscribe}}
{{end}}`))

// render produces the HTML and text parts of v.
func render(v mailView) (htmlBody, textBody string) {
	var h, t bytes.Buffer
	if err := htmlTmpl.Execute(&h, v); err != nil {
		h.Reset()
		h.WriteString(template.HTMLEscapeString(v.Heading))
	}
	if err := textTmpl.Execute(&t, v); err != nil {
		t.Reset()
		t.WriteString(v.Heading)
	}
	return h.String(), t.String()
}
