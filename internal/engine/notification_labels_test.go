package engine

import (
	"strings"
	"testing"
)

func k8sPayload() map[string]any {
	return map[string]any{
		"title":          "KubePodCrashLooping",
		"severity":       "critical",
		"alert_group_id": "grp_1",
		"labels": map[string]any{
			"alertname":     "KubePodCrashLooping",
			"severity":      "critical",
			"pod":           "checkout-7d9f-abcde",
			"namespace":     "payments",
			"container":     "api",
			"kubernetes.io": "cluster-a",
		},
	}
}

// TestDefaultTextNamesThePodAndNamespace: the labels were carried in the
// payload and stored — which is why the UI showed them — and then dropped when
// the message was built, so the delivered alert named an incident without
// saying what it was about.
func TestDefaultTextNamesThePodAndNamespace(t *testing.T) {
	got := renderNotificationText(map[string]any{}, k8sPayload(), "")
	for _, want := range []string{"pod=checkout-7d9f-abcde", "namespace=payments", "container=api"} {
		if !strings.Contains(got, want) {
			t.Errorf("delivered text is missing %q:\n%s", want, got)
		}
	}
	// The title already says the alertname and the prefix already says the
	// severity; repeating them costs a line and says nothing.
	if strings.Contains(got, "alertname=") || strings.Contains(got, "severity=critical") {
		t.Errorf("labels line repeats what the text already says:\n%s", got)
	}
	if !strings.HasPrefix(got, "[critical] KubePodCrashLooping") {
		t.Errorf("the existing first line changed shape:\n%s", got)
	}
}

// A template must be able to reach a single label. The renderer stringifies the
// context into map[string]string before executing, so {{ labels.pod }} cannot
// work by construction — {{ label_pod }} is the reachable form, and label names
// carrying dots or slashes have to be spellable too.
func TestTemplateCanReachIndividualLabels(t *testing.T) {
	got := renderNotificationText(map[string]any{}, k8sPayload(),
		"{{ label_namespace }}/{{ label_pod }} [{{ label_kubernetes_io }}]")
	if got != "payments/checkout-7d9f-abcde [cluster-a]" {
		t.Errorf("rendered %q", got)
	}
}

func TestTemplateCanRenderAllLabelsAtOnce(t *testing.T) {
	got := renderNotificationText(map[string]any{}, k8sPayload(), "{{ labels }}")
	if !strings.Contains(got, "namespace=payments") || !strings.Contains(got, "pod=checkout-7d9f-abcde") {
		t.Errorf("{{ labels }} rendered %q", got)
	}
	// Deterministic: the same alert has to read the same way twice.
	if got != renderNotificationText(map[string]any{}, k8sPayload(), "{{ labels }}") {
		t.Error("{{ labels }} is not deterministic")
	}
}

// A label must never quietly replace a built-in context key.
func TestLabelsDoNotShadowBuiltins(t *testing.T) {
	p := k8sPayload()
	p["labels"].(map[string]any)["title"] = "not-the-title"
	got := renderNotificationText(map[string]any{}, p, "{{ title }}")
	if got != "KubePodCrashLooping" {
		t.Errorf("a label shadowed a built-in: %q", got)
	}
}

// An alert with no labels must render exactly as before.
func TestNoLabelsKeepsTheOldText(t *testing.T) {
	got := renderNotificationText(map[string]any{},
		map[string]any{"title": "Disk full", "severity": "warning", "alert_group_id": "grp_9", "reason": "escalation"}, "")
	want := "[warning] Disk full\nescalation\nAlert group: grp_9"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// An Alertmanager alert can carry dozens of labels; a message truncated by
// Telegram or an SMS gateway helps nobody.
func TestLabelsLineIsBounded(t *testing.T) {
	labels := map[string]any{}
	for i := 0; i < 60; i++ {
		labels[string(rune('a'+i%26))+strings.Repeat("x", 20)+string(rune('0'+i%10))] = strings.Repeat("v", 20)
	}
	got := renderNotificationText(map[string]any{}, map[string]any{"title": "t", "labels": labels}, "")
	if len(got) > maxDefaultLabelChars+200 {
		t.Errorf("labels line unbounded: %d chars", len(got))
	}
	if !strings.Contains(got, "…") {
		t.Error("truncation is not signalled to the reader")
	}
}
