package engine

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func testClient() *http.Client { return &http.Client{Timeout: 2 * time.Second} }

func TestPostWebhook(t *testing.T) {
	t.Run("2xx delivered", func(t *testing.T) {
		var gotBody bool
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Content-Type") == "application/json" {
				gotBody = true
			}
			w.WriteHeader(http.StatusOK)
		}))
		defer srv.Close()
		status, msg := postWebhook(context.Background(), testClient(), srv.URL, map[string]any{"x": 1}, false)
		if status != "delivered" || msg != "" || !gotBody {
			t.Fatalf("status=%q msg=%q gotBody=%v", status, msg, gotBody)
		}
	})
	t.Run("5xx failed", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		defer srv.Close()
		status, msg := postWebhook(context.Background(), testClient(), srv.URL, nil, false)
		if status != "failed" || msg == "" {
			t.Fatalf("status=%q msg=%q", status, msg)
		}
	})
	t.Run("unreachable failed", func(t *testing.T) {
		status, _ := postWebhook(context.Background(), testClient(), "http://127.0.0.1:1/x", nil, false)
		if status != "failed" {
			t.Fatalf("status=%q, want failed", status)
		}
	})
	t.Run("ssrf guard blocks private", func(t *testing.T) {
		status, msg := postWebhook(context.Background(), testClient(), "http://127.0.0.1/x", nil, true)
		if status != "failed" || msg == "" {
			t.Fatalf("guard not enforced: status=%q msg=%q", status, msg)
		}
	})
}

func TestSendTelegramGuardBranches(t *testing.T) {
	status, msg, _ := sendTelegram(context.Background(), testClient(), "chat", "hi", "", "grp-1", telegramShiftOptions{}, "")
	if status != "failed" || msg == "" {
		t.Errorf("empty token should fail: %q %q", status, msg)
	}
	status, msg, _ = sendTelegram(context.Background(), testClient(), "", "hi", "tok", "grp-1", telegramShiftOptions{}, "")
	if status != "failed" || msg == "" {
		t.Errorf("empty chat id should fail: %q %q", status, msg)
	}
}

func TestCallMessage(t *testing.T) {
	if got := callMessage(map[string]any{"severity": "critical", "title": "DB down"}); got != "Emergency alert. DB down. See messages for details." {
		t.Errorf("critical msg = %q", got)
	}
	if got := callMessage(map[string]any{"title": "Blip"}); got != "Alert. Blip. See messages for details." {
		t.Errorf("default msg = %q", got)
	}
	// Defaults when fields absent.
	if got := callMessage(map[string]any{}); got != "Alert. alert. See messages for details." {
		t.Errorf("empty msg = %q", got)
	}
}

func TestCreateGenericIssue(t *testing.T) {
	t.Run("created with bearer", func(t *testing.T) {
		var auth string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			auth = r.Header.Get("Authorization")
			w.WriteHeader(http.StatusCreated)
		}))
		defer srv.Close()
		status, _, msg := createGenericIssue(context.Background(), testClient(), srv.URL, "secret", "subj", "body")
		if status != "created" || msg != "" {
			t.Fatalf("status=%q msg=%q", status, msg)
		}
		if auth != "Bearer secret" {
			t.Errorf("auth header = %q", auth)
		}
	})
	t.Run("non-2xx failed", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusBadGateway)
		}))
		defer srv.Close()
		status, _, msg := createGenericIssue(context.Background(), testClient(), srv.URL, "", "s", "b")
		if status != "failed" || msg == "" {
			t.Fatalf("status=%q msg=%q", status, msg)
		}
	})
}

func TestCreateRedmineIssue(t *testing.T) {
	var gotKey, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("X-Redmine-API-Key")
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()
	status, _, msg := createRedmineIssue(context.Background(), testClient(), srv.URL+"/", "rkey", "proj", "subj", "body")
	if status != "created" || msg != "" {
		t.Fatalf("status=%q msg=%q", status, msg)
	}
	if gotKey != "rkey" {
		t.Errorf("api key header = %q", gotKey)
	}
	if gotPath != "/issues.json" {
		t.Errorf("path = %q, want /issues.json", gotPath)
	}
}

func TestToAlertmanagerPayload(t *testing.T) {
	out := toAlertmanagerPayload(map[string]any{
		"title":          "DiskFull",
		"severity":       "critical",
		"status":         "firing",
		"alert_group_id": "g42",
	})
	if out["receiver"] != "nxs-anomaly" || out["version"] != "4" {
		t.Errorf("envelope fields wrong: %v", out)
	}
	if out["status"] != "firing" || out["groupKey"] != "g42" {
		t.Errorf("status/groupKey wrong: %v", out)
	}
	alerts, _ := out["alerts"].([]any)
	if len(alerts) != 1 {
		t.Fatalf("alerts len = %d", len(alerts))
	}
	alert := alerts[0].(map[string]any)
	labels := alert["labels"].(map[string]any)
	if labels["alertname"] != "DiskFull" || labels["severity"] != "critical" {
		t.Errorf("labels not derived from title/severity: %v", labels)
	}
	ann := alert["annotations"].(map[string]any)
	if ann["summary"] != "DiskFull" {
		t.Errorf("summary = %v", ann["summary"])
	}

	// Resolved status maps through.
	out = toAlertmanagerPayload(map[string]any{"status": "resolved", "title": "x"})
	if out["status"] != "resolved" {
		t.Errorf("resolved status = %v", out["status"])
	}
}

func TestSendEmailGuardBranches(t *testing.T) {
	if s, _, _ := sendEmail("a@x.io", "subj", "hi", SMTPConfig{}); s != "failed" {
		t.Errorf("empty host should fail: %q", s)
	}
	if s, _, _ := sendEmail("", "subj", "hi", SMTPConfig{Host: "smtp.x.io", Port: 25}); s != "failed" {
		t.Errorf("empty recipient should fail: %q", s)
	}
}

func TestSendAsteriskGuardBranches(t *testing.T) {
	if s, msg, _ := sendAsteriskCall("+1", "hi", nil); s != "failed" || msg == "" {
		t.Errorf("no instances should fail: %q %q", s, msg)
	}
	// Incomplete instance settings rejected before any dial.
	if s, _, _ := sendAsteriskCallToInstance(asteriskInstance{host: "h"}, "+1", "hi"); s != "failed" {
		t.Errorf("incomplete settings should fail: %q", s)
	}
	if s, _, _ := sendAsteriskCall("+1", "hi", []asteriskInstance{{host: "h"}}); s != "failed" {
		t.Errorf("incomplete instance via sendAsteriskCall should fail: %q", s)
	}
}
