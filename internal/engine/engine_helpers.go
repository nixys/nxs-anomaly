package engine

import (
	"context"
	"crypto/sha256"
	"fmt"
	"log/slog"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"text/template"
	"time"

	"github.com/nixys/nxs-anomaly/internal/store"
	"github.com/nixys/nxs-anomaly/internal/utils"
)

// --- misc helpers ---

func intFromAny(v any, def int) int {
	if v == nil {
		return def
	}
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	case string:
		if i, err := strconv.Atoi(n); err == nil {
			return i
		}
	}
	return def
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func strOrEmpty(m map[string]any, key string) string {
	return utils.StrVal(m, key)
}

func nilOrStr(m map[string]any, key string) any {
	v := utils.StrVal(m, key)
	if v == "" {
		return nil
	}
	return v
}

func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// nilIfUnsetTime is nilIfEmpty for timestamps that sources also mark unset with
// Go's zero time (Alertmanager and Grafana send endsAt "0001-01-01T00:00:00Z"
// for a firing alert).
func nilIfUnsetTime(s string) any {
	if strings.HasPrefix(s, "0001-01-01") {
		return nil
	}
	return nilIfEmpty(s)
}

// labelSetFingerprint identifies an alert by its labels, independent of order.
func labelSetFingerprint(labels map[string]string) string {
	if len(labels) == 0 {
		return ""
	}
	names := make([]string, 0, len(labels))
	for name := range labels {
		names = append(names, name)
	}
	sort.Strings(names)
	h := sha256.New()
	for _, name := range names {
		h.Write([]byte(name))
		h.Write([]byte{0})
		h.Write([]byte(labels[name]))
		h.Write([]byte{0})
	}
	return fmt.Sprintf("%x", h.Sum(nil))[:16]
}

func copyStringMap(m map[string]string) map[string]string {
	out := make(map[string]string, len(m)+1)
	for k, v := range m {
		out[k] = v
	}
	return out
}

func strDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func toAnySlice(ss []string) []any {
	out := make([]any, len(ss))
	for i, v := range ss {
		out[i] = v
	}
	return out
}

// anyList reads a JSONB list field, yielding an empty slice for a missing or
// wrongly typed value so callers can range over it unconditionally.
func anyList(v any) []any {
	list, _ := v.([]any)
	return list
}

func copyMap(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func sortStrings(ss []string) {
	sort.Strings(ss)
}

// SetReferenceCacheTTL aligns reference-cache staleness with the worker poll
// interval: the TTL bounds how long writes from other replicas stay invisible,
// which only needs to hold until the next worker tick. No-op for non-positive
// durations or engines without a cache (unit tests).
func (e *Engine) SetReferenceCacheTTL(d time.Duration) {
	if e.refCache != nil && d > 0 {
		e.refCache.setTTL(d)
	}
}

// wakeWorker nudges worker loops over LISTEN/NOTIFY after an operation that
// scheduled new escalation/delivery work (ingest, group reopen). Best-effort:
// on failure the worker picks the work up on its next poll tick.
func (e *Engine) wakeWorker(ctx context.Context) {
	if err := e.store.NotifyWake(ctx); err != nil {
		slog.Debug("notify_wake_failed", "error", err)
	}
}

// loadItems builds a LoadSpec list selecting only the named rows of a
// collection, so single-entity mutators don't load whole tables under lock.
func loadItems(collection string, ids ...string) []store.LoadSpec {
	vals := make([]any, len(ids))
	for i, id := range ids {
		vals[i] = id
	}
	return []store.LoadSpec{{Collection: collection, Filters: map[string]any{"id": vals}}}
}

func setKeys(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	return out
}

// parsedTemplateCache caches parse results keyed by normalized template
// source. Entries are never evicted: the set of distinct templates is bounded
// by integration/step configuration. A nil inner template records a parse
// failure so broken templates don't re-parse on every notification.
var parsedTemplateCache sync.Map // string → *cachedTemplate

type cachedTemplate struct {
	t *template.Template
	// execWarn keeps a template that fails at execution time from logging on
	// every single notification it renders.
	execWarn sync.Once
}

// templateKeywords are text/template actions that placeholder normalization
// must leave untouched.
var templateKeywords = map[string]bool{
	"if": true, "else": true, "end": true, "range": true, "with": true,
	"template": true, "block": true, "define": true,
	"nil": true, "true": true, "false": true,
}

var barePlaceholderRe = regexp.MustCompile(`\{\{\s*([A-Za-z_][A-Za-z0-9_]*)\s*\}\}`)

// normalizeTemplatePlaceholders rewrites legacy bare placeholders
// ("{{ title }}", "{{title}}") to dotted text/template form ("{{.title}}").
func normalizeTemplatePlaceholders(tmpl string) string {
	return barePlaceholderRe.ReplaceAllStringFunc(tmpl, func(m string) string {
		name := strings.TrimSpace(m[2 : len(m)-2])
		if templateKeywords[name] {
			return m
		}
		return "{{." + name + "}}"
	})
}

// templateInitialisms are the trailing/leading word forms that Go-style
// CamelCase aliases spell in all caps, so "group_id" also answers to "GroupID".
var templateInitialisms = map[string]string{
	"id": "ID", "url": "URL", "uri": "URI", "api": "API", "ip": "IP",
}

// camelAlias converts a snake_case context key to its CamelCase spelling
// ("group_id" → "GroupID"). Returns "" when the key is already camel or has
// nothing to convert.
func camelAlias(key string) string {
	if key == "" || strings.ToLower(key) != key {
		return ""
	}
	var sb strings.Builder
	for _, part := range strings.Split(key, "_") {
		if part == "" {
			continue
		}
		if up, ok := templateInitialisms[part]; ok {
			sb.WriteString(up)
			continue
		}
		sb.WriteString(strings.ToUpper(part[:1]))
		sb.WriteString(part[1:])
	}
	alias := sb.String()
	if alias == key {
		return ""
	}
	return alias
}

// expandTemplateContext adds CamelCase aliases for snake_case keys. Templates
// written as "{{ .Severity }}" or "{{ .GroupID }}" — the spelling the
// Terraform provider and module examples have always shown — must render the
// same values as the canonical "{{ .severity }}" / "{{ .group_id }}" form.
// Existing keys are never overwritten.
func expandTemplateContext(ctx map[string]any) map[string]any {
	out := make(map[string]any, len(ctx)*2)
	for k, v := range ctx {
		out[k] = v
	}
	for k, v := range ctx {
		if alias := camelAlias(k); alias != "" {
			if _, exists := out[alias]; !exists {
				out[alias] = v
			}
		}
	}
	return out
}

// renderTemplate renders tmpl with text/template. Context values are
// stringified the same way the legacy renderer did (fmt %v). Templates that
// fail to parse or that reference keys missing from ctx fall back to the
// legacy single-pass replacement, which leaves unknown placeholders intact;
// both failures are logged, because a silent fallback ships raw "{{ ... }}"
// text to the notification channel with nothing in the logs to explain it.
// Unlike the legacy renderer, text/template substitutes values verbatim:
// placeholder-like text inside an alert title can no longer be re-expanded
// into another context value.
func renderTemplate(tmpl string, ctx map[string]any) string {
	if !strings.Contains(tmpl, "{{") {
		return tmpl
	}
	ctx = expandTemplateContext(ctx)
	normalized := normalizeTemplatePlaceholders(tmpl)
	var entry *cachedTemplate
	if cached, ok := parsedTemplateCache.Load(normalized); ok {
		entry = cached.(*cachedTemplate)
	} else {
		parsed, err := template.New("notification").Option("missingkey=error").Parse(normalized)
		if err != nil {
			slog.Warn("notification_template_parse_failed",
				"template", truncateForLog(tmpl), "error", err)
			parsed = nil
		}
		entry = &cachedTemplate{t: parsed}
		parsedTemplateCache.Store(normalized, entry)
	}
	if entry.t == nil {
		return legacyRenderTemplate(tmpl, ctx)
	}
	strCtx := make(map[string]string, len(ctx))
	for k, v := range ctx {
		strCtx[k] = fmt.Sprintf("%v", v)
	}
	var sb strings.Builder
	if err := entry.t.Execute(&sb, strCtx); err != nil {
		entry.execWarn.Do(func() {
			slog.Warn("notification_template_render_failed",
				"template", truncateForLog(tmpl), "error", err,
				// Listing the keys is the whole value of this line: the render
				// already fell back and shipped raw "{{ ... }}" text, so the
				// reader needs to know what they could have written instead.
				"hint", "unknown placeholder; supported keys are title, severity, reason, group_id, status, "+
					"user_name, user_username, labels, and label_<name> for each label on the alert "+
					"(non-alphanumeric characters become underscores: kubernetes.io/name is label_kubernetes_io_name)")
		})
		return legacyRenderTemplate(tmpl, ctx)
	}
	return sb.String()
}

// truncateForLog keeps a template excerpt short enough for a log line.
func truncateForLog(s string) string {
	const limit = 200
	if len(s) <= limit {
		return s
	}
	return s[:limit] + "…"
}

// legacyRenderTemplate is the historical sequential string replacement,
// kept as the fallback for templates text/template cannot handle.
func legacyRenderTemplate(tmpl string, ctx map[string]any) string {
	for k, v := range ctx {
		repl := fmt.Sprintf("%v", v)
		tmpl = strings.ReplaceAll(tmpl, "{{ "+k+" }}", repl)
		tmpl = strings.ReplaceAll(tmpl, "{{"+k+"}}", repl)
		tmpl = strings.ReplaceAll(tmpl, "{{ ."+k+" }}", repl)
		tmpl = strings.ReplaceAll(tmpl, "{{."+k+"}}", repl)
	}
	return tmpl
}

func pickFirst(values []string, def string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return def
}

func anyToStringSlice(raw any) []string {
	list, _ := raw.([]any)
	var out []string
	for _, v := range list {
		out = append(out, fmt.Sprintf("%v", v))
	}
	return out
}

// softDeleted reports whether a row carries a deletion timestamp. Collections
// without soft delete never have the field, so this is false for them.
func softDeleted(item map[string]any) bool {
	v, ok := item["deleted_at"]
	if !ok || v == nil {
		return false
	}
	return utils.StrVal(item, "deleted_at") != ""
}
