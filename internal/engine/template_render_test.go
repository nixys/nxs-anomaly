package engine

import "testing"

func TestRenderTemplatePlaceholderForms(t *testing.T) {
	ctx := map[string]any{"title": "DB down", "severity": "critical"}
	cases := []struct {
		name, tmpl, want string
	}{
		{"bare spaced", "[{{ severity }}] {{ title }}", "[critical] DB down"},
		{"bare tight", "[{{severity}}] {{title}}", "[critical] DB down"},
		{"dotted spaced", "[{{ .severity }}] {{ .title }}", "[critical] DB down"},
		{"dotted tight", "[{{.severity}}] {{.title}}", "[critical] DB down"},
		{"no placeholders", "plain text", "plain text"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := renderTemplate(c.tmpl, ctx); got != c.want {
				t.Fatalf("renderTemplate(%q) = %q, want %q", c.tmpl, got, c.want)
			}
		})
	}
}

func TestRenderTemplateUnknownPlaceholderPreserved(t *testing.T) {
	ctx := map[string]any{"title": "DB down"}
	got := renderTemplate("{{ title }} / {{ unknown_key }}", ctx)
	want := "DB down / {{ unknown_key }}"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestRenderTemplateValueNotReExpanded(t *testing.T) {
	// The legacy sequential ReplaceAll re-expanded placeholder-like text
	// inside already-substituted values; text/template must not.
	ctx := map[string]any{"title": "evil {{ severity }}", "severity": "critical"}
	got := renderTemplate("{{ title }}", ctx)
	want := "evil {{ severity }}"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestRenderTemplateConditional(t *testing.T) {
	ctx := map[string]any{"severity": "critical", "title": "DB down"}
	got := renderTemplate("{{if .severity}}[{{.severity}}] {{end}}{{.title}}", ctx)
	want := "[critical] DB down"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestRenderTemplateNonStringValues(t *testing.T) {
	ctx := map[string]any{"count": 3, "labels": map[string]any{"env": "prod"}}
	got := renderTemplate("{{ count }} {{ labels }}", ctx)
	want := "3 map[env:prod]"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestRenderTemplateBrokenTemplateFallsBack(t *testing.T) {
	// Unparsable template → legacy replacement still substitutes known keys.
	ctx := map[string]any{"title": "DB down"}
	got := renderTemplate("{{ title }} {{if}}", ctx)
	want := "DB down {{if}}"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestRenderTemplateCamelCaseAliases(t *testing.T) {
	// The Terraform provider and module examples spell placeholders in
	// CamelCase; they must resolve to the same snake_case context values
	// instead of falling through to the raw template text.
	ctx := map[string]any{
		"title": "DB down", "severity": "critical", "group_id": "grp_1",
		"user_name": "Ann", "status": "open",
	}
	cases := []struct {
		name, tmpl, want string
	}{
		{"severity and title", "*[{{ .Severity }}]* {{ .Title }}", "*[critical]* DB down"},
		{"initialism", "Group: {{ .GroupID }}", "Group: grp_1"},
		{"multiword", "{{ .UserName }} / {{ .Status }}", "Ann / open"},
		{"mixed with canonical", "{{ .Severity }} {{ .title }}", "critical DB down"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := renderTemplate(c.tmpl, ctx); got != c.want {
				t.Fatalf("renderTemplate(%q) = %q, want %q", c.tmpl, got, c.want)
			}
		})
	}
}

func TestRenderTemplateCamelCaseDoesNotShadowExplicitKey(t *testing.T) {
	ctx := map[string]any{"title": "lower", "Title": "explicit"}
	if got := renderTemplate("{{ .Title }}", ctx); got != "explicit" {
		t.Fatalf("got %q, want %q", got, "explicit")
	}
}

func TestRenderNotificationTextCamelCaseTemplate(t *testing.T) {
	payload := map[string]any{
		"title": "DB down", "severity": "critical", "alert_group_id": "grp_1",
	}
	got := renderNotificationText(map[string]any{}, payload, "🚨 *[{{ .Severity }}]* {{ .Title }}")
	want := "🚨 *[critical]* DB down"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}
