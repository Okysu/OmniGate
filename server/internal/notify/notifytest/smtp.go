// Package notifytest provides an in-process fake SMTP server for tests
// (plain, STARTTLS or implicit TLS, AUTH PLAIN). It never touches the
// network beyond 127.0.0.1.
package notifytest

import (
	"bufio"
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"io"
	"math/big"
	"mime"
	"mime/multipart"
	"net"
	"net/mail"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Message is a received email.
type Message struct {
	From    string
	To      []string
	Raw     string
	Subject string
	Text    string
	HTML    string
	Header  mail.Header
	// Username and Password of the AUTH PLAIN exchange ("" without auth).
	Username, Password string
}

// Server is a fake SMTP server.
type Server struct {
	Host string
	Port int
	// Mode is "none" (plain), "starttls" or "tls" (implicit).
	Mode string
	// ClientTLS trusts the server certificate (pass it to the mailer).
	ClientTLS *tls.Config
	// FailNext makes the next n DATA commands fail with 451.
	FailNext atomic.Int32
	// RejectAuth answers AUTH with 535.
	RejectAuth atomic.Bool

	ln      net.Listener
	srvTLS  *tls.Config
	mu      sync.Mutex
	msgs    []Message
	arrived chan struct{}
}

// Start starts a server in mode ("none", "starttls" or "tls"), stopped when t ends.
func Start(t testing.TB, mode string) *Server {
	t.Helper()
	cert, pool := selfSigned(t)
	s := &Server{Mode: mode, srvTLS: &tls.Config{Certificates: []tls.Certificate{cert}},
		ClientTLS: &tls.Config{RootCAs: pool}, arrived: make(chan struct{}, 1000)}
	var err error
	if mode == "tls" {
		s.ln, err = tls.Listen("tcp", "127.0.0.1:0", s.srvTLS)
	} else {
		s.ln, err = net.Listen("tcp", "127.0.0.1:0")
	}
	if err != nil {
		t.Fatal(err)
	}
	addr := s.ln.Addr().(*net.TCPAddr)
	s.Host, s.Port = "127.0.0.1", addr.Port
	go s.serve()
	t.Cleanup(func() { _ = s.ln.Close() })
	return s
}

// Messages returns the received messages.
func (s *Server) Messages() []Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Message(nil), s.msgs...)
}

// Reset forgets the received messages.
func (s *Server) Reset() {
	s.mu.Lock()
	s.msgs = nil
	s.mu.Unlock()
}

// Wait waits until at least n messages arrived (or the timeout passed).
func (s *Server) Wait(n int, timeout time.Duration) []Message {
	deadline := time.Now().Add(timeout)
	for {
		if m := s.Messages(); len(m) >= n || time.Now().After(deadline) {
			return m
		}
		select {
		case <-s.arrived:
		case <-time.After(20 * time.Millisecond):
		}
	}
}

func (s *Server) serve() {
	for {
		c, err := s.ln.Accept()
		if err != nil {
			return
		}
		go s.handle(c)
	}
}

func (s *Server) handle(c net.Conn) {
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(30 * time.Second))
	r, w := bufio.NewReader(c), bufio.NewWriter(c)
	reply := func(line string) { _, _ = w.WriteString(line + "\r\n"); _ = w.Flush() }
	reply("220 fake.smtp ESMTP")
	var msg Message
	tlsOn := s.Mode == "tls"
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		cmd := strings.ToUpper(line)
		switch {
		case strings.HasPrefix(cmd, "EHLO"), strings.HasPrefix(cmd, "HELO"):
			ext := []string{"250-fake.smtp", "250-AUTH PLAIN"}
			if s.Mode == "starttls" && !tlsOn {
				ext = append(ext, "250-STARTTLS")
			}
			ext = append(ext, "250 8BITMIME")
			for _, e := range ext {
				_, _ = w.WriteString(e + "\r\n")
			}
			_ = w.Flush()
		case cmd == "STARTTLS":
			reply("220 ready")
			tc := tls.Server(c, s.srvTLS)
			if tc.Handshake() != nil {
				return
			}
			c, tlsOn = tc, true
			r, w = bufio.NewReader(c), bufio.NewWriter(c)
		case strings.HasPrefix(cmd, "AUTH PLAIN"):
			arg := strings.TrimSpace(line[len("AUTH PLAIN"):])
			if arg == "" {
				reply("334 ")
				l, _ := r.ReadString('\n')
				arg = strings.TrimSpace(l)
			}
			raw, _ := base64.StdEncoding.DecodeString(arg)
			parts := strings.Split(string(raw), "\x00")
			if len(parts) == 3 {
				msg.Username, msg.Password = parts[1], parts[2]
			}
			if s.RejectAuth.Load() {
				reply("535 5.7.8 authentication failed")
				continue
			}
			reply("235 2.7.0 ok")
		case strings.HasPrefix(cmd, "MAIL FROM:"):
			msg.From = addrArg(line[len("MAIL FROM:"):])
			reply("250 ok")
		case strings.HasPrefix(cmd, "RCPT TO:"):
			msg.To = append(msg.To, addrArg(line[len("RCPT TO:"):]))
			reply("250 ok")
		case cmd == "DATA":
			reply("354 go ahead")
			var buf bytes.Buffer
			for {
				l, err := r.ReadString('\n')
				if err != nil {
					return
				}
				if l == ".\r\n" {
					break
				}
				buf.WriteString(strings.TrimPrefix(l, "."))
			}
			if s.FailNext.Load() > 0 {
				s.FailNext.Add(-1)
				reply("451 4.3.0 try again later")
				msg = Message{Username: msg.Username, Password: msg.Password}
				continue
			}
			msg.Raw = buf.String()
			parse(&msg)
			s.mu.Lock()
			s.msgs = append(s.msgs, msg)
			s.mu.Unlock()
			select {
			case s.arrived <- struct{}{}:
			default:
			}
			msg = Message{Username: msg.Username, Password: msg.Password}
			reply("250 queued")
		case cmd == "RSET":
			msg = Message{}
			reply("250 ok")
		case cmd == "NOOP":
			reply("250 ok")
		case cmd == "QUIT":
			reply("221 bye")
			return
		default:
			reply("502 unknown command")
		}
	}
}

// addrArg extracts the address of "<a@b> PARAM=…".
func addrArg(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '>'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimPrefix(s, "<")
}

func parse(m *Message) {
	pm, err := mail.ReadMessage(strings.NewReader(m.Raw))
	if err != nil {
		return
	}
	m.Header = pm.Header
	dec := new(mime.WordDecoder)
	m.Subject, _ = dec.DecodeHeader(pm.Header.Get("Subject"))
	_, params, err := mime.ParseMediaType(pm.Header.Get("Content-Type"))
	if err != nil {
		return
	}
	mr := multipart.NewReader(pm.Body, params["boundary"])
	for {
		p, err := mr.NextPart()
		if err != nil {
			return
		}
		var body []byte
		raw, _ := io.ReadAll(p)
		if strings.EqualFold(p.Header.Get("Content-Transfer-Encoding"), "base64") {
			body, _ = base64.StdEncoding.DecodeString(strings.ReplaceAll(strings.ReplaceAll(string(raw), "\r\n", ""), "\n", ""))
		} else {
			body = raw
		}
		if strings.HasPrefix(p.Header.Get("Content-Type"), "text/html") {
			m.HTML = string(body)
		} else {
			m.Text = string(body)
		}
	}
}

func selfSigned(t testing.TB) (tls.Certificate, *x509.CertPool) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "127.0.0.1"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := x509.ParseCertificate(der)
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, pool
}

// Addr is "host:port".
func (s *Server) Addr() string { return net.JoinHostPort(s.Host, strconv.Itoa(s.Port)) }
