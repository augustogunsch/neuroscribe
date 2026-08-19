package main

// Outgoing mail over SMTP (stdlib only), used for address verification.
//
// Any transactional provider works — Resend, Brevo, Postmark, SES, Mailgun all
// speak SMTP. Configure with NEUROSCRIBE_SMTP_* ; see the README. When it is
// not configured, public registration stays switched off and accounts are
// created from the CLI as before.

import (
	"crypto/tls"
	"errors"
	"fmt"
	"log"
	"net"
	"net/mail"
	"net/smtp"
	"os"
	"strings"
	"time"
)

type mailer struct {
	host    string
	port    string
	user    string
	pass    string
	from    string
	baseURL string

	// sendFunc replaces the SMTP conversation in tests
	sendFunc func(to, subject, body string) error
}

func newMailer(baseURL string) *mailer {
	return &mailer{
		host:    envOr("SMTP_HOST", ""),
		port:    envOr("SMTP_PORT", "465"),
		user:    envOr("SMTP_USER", ""),
		pass:    envOr("SMTP_PASS", ""),
		from:    envOr("MAIL_FROM", ""),
		baseURL: strings.TrimRight(baseURL, "/"),
	}
}

// configured reports whether mail can actually be sent; registration depends
// on it, since an unverifiable address is worse than none.
func (m *mailer) configured() bool {
	return m.sendFunc != nil || (m.host != "" && m.from != "" && m.baseURL != "")
}

// headerSafe strips CR/LF so nothing can inject extra SMTP headers.
func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}

func headerSafe(v string) string {
	return strings.NewReplacer("\r", "", "\n", "").Replace(v)
}

func validEmail(addr string) bool {
	if len(addr) > 254 || strings.ContainsAny(addr, "\r\n") {
		return false
	}
	parsed, err := mail.ParseAddress(addr)
	if err != nil || parsed.Address != addr {
		return false
	}
	at := strings.LastIndex(addr, "@")
	return at > 0 && strings.Contains(addr[at:], ".")
}

func (m *mailer) send(to, subject, body string) error {
	if m.sendFunc != nil {
		return m.sendFunc(to, subject, body)
	}
	if !m.configured() {
		return errors.New("mail is not configured")
	}
	started := time.Now()
	err := m.deliver(to, subject, body)
	if err != nil {
		log.Printf("mail: FAILED to %s via %s:%s after %s — %v",
			to, m.host, m.port, time.Since(started).Round(time.Millisecond), err)
		return err
	}
	log.Printf("mail: sent %q to %s via %s:%s in %s",
		subject, to, m.host, m.port, time.Since(started).Round(time.Millisecond))
	return nil
}

func (m *mailer) deliver(to, subject, body string) error {
	to = headerSafe(to)
	msg := strings.Join([]string{
		"From: " + headerSafe(m.from),
		"To: " + to,
		"Subject: " + headerSafe(subject),
		"Date: " + time.Now().Format(time.RFC1123Z),
		"MIME-Version: 1.0",
		"Content-Type: text/plain; charset=utf-8",
		"", body,
	}, "\r\n")

	client, err := m.dial()
	if err != nil {
		return err
	}
	defer client.Close()

	if m.user != "" {
		if err := m.authenticate(client); err != nil {
			return err
		}
	}
	fromAddr := m.from
	if parsed, perr := mail.ParseAddress(m.from); perr == nil {
		fromAddr = parsed.Address
	}
	if err := client.Mail(fromAddr); err != nil {
		return err
	}
	if err := client.Rcpt(to); err != nil {
		return err
	}
	w, err := client.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write([]byte(msg)); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return client.Quit()
}

const (
	dialTimeout = 15 * time.Second
	sendTimeout = 60 * time.Second
)

// dial opens an authenticated-capable SMTP connection. Port 465 is implicit
// TLS; anything else is upgraded with STARTTLS. Every step is bounded: a port
// that accepts TCP but never speaks SMTP (a blackholing middlebox, common on
// 587) has to surface as an error rather than a hung request.
func (m *mailer) dial() (*smtp.Client, error) {
	addr := net.JoinHostPort(m.host, m.port)
	conn, err := net.DialTimeout("tcp", addr, dialTimeout)
	if err != nil {
		return nil, fmt.Errorf("connecting to %s: %w", addr, err)
	}
	// bounds the banner, EHLO, TLS handshake and the whole conversation
	_ = conn.SetDeadline(time.Now().Add(sendTimeout))

	if m.port == "465" {
		tlsConn := tls.Client(conn, &tls.Config{ServerName: m.host})
		if err := tlsConn.Handshake(); err != nil {
			conn.Close()
			return nil, fmt.Errorf("TLS handshake with %s: %w", addr, err)
		}
		conn = tlsConn
	}
	client, err := smtp.NewClient(conn, m.host)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("no SMTP greeting from %s (port blocked or filtered?): %w", addr, err)
	}
	if m.port != "465" {
		if ok, _ := client.Extension("STARTTLS"); ok {
			if err := client.StartTLS(&tls.Config{ServerName: m.host}); err != nil {
				client.Close()
				return nil, fmt.Errorf("STARTTLS with %s: %w", addr, err)
			}
		} else if !isLoopback(m.host) {
			// never hand credentials or addresses to a remote relay in the
			// clear; local dev catchers (Mailpit et al.) are fine
			client.Close()
			return nil, fmt.Errorf("smtp server %s does not offer STARTTLS — use port 465", m.host)
		}
	}
	return client, nil
}

// authenticate picks a mechanism the server actually advertises. Go's stdlib
// only implements PLAIN and CRAM-MD5; several providers (and older relays)
// offer LOGIN only, so that one is implemented below.
func (m *mailer) authenticate(client *smtp.Client) error {
	hasAuth, mechs := client.Extension("AUTH")
	switch {
	case hasAuth && strings.Contains(mechs, "PLAIN"):
		return client.Auth(smtp.PlainAuth("", m.user, m.pass, m.host))
	case hasAuth && strings.Contains(mechs, "LOGIN"):
		return client.Auth(&loginAuth{user: m.user, pass: m.pass, host: m.host})
	case !hasAuth:
		if isLoopback(m.host) {
			return nil // local catcher with authentication disabled
		}
		return fmt.Errorf("smtp server %s does not offer authentication", m.host)
	default:
		return fmt.Errorf("smtp server %s offers no supported auth mechanism (%s)", m.host, mechs)
	}
}

// loginAuth implements the non-standard but widespread AUTH LOGIN exchange.
type loginAuth struct{ user, pass, host string }

func (a *loginAuth) Start(server *smtp.ServerInfo) (string, []byte, error) {
	if !server.TLS && !isLoopback(a.host) {
		return "", nil, errors.New("refusing to send SMTP credentials over an unencrypted connection")
	}
	return "LOGIN", nil, nil
}

func (a *loginAuth) Next(fromServer []byte, more bool) ([]byte, error) {
	if !more {
		return nil, nil
	}
	switch strings.ToLower(strings.TrimSpace(string(fromServer))) {
	case "username:":
		return []byte(a.user), nil
	case "password:":
		return []byte(a.pass), nil
	default:
		return nil, fmt.Errorf("unexpected SMTP challenge %q", fromServer)
	}
}

// sendTest delivers a short message, used by `neuroscribe mail test`.
func (m *mailer) sendTest(to string) error {
	return m.send(to, "Neuroscribe test message",
		"If you are reading this, Neuroscribe can send mail through your provider.\n")
}

func (m *mailer) sendVerification(to, username, token string) error {
	link := fmt.Sprintf("%s/verify?token=%s", m.baseURL, token)
	body := fmt.Sprintf(`Hi %s,

Confirm this address to activate your Neuroscribe account:

%s

The link is valid for 24 hours. If you did not sign up, ignore this message —
the account stays inactive and is removed automatically.
`, username, link)
	return m.send(to, "Confirm your Neuroscribe account", body)
}

// runMailCLI implements `neuroscribe mail test <address>`.
func runMailCLI(m *mailer, args []string) {
	if len(args) < 2 || args[0] != "test" {
		fmt.Fprintln(os.Stderr, "usage: neuroscribe mail test <address>")
		os.Exit(2)
	}
	if !m.configured() {
		log.Fatal("mail is not configured: set NEUROSCRIBE_SMTP_HOST, NEUROSCRIBE_MAIL_FROM and NEUROSCRIBE_BASE_URL")
	}
	fmt.Printf("sending via %s:%s as %q, from %q\n", m.host, m.port, m.user, m.from)
	if err := m.sendTest(args[1]); err != nil {
		log.Fatalf("send failed: %v", err)
	}
	fmt.Println("sent — check", args[1])
}
