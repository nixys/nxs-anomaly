package engine

import (
	"bytes"
	"io"
	"mime"
	"net/mail"
	"strings"
	"testing"
	"time"

	"mime/quotedprintable"
)

// Every alert used to arrive as "Nixys Monitoring Alert" with no Date, no MIME
// headers and a raw UTF-8 body.
func TestEmailMessageIsAWellFormedUTF8Message(t *testing.T) {
	subject := emailSubject(map[string]any{"title": "Диск заполнен на db-1", "severity": "critical", "status": "open"})
	if subject != "[critical] Диск заполнен на db-1" {
		t.Errorf("subject = %q", subject)
	}
	raw := buildEmailMessage("Nixys Alerter <a@qa.local>", "qa@qa.local", subject,
		"[critical] Диск заполнен на db-1\nhost=db-1", time.Date(2026, 9, 17, 10, 0, 0, 0, time.UTC), "qa.local")

	msg, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("not a parseable message: %v", err)
	}
	for _, h := range []string{"Date", "Message-Id", "Mime-Version", "Content-Type", "Content-Transfer-Encoding"} {
		if msg.Header.Get(h) == "" {
			t.Errorf("missing header %s", h)
		}
	}
	if _, err := msg.Header.Date(); err != nil {
		t.Errorf("Date header does not parse: %v", err)
	}
	decoded, err := new(mime.WordDecoder).DecodeHeader(msg.Header.Get("Subject"))
	if err != nil || decoded != subject {
		t.Errorf("Subject decodes to %q (%v), want %q", decoded, err, subject)
	}
	body, _ := io.ReadAll(quotedprintable.NewReader(msg.Body))
	if !strings.Contains(string(body), "Диск заполнен на db-1") {
		t.Errorf("body = %q, want the UTF-8 text back", body)
	}
}

func TestEmailSubjectVariants(t *testing.T) {
	for _, tc := range []struct {
		payload map[string]any
		want    string
	}{
		{map[string]any{"title": "CPU", "severity": "warning", "status": "resolved"}, "[RESOLVED] [warning] CPU"},
		{map[string]any{"kind": "report_digest"}, "On-call quality report"},
		{map[string]any{}, "nxs-anomaly notification"},
	} {
		if got := emailSubject(tc.payload); got != tc.want {
			t.Errorf("emailSubject(%v) = %q, want %q", tc.payload, got, tc.want)
		}
	}
}
