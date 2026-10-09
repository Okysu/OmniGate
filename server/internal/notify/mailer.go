package notify

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/mail"
	"net/smtp"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"omnigate/internal/settings"
)

// Mail is one outgoing email (HTML + plain text).
type Mail struct {
	To      string
	Subject string
	Text    string
	HTML    string
	// Unsubscribe is the one-click unsubscribe URL (List-Unsubscribe).
	Unsubscribe string
}

// ErrSMTPNotConfigured is returned when no SMTP server is configured.
var ErrSMTPNotConfigured = errors.New("SMTP 未配置")

const (
	smtpDialTimeout = 10 * time.Second
	smtpTimeout     = 30 * time.Second
)

// sendMail delivers m through the SMTP server in cfg. Errors never contain
// the password.
func sendMail(ctx context.Context, cfg settings.SMTPConfig, production bool, base *tls.Config, m Mail) error {
	if !cfg.Configured() {
		return ErrSMTPNotConfigured
	}
	err := deliver(ctx, cfg, production, base, m)
	if err != nil && cfg.Password != "" && strings.Contains(err.Error(), cfg.Password) {
		return errors.New(strings.ReplaceAll(err.Error(), cfg.Password, "***"))
	}
	return err
}

func deliver(ctx context.Context, cfg settings.SMTPConfig, production bool, base *tls.Config, m Mail) error {
	from, err := mail.ParseAddress(cfg.From)
	if err != nil {
		return fmt.Errorf("发件人地址无效：%v", err)
	}
	to, err := mail.ParseAddress(m.To)
	if err != nil {
		return fmt.Errorf("收件人地址无效：%v", err)
	}
	if cfg.Security == "none" && production {
		return errors.New("生产环境不允许不加密的 SMTP 连接（security = none）")
	}
	port := cfg.Port
	if port == 0 {
		port = 587
	}
	addr := net.JoinHostPort(cfg.Host, strconv.Itoa(port))
	dctx, cancel := context.WithTimeout(ctx, smtpDialTimeout)
	defer cancel()
	conn, err := (&net.Dialer{}).DialContext(dctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("连接 SMTP 服务器失败：%w", err)
	}
	defer conn.Close()
	deadline := time.Now().Add(smtpTimeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = conn.SetDeadline(deadline)
	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS12}
	if base != nil {
		tlsCfg = base.Clone()
	}
	tlsCfg.ServerName = cfg.Host
	if cfg.Security == "tls" {
		tc := tls.Client(conn, tlsCfg)
		if err := tc.HandshakeContext(ctx); err != nil {
			return fmt.Errorf("TLS 握手失败：%w", err)
		}
		conn = tc
	}
	c, err := smtp.NewClient(conn, cfg.Host)
	if err != nil {
		return fmt.Errorf("SMTP 握手失败：%w", err)
	}
	defer c.Close()
	if err := c.Hello("omnigate.local"); err != nil {
		return fmt.Errorf("SMTP EHLO 失败：%w", err)
	}
	if cfg.Security == "" || cfg.Security == "starttls" {
		if ok, _ := c.Extension("STARTTLS"); !ok {
			return errors.New("SMTP 服务器不支持 STARTTLS（可改用 tls 或检查端口）")
		}
		if err := c.StartTLS(tlsCfg); err != nil {
			return fmt.Errorf("STARTTLS 失败：%w", err)
		}
	}
	if cfg.Username != "" {
		if ok, _ := c.Extension("AUTH"); !ok {
			return errors.New("SMTP 服务器不支持身份验证（AUTH）")
		}
		if err := c.Auth(smtp.PlainAuth("", cfg.Username, cfg.Password, cfg.Host)); err != nil {
			return fmt.Errorf("SMTP 身份验证失败：%w", err)
		}
	}
	if err := c.Mail(from.Address); err != nil {
		return fmt.Errorf("SMTP MAIL FROM 被拒绝：%w", err)
	}
	if err := c.Rcpt(to.Address); err != nil {
		return fmt.Errorf("SMTP 收件人被拒绝：%w", err)
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("SMTP DATA 失败：%w", err)
	}
	if _, err := w.Write(buildMessage(from, to, m)); err != nil {
		return fmt.Errorf("写入邮件内容失败：%w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("SMTP 服务器拒绝邮件：%w", err)
	}
	_ = c.Quit()
	return nil
}

// buildMessage renders an RFC 5322 multipart/alternative message.
func buildMessage(from, to *mail.Address, m Mail) []byte {
	var b bytes.Buffer
	boundary := "og-" + strings.ReplaceAll(uuid.NewString(), "-", "")
	domain := "omnigate.local"
	if at := strings.LastIndexByte(from.Address, '@'); at >= 0 {
		domain = from.Address[at+1:]
	}
	h := func(k, v string) { b.WriteString(k + ": " + v + "\r\n") }
	h("From", from.String())
	h("To", to.String())
	h("Subject", mime.BEncoding.Encode("UTF-8", m.Subject))
	h("Date", time.Now().Format(time.RFC1123Z))
	h("Message-ID", "<"+uuid.NewString()+"@"+domain+">")
	h("MIME-Version", "1.0")
	h("Auto-Submitted", "auto-generated")
	if m.Unsubscribe != "" {
		h("List-Unsubscribe", "<"+m.Unsubscribe+">")
	}
	h("Content-Type", `multipart/alternative; boundary="`+boundary+`"`)
	b.WriteString("\r\n")
	for _, part := range []struct{ ct, body string }{{"text/plain; charset=UTF-8", m.Text}, {"text/html; charset=UTF-8", m.HTML}} {
		b.WriteString("--" + boundary + "\r\n")
		h("Content-Type", part.ct)
		h("Content-Transfer-Encoding", "base64")
		b.WriteString("\r\n")
		enc := base64.StdEncoding.EncodeToString([]byte(part.body))
		for len(enc) > 76 {
			b.WriteString(enc[:76] + "\r\n")
			enc = enc[76:]
		}
		b.WriteString(enc + "\r\n")
	}
	b.WriteString("--" + boundary + "--\r\n")
	return b.Bytes()
}
