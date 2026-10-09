package notify

import (
	"context"
	"strings"
	"testing"
	"time"

	"omnigate/internal/notify/notifytest"
	"omnigate/internal/settings"
)

func TestMailerModes(t *testing.T) {
	for _, mode := range []string{"none", "starttls", "tls"} {
		t.Run(mode, func(t *testing.T) {
			srv := notifytest.Start(t, mode)
			cfg := settings.SMTPConfig{Host: srv.Host, Port: srv.Port, Security: mode, Username: "mailer", Password: "pa55word",
				From: "OmniGate <noreply@example.com>"}
			h, txt := render(mailView{Site: "OmniGate", Heading: "你好", Items: []mailItem{{Title: "t", Body: "第一行\n第二行", CreatedAt: time.Now()}}})
			err := sendMail(context.Background(), cfg, false, srv.ClientTLS, Mail{To: "a@example.com", Subject: "测试 subject", HTML: h, Text: txt})
			if err != nil {
				t.Fatal(err)
			}
			m := srv.Wait(1, time.Second)
			if len(m) != 1 || m[0].Subject != "测试 subject" || m[0].From != "noreply@example.com" || m[0].To[0] != "a@example.com" ||
				m[0].Username != "mailer" || m[0].Password != "pa55word" || !strings.Contains(m[0].Text, "第二行") || !strings.Contains(m[0].HTML, "<p") {
				t.Fatalf("message = %+v", m)
			}
		})
	}
}

func TestMailerErrors(t *testing.T) {
	srv := notifytest.Start(t, "none")
	cfg := settings.SMTPConfig{Host: srv.Host, Port: srv.Port, Security: "none", Username: "u", Password: "Sup3rS3cret!", From: "x@example.com"}
	srv.RejectAuth.Store(true)
	err := sendMail(context.Background(), cfg, false, nil, Mail{To: "a@example.com", Subject: "s"})
	if err == nil || strings.Contains(err.Error(), "Sup3rS3cret!") || !strings.Contains(err.Error(), "身份验证失败") {
		t.Fatalf("auth error = %v", err)
	}
	if err := sendMail(context.Background(), cfg, true, nil, Mail{To: "a@example.com"}); err == nil || !strings.Contains(err.Error(), "生产环境") {
		t.Fatalf("production none = %v", err)
	}
	cfg.Security = "starttls" // the plain fake does not offer STARTTLS
	if err := sendMail(context.Background(), cfg, false, nil, Mail{To: "a@example.com"}); err == nil || !strings.Contains(err.Error(), "STARTTLS") {
		t.Fatalf("missing starttls = %v", err)
	}
	if err := sendMail(context.Background(), settings.SMTPConfig{}, false, nil, Mail{To: "a@example.com"}); err != ErrSMTPNotConfigured {
		t.Fatalf("unconfigured = %v", err)
	}
}
