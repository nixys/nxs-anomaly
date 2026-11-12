package engine

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"mime"
	"mime/quotedprintable"
	"net"
	"net/smtp"
	"strings"
	"time"

	"github.com/nixys/nxs-anomaly/internal/utils"
)

// E-mail delivery over SMTP.

// emailSubject names what the message is about, so a mailbox of pages can be
// triaged — and threaded — per incident. The old fixed subject made every alert
// the same conversation.
func emailSubject(payload map[string]any) string {
	if utils.StrVal(payload, "kind") == "report_digest" {
		return "On-call quality report"
	}
	title := utils.StrVal(payload, "title")
	if title == "" {
		return "nxs-anomaly notification"
	}
	subject := title
	if sev := utils.StrVal(payload, "severity"); sev != "" {
		subject = "[" + sev + "] " + subject
	}
	if utils.StrVal(payload, "status") == "resolved" {
		subject = "[RESOLVED] " + subject
	}
	return subject
}

// buildEmailMessage renders an RFC 5322 message with a UTF-8 body. Date and
// Message-ID are required or expected by receiving servers; without MIME
// headers a Cyrillic alert title arrived as mojibake or not at all.
func buildEmailMessage(from, recipient, subject, body string, now time.Time, host string) []byte {
	var qp bytes.Buffer
	w := quotedprintable.NewWriter(&qp)
	_, _ = w.Write([]byte(body))
	_ = w.Close()
	var b bytes.Buffer
	fmt.Fprintf(&b, "From: %s\r\n", from)
	fmt.Fprintf(&b, "To: %s\r\n", recipient)
	fmt.Fprintf(&b, "Subject: %s\r\n", mime.QEncoding.Encode("utf-8", subject))
	fmt.Fprintf(&b, "Date: %s\r\n", now.Format(time.RFC1123Z))
	fmt.Fprintf(&b, "Message-ID: <%s@%s>\r\n", utils.MakeID("msg"), strDefault(host, "nxs-anomaly"))
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/plain; charset=UTF-8\r\n")
	b.WriteString("Content-Transfer-Encoding: quoted-printable\r\n\r\n")
	b.Write(qp.Bytes())
	return b.Bytes()
}

func sendEmail(ctx context.Context, recipient, subject, text string, cfg SMTPConfig) (status, errMsg, providerResp string) {
	if cfg.Host == "" {
		return "failed", "NXS_ANOMALY_SMTP_HOST is not set", ""
	}
	if recipient == "" {
		return "failed", "email target recipient is empty", ""
	}
	host := cfg.Host
	port := cfg.Port
	username := cfg.Username
	password := cfg.Password
	sender := cfg.Sender
	senderName := cfg.From
	useTLS := cfg.UseTLS

	from := fmt.Sprintf("%s <%s>", senderName, sender)
	msgHost := sender
	if i := strings.LastIndex(sender, "@"); i >= 0 {
		msgHost = sender[i+1:]
	}
	msg := buildEmailMessage(from, recipient, subject, text, time.Now(), msgHost)
	addr := fmt.Sprintf("%s:%d", host, port)

	var auth smtp.Auth
	if username != "" {
		auth = smtp.PlainAuth("", username, password, host)
	}

	// One path for direct and proxied delivery. The direct one used to be
	// tls.Dial or smtp.SendMail, and neither takes a context or a deadline: a
	// server that accepted the connection and never answered held the delivery
	// goroutine — and with it the whole delivery stage and every later worker
	// cycle — forever. With a SOCKS5 proxy or a tcp relay configured for the
	// email channel the TCP connection comes from that dialler instead.
	dial := cfg.Dial
	if dial == nil {
		dialer := &net.Dialer{Timeout: cfg.Timeout}
		dial = func(network, addr string) (net.Conn, error) {
			return dialer.DialContext(ctx, network, addr)
		}
	}
	return sendEmailVia(ctx, dial, cfg.Timeout, addr, host, sender, recipient, msg, auth, useTLS)
}

// sendEmailVia sends one message over a connection the given dialler produced.
// It mirrors what smtp.SendMail does, including opportunistic STARTTLS on the
// plain port: dropping that would quietly downgrade the connection.
//
// The whole exchange — TLS handshake, greeting, every command, DATA — runs
// under one deadline of the delivery timeout, the same bound every HTTP
// channel has, and a cancelled ctx ends it at once. net/smtp observes neither
// on its own; the deadline on the connection is what it does observe.
func sendEmailVia(ctx context.Context, dial func(network, addr string) (net.Conn, error), timeout time.Duration, addr, host, sender, recipient string, msg []byte, auth smtp.Auth, useTLS bool) (status, errMsg, providerResp string) {
	conn, err := dial("tcp", addr)
	if err != nil {
		return "failed", err.Error(), ""
	}
	_ = conn.SetDeadline(time.Now().Add(timeout))
	stop := context.AfterFunc(ctx, func() { _ = conn.SetDeadline(time.Unix(1, 0)) })
	defer stop()
	if useTLS {
		tlsConn := tls.Client(conn, &tls.Config{ServerName: host})
		if err := tlsConn.Handshake(); err != nil {
			conn.Close() //nolint:errcheck
			return "failed", err.Error(), ""
		}
		conn = tlsConn
	}
	client, err := smtp.NewClient(conn, host)
	if err != nil {
		conn.Close() //nolint:errcheck
		return "failed", err.Error(), ""
	}
	defer client.Close() //nolint:errcheck
	if !useTLS {
		if ok, _ := client.Extension("STARTTLS"); ok {
			if err := client.StartTLS(&tls.Config{ServerName: host}); err != nil {
				return "failed", err.Error(), ""
			}
		}
	}
	if auth != nil {
		if err := client.Auth(auth); err != nil {
			return "failed", err.Error(), ""
		}
	}
	if err := client.Mail(sender); err != nil {
		return "failed", err.Error(), ""
	}
	if err := client.Rcpt(recipient); err != nil {
		return "failed", err.Error(), ""
	}
	w, err := client.Data()
	if err != nil {
		return "failed", err.Error(), ""
	}
	if _, err := w.Write(msg); err != nil {
		return "failed", err.Error(), ""
	}
	if err := w.Close(); err != nil {
		return "failed", err.Error(), ""
	}
	return "delivered", "", "smtp"
}
