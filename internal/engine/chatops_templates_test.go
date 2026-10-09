package engine

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nixys/nxs-anomaly/internal/model"
)

// chatopsSink is a webhook that records the text of every post.
func chatopsSink(t *testing.T) (*httptest.Server, *[]string) {
	t.Helper()
	var texts []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := decodeJSONBody(r)
		text, _ := body["text"].(string)
		texts = append(texts, text)
		_, _ = w.Write([]byte("ok"))
	}))
	t.Cleanup(srv.Close)
	return srv, &texts
}

func templatedChatopsEngine(t *testing.T, platform string, templates map[string]any, publicURL string) (*Engine, *[]string) {
	t.Helper()
	srv, texts := chatopsSink(t)
	ms := newMemStore()
	ms.seed("chatops_channels", map[string]any{
		"id": "chat_1", "name": "#sre", "platform": platform, "webhook_url": srv.URL,
	})
	ms.seed("integrations", map[string]any{"id": "int_1", "name": "prod", "templates": templates})
	return honestyEngine(ms, DeliveryConfig{PublicURL: publicURL}), texts
}

func chatopsPayload() map[string]any {
	return map[string]any{
		"alert_group_id": "grp_1", "integration_id": "int_1",
		"title": "Disk full", "severity": "critical", "reason": "escalation step",
	}
}

func deliverChatopsText(t *testing.T, e *Engine, texts *[]string, payload map[string]any) string {
	t.Helper()
	res := e.deliverNotificationViaAdapter(context.Background(), notificationFor("chatops", "chat_1", payload))
	if res.Status != deliveryDelivered {
		t.Fatalf("status = %q (%s), want delivered", res.Status, res.Err)
	}
	if len(*texts) == 0 {
		t.Fatal("nothing was posted")
	}
	return (*texts)[len(*texts)-1]
}

// Without a template and without a public URL the channel reads exactly what
// it read before templates applied to it.
func TestChatopsBuiltInTextUnchangedWithoutPublicURL(t *testing.T) {
	e, texts := templatedChatopsEngine(t, "slack", nil, "")
	payload := chatopsPayload()

	got := deliverChatopsText(t, e, texts, payload)

	n := notificationFor("chatops", "chat_1", payload)
	if want := renderNotificationText(n, payload, ""); got != want {
		t.Errorf("text = %q, want the built-in %q", got, want)
	}
}

func TestChatopsBuiltInTextLinksTheGroup(t *testing.T) {
	e, texts := templatedChatopsEngine(t, "slack", nil, "https://anomaly.example.com/")

	got := deliverChatopsText(t, e, texts, chatopsPayload())

	if !strings.HasSuffix(got, "\nhttps://anomaly.example.com/alert-groups/grp_1") {
		t.Errorf("built-in text does not end with the group link: %q", got)
	}
}

// chatops wins over the platform key, the platform key over default.
func TestChatopsTemplateLookupOrder(t *testing.T) {
	cases := []struct {
		name      string
		templates map[string]any
		want      string
	}{
		{"chatops", map[string]any{"chatops": "C {{ .title }}", "slack": "S", "default": "D"}, "C Disk full"},
		{"platform", map[string]any{"slack": "S {{ .severity }}", "default": "D"}, "S critical"},
		{"default", map[string]any{"telegram": "T", "default": "D {{ .group_id }}"}, "D grp_1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e, texts := templatedChatopsEngine(t, "slack", tc.templates, "https://anomaly.example.com")
			if got := deliverChatopsText(t, e, texts, chatopsPayload()); got != tc.want {
				t.Errorf("text = %q, want %q", got, tc.want)
			}
		})
	}
}

// Every link key is in the context whether or not it has a value: a missing
// key would make the renderer fall back and post the raw template.
func TestTemplateContextCarriesLinks(t *testing.T) {
	tmpl := "{{ .group_url }}|{{ .generator_url }}|{{ .dashboard_url }}|{{ .panel_url }}|{{ .silence_url }}"
	e, texts := templatedChatopsEngine(t, "mattermost", map[string]any{"chatops": tmpl}, "https://anomaly.example.com")

	payload := chatopsPayload()
	payload["source_links"] = map[string]any{
		"generator_url": "https://grafana.example.com/alerting/1/view",
		"panel_url":     "https://grafana.example.com/d/abc?viewPanel=2",
	}
	got := deliverChatopsText(t, e, texts, payload)
	want := "https://anomaly.example.com/alert-groups/grp_1|https://grafana.example.com/alerting/1/view||" +
		"https://grafana.example.com/d/abc?viewPanel=2|"
	if got != want {
		t.Errorf("text = %q, want %q", got, want)
	}

	e, texts = templatedChatopsEngine(t, "mattermost", map[string]any{"chatops": tmpl}, "")
	if got := deliverChatopsText(t, e, texts, chatopsPayload()); got != "||||" {
		t.Errorf("text without any link = %q, want every placeholder empty", got)
	}
}

// A Telegram ChatOps channel goes out through the Telegram adapter, and reads
// the chatops template before the telegram one; a person's own Telegram chat
// does not.
func TestTelegramChatopsChannelReadsChatopsTemplateFirst(t *testing.T) {
	ms := newMemStore()
	ms.seed("integrations", map[string]any{"id": "int_1", "templates": map[string]any{
		"chatops": "team", "telegram": "personal",
	}})
	e := honestyEngine(ms, DeliveryConfig{})
	ctx := context.Background()

	if got := e.getNotificationTemplate(ctx, "int_1", "chatops", "telegram"); got != "team" {
		t.Errorf("channel template = %q, want team", got)
	}
	if got := e.getNotificationTemplate(ctx, "int_1", "telegram"); got != "personal" {
		t.Errorf("personal template = %q, want personal", got)
	}
}

func TestAlertSourceLinksFromGrafanaPayload(t *testing.T) {
	payload := normalizeGrafanaAlertingAlert(map[string]any{"status": "firing"}, map[string]any{
		"status":       "firing",
		"labels":       map[string]any{"alertname": "DiskFull"},
		"generatorURL": "https://grafana.example.com/alerting/grafana/abc/view",
		"silenceURL":   "https://grafana.example.com/alerting/silence/new",
		"dashboardURL": "https://grafana.example.com/d/abc",
		"panelURL":     "https://grafana.example.com/d/abc?viewPanel=2",
	})
	links := alertSourceLinks(payload)
	for _, k := range sourceLinkKeys {
		if links[k] == "" {
			t.Errorf("%s not collected from the Grafana payload: %v", k, links)
		}
	}
}

// A repeat that carries no links keeps the ones the group already has.
func TestGroupSourceLinksSurviveALinklessRepeat(t *testing.T) {
	g := model.WrapAlertGroup(map[string]any{"id": "grp_1"})
	g.SetSourceLinks(map[string]string{"generator_url": "https://prometheus.example.com/graph"})
	g.SetSourceLinks(map[string]string{})
	if got := g.SourceLinks()["generator_url"]; got != "https://prometheus.example.com/graph" {
		t.Errorf("generator_url = %q after a linkless repeat", got)
	}
	if _, present := model.WrapAlertGroup(map[string]any{"id": "grp_2"}).Raw()["source_links"]; present {
		t.Error("a group without links grew a source_links key")
	}

	p := notificationPayload(g, nil, "test")
	links, _ := p["source_links"].(map[string]any)
	if links["generator_url"] != "https://prometheus.example.com/graph" {
		t.Errorf("notification payload links = %v", p["source_links"])
	}
}

// The links travel from the alert to the group at ingest, so an escalation
// step that runs later — without the alert in hand — can still put them in
// the message.
func TestIngestCarriesSourceLinksToTheGroup(t *testing.T) {
	ms := ingestStore()
	e := crudEngine(ms)
	if _, err := e.IngestGrafanaAlerting(context.Background(), "key-1", map[string]any{
		"status": "firing",
		"alerts": []any{map[string]any{
			"status": "firing", "fingerprint": "g1",
			"labels":       map[string]any{"alertname": "HighCPU"},
			"generatorURL": "https://grafana.example.com/alerting/grafana/abc/view",
			"panelURL":     "https://grafana.example.com/d/abc?viewPanel=2",
		}},
	}); err != nil {
		t.Fatal(err)
	}
	g := model.WrapAlertGroup(groupsOf(ms)[0])
	links := g.SourceLinks()
	if links["generator_url"] != "https://grafana.example.com/alerting/grafana/abc/view" ||
		links["panel_url"] != "https://grafana.example.com/d/abc?viewPanel=2" {
		t.Errorf("group links = %v", links)
	}
	if _, present := links["silence_url"]; present {
		t.Errorf("a link the source did not send was stored: %v", links)
	}
}
