package engine

import (
	"fmt"
	"log/slog"
	"regexp"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/nixys/nxs-anomaly/internal/utils"
)

// An ingest pipeline rewrites an alert after its route is chosen and before
// anything else is decided about it.
//
// Routing deliberately stays upstream. Enrichment is added constantly and by
// many hands — a team label for a dashboard, a namespace pulled out of a title —
// and if it fed route selection, every one of those edits would be a change to
// which rota gets woken, made by someone who was not thinking about paging. So
// the route follows from the alert as the source sent it, and reading an
// integration's routes tells the whole story of where its alerts go.
//
// Everything downstream does see the enriched alert, which is where the useful
// work is: the dedupe key and therefore grouping, the stored alert and its
// labels, and the notification text. Cutting a build id out of a title so that
// repeated failures collapse into one incident still works; adding the pod and
// namespace a source failed to state still reaches the message and the UI.
//
// The one thing this ordering gives up: a rule cannot repoint an alert at a
// different escalation chain. That is the point of it.
//
// The vocabulary is a deliberately small subset of what a log pipeline offers.
// Each stage is one action with an optional condition, which keeps a rule
// readable in a config review and keeps the failure modes countable:
//
//	extract   pull named groups out of a field into labels   (grok, roughly)
//	set       add or overwrite labels
//	rename    move a label to another name
//	remove    delete labels
//	gsub      rewrite a field with a regular expression
//	truncate  bound a field's length
//	drop      discard the alert entirely
//
// Fields a stage may address are title, message, severity and label:<name>.
// There is no arithmetic, no date parsing and no lookups: those belong to
// whatever produced the alert.

// maxPipelineStages bounds a single integration's pipeline. The stages run on
// every alert of that integration, inside the ingest hot path, so the length is
// a latency budget rather than a matter of taste.
const maxPipelineStages = 32

// maxPatternLen bounds a single regular expression. Go's regexp is RE2 and has
// no catastrophic backtracking, so this is about keeping compilation and the
// config itself sane rather than about ReDoS.
const maxPatternLen = 512

// maxFieldLen is the largest input a regex stage will look at. RE2 is linear in
// the input, so a very long title costs time proportional to its length on every
// alert; past this the field is left alone and the stage is counted as skipped.
const maxFieldLen = 16 * 1024

// maxExtractedLabels bounds how many labels one extract stage may add, so a
// pattern with many groups cannot inflate every alert of a noisy integration.
const maxExtractedLabels = 32

// pipelineRegexCache holds compiled patterns across alerts. Same reasoning as
// parsedTemplateCache: the pipeline is per integration and stable, so compiling
// per alert would be pure waste on the hottest path in the service.
var pipelineRegexCache sync.Map // string -> *cachedRegex

type cachedRegex struct {
	re  *regexp.Regexp
	err error
}

func compilePipelineRegex(pattern string) (*regexp.Regexp, error) {
	if v, ok := pipelineRegexCache.Load(pattern); ok {
		c := v.(*cachedRegex)
		return c.re, c.err
	}
	var c cachedRegex
	if len(pattern) > maxPatternLen {
		c.err = fmt.Errorf("pattern longer than %d characters", maxPatternLen)
	} else {
		c.re, c.err = regexp.Compile(pattern)
	}
	pipelineRegexCache.Store(pattern, &c)
	return c.re, c.err
}

// pipelineField names something a stage can read or write.
type pipelineField struct {
	label string // non-empty for label:<name>
	name  string // title | message | severity, when label is empty
}

var pipelineScalarFields = map[string]bool{"title": true, "message": true, "severity": true}

func parsePipelineField(s string) (pipelineField, error) {
	s = strings.TrimSpace(s)
	if rest, ok := strings.CutPrefix(s, "label:"); ok {
		rest = strings.TrimSpace(rest)
		if rest == "" {
			return pipelineField{}, fmt.Errorf("label: needs a name")
		}
		return pipelineField{label: rest}, nil
	}
	if pipelineScalarFields[s] {
		return pipelineField{name: s}, nil
	}
	return pipelineField{}, fmt.Errorf("unknown field %q: use title, message, severity or label:<name>", s)
}

func (f pipelineField) String() string {
	if f.label != "" {
		return "label:" + f.label
	}
	return f.name
}

// pipelineDoc is the alert as the pipeline sees it: the scalar fields it may
// rewrite plus the labels. Nothing else is exposed, so a stage cannot reach the
// raw payload and invent structure the rest of ingest does not expect.
type pipelineDoc struct {
	title    string
	message  string
	severity string
	labels   map[string]string
}

func (d *pipelineDoc) get(f pipelineField) (string, bool) {
	if f.label != "" {
		v, ok := d.labels[f.label]
		return v, ok
	}
	switch f.name {
	case "title":
		return d.title, true
	case "message":
		return d.message, true
	case "severity":
		return d.severity, true
	}
	return "", false
}

func (d *pipelineDoc) set(f pipelineField, v string) {
	if f.label != "" {
		d.labels[f.label] = v
		return
	}
	switch f.name {
	case "title":
		d.title = v
	case "message":
		d.message = v
	case "severity":
		d.severity = v
	}
}

// pipelineCond is the optional "if" on a stage.
type pipelineCond struct {
	field   pipelineField
	equals  *string
	matches *regexp.Regexp
	exists  *bool
}

func (c *pipelineCond) eval(d *pipelineDoc) bool {
	if c == nil {
		return true
	}
	v, present := d.get(c.field)
	switch {
	case c.exists != nil:
		return present == *c.exists
	case c.equals != nil:
		return present && v == *c.equals
	case c.matches != nil:
		return present && len(v) <= maxFieldLen && c.matches.MatchString(v)
	}
	return true
}

// pipelineStage is one action with an optional condition.
type pipelineStage struct {
	cond *pipelineCond
	kind string

	// extract
	from    pipelineField
	pattern *regexp.Regexp

	// set / rename
	pairs map[string]string

	// remove
	names []string

	// gsub / truncate
	field   pipelineField
	replace string
	max     int
}

// AlertPipeline is a compiled, ready-to-run pipeline.
type AlertPipeline struct {
	stages []pipelineStage
}

// Len reports the number of stages, so callers can tell "no pipeline" from
// "a pipeline that does nothing".
func (p *AlertPipeline) Len() int {
	if p == nil {
		return 0
	}
	return len(p.stages)
}

// CompileAlertPipeline validates and compiles the integration's pipeline
// definition. It is called both when an integration is saved — so a bad rule is
// a 400 at configuration time rather than a surprise at 3am — and lazily on
// ingest, where the result is cached.
func CompileAlertPipeline(raw any) (*AlertPipeline, error) {
	if raw == nil {
		return nil, nil
	}
	list, ok := raw.([]any)
	if !ok {
		return nil, errValidation("pipeline must be a list of stages")
	}
	if len(list) > maxPipelineStages {
		return nil, errValidation(fmt.Sprintf("pipeline has %d stages, the limit is %d", len(list), maxPipelineStages))
	}
	p := &AlertPipeline{}
	for i, item := range list {
		m, ok := item.(map[string]any)
		if !ok {
			return nil, errValidation(fmt.Sprintf("pipeline stage %d must be an object", i+1))
		}
		st, err := compileStage(m)
		if err != nil {
			return nil, errValidation(fmt.Sprintf("pipeline stage %d: %s", i+1, err.Error()))
		}
		p.stages = append(p.stages, st)
	}
	if len(p.stages) == 0 {
		return nil, nil
	}
	return p, nil
}

var pipelineActions = []string{"extract", "set", "rename", "remove", "gsub", "truncate", "drop"}

func compileStage(m map[string]any) (pipelineStage, error) {
	var st pipelineStage

	if rawCond, ok := m["if"]; ok && rawCond != nil {
		c, err := compileCond(rawCond)
		if err != nil {
			return st, err
		}
		st.cond = c
	}

	// Exactly one action. Two actions in one stage would make the order between
	// them invisible in the config, and order is the whole semantics here.
	var found []string
	for _, a := range pipelineActions {
		if v, ok := m[a]; ok && v != nil {
			found = append(found, a)
		}
	}
	sort.Strings(found)
	if len(found) == 0 {
		return st, fmt.Errorf("no action: expected one of %s", strings.Join(pipelineActions, ", "))
	}
	if len(found) > 1 {
		return st, fmt.Errorf("stage has %d actions (%s); split them into separate stages so their order is explicit",
			len(found), strings.Join(found, ", "))
	}
	st.kind = found[0]

	switch st.kind {
	case "extract":
		spec, ok := m["extract"].(map[string]any)
		if !ok {
			return st, fmt.Errorf("extract must be an object with from and pattern")
		}
		f, err := parsePipelineField(strDefault(utils.StrVal(spec, "from"), "title"))
		if err != nil {
			return st, fmt.Errorf("extract.from: %w", err)
		}
		st.from = f
		pat := utils.StrVal(spec, "pattern")
		if pat == "" {
			return st, fmt.Errorf("extract.pattern is required")
		}
		re, err := compilePipelineRegex(pat)
		if err != nil {
			return st, fmt.Errorf("extract.pattern: %w", err)
		}
		named := 0
		for _, n := range re.SubexpNames() {
			if n != "" {
				named++
			}
		}
		if named == 0 {
			return st, fmt.Errorf("extract.pattern has no named groups: use (?P<name>...) to say which label to set")
		}
		if named > maxExtractedLabels {
			return st, fmt.Errorf("extract.pattern has %d named groups, the limit is %d", named, maxExtractedLabels)
		}
		st.pattern = re

	case "set", "rename":
		spec, ok := m[st.kind].(map[string]any)
		if !ok {
			return st, fmt.Errorf("%s must be an object of label pairs", st.kind)
		}
		if len(spec) == 0 {
			return st, fmt.Errorf("%s is empty", st.kind)
		}
		st.pairs = make(map[string]string, len(spec))
		for k, v := range spec {
			// Both spellings: these stages take label names, but the rest of a
			// pipeline addresses labels as label:<name>, and someone writing
			// "label:severity" here used to get a label literally called that.
			name := strings.TrimPrefix(strings.TrimSpace(k), "label:")
			if name == "" {
				return st, fmt.Errorf("%s has an empty label name", st.kind)
			}
			st.pairs[name] = fmt.Sprintf("%v", v)
		}

	case "remove":
		names, err := utils.CoerceStringList(m["remove"])
		for i, n := range names {
			names[i] = strings.TrimPrefix(strings.TrimSpace(n), "label:")
		}
		if err != nil || len(names) == 0 {
			return st, fmt.Errorf("remove must be a non-empty list of label names")
		}
		st.names = names

	case "gsub":
		spec, ok := m["gsub"].(map[string]any)
		if !ok {
			return st, fmt.Errorf("gsub must be an object with field, pattern and replace")
		}
		f, err := parsePipelineField(strDefault(utils.StrVal(spec, "field"), "title"))
		if err != nil {
			return st, fmt.Errorf("gsub.field: %w", err)
		}
		st.field = f
		pat := utils.StrVal(spec, "pattern")
		if pat == "" {
			return st, fmt.Errorf("gsub.pattern is required")
		}
		re, err := compilePipelineRegex(pat)
		if err != nil {
			return st, fmt.Errorf("gsub.pattern: %w", err)
		}
		st.pattern = re
		st.replace = utils.StrVal(spec, "replace")

	case "truncate":
		spec, ok := m["truncate"].(map[string]any)
		if !ok {
			return st, fmt.Errorf("truncate must be an object with field and max")
		}
		f, err := parsePipelineField(strDefault(utils.StrVal(spec, "field"), "title"))
		if err != nil {
			return st, fmt.Errorf("truncate.field: %w", err)
		}
		st.field = f
		st.max = utils.IntVal(spec, "max")
		if st.max <= 0 {
			return st, fmt.Errorf("truncate.max must be a positive number")
		}

	case "drop":
		// `drop: true` with no condition discards every alert of the
		// integration. That is almost certainly a mistake, and a silent one:
		// the integration keeps answering 202 and nothing is ever paged.
		if st.cond == nil {
			return st, fmt.Errorf("drop needs an `if`: an unconditional drop discards every alert of this integration")
		}
	}
	return st, nil
}

func compileCond(raw any) (*pipelineCond, error) {
	m, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("if must be an object")
	}
	f, err := parsePipelineField(strDefault(utils.StrVal(m, "field"), "title"))
	if err != nil {
		return nil, fmt.Errorf("if.field: %w", err)
	}
	c := &pipelineCond{field: f}
	n := 0
	if v, ok := m["equals"]; ok && v != nil {
		s := fmt.Sprintf("%v", v)
		c.equals = &s
		n++
	}
	if v, ok := m["matches"]; ok && v != nil {
		re, err := compilePipelineRegex(fmt.Sprintf("%v", v))
		if err != nil {
			return nil, fmt.Errorf("if.matches: %w", err)
		}
		c.matches = re
		n++
	}
	if v, ok := m["exists"]; ok && v != nil {
		b := utils.BoolVal(m, "exists", true)
		c.exists = &b
		n++
	}
	if n == 0 {
		return nil, fmt.Errorf("if needs one of equals, matches or exists")
	}
	if n > 1 {
		return nil, fmt.Errorf("if has more than one test; use separate stages")
	}
	return c, nil
}

// PipelineResult reports what the pipeline did, for the debug endpoint and for
// the metric. Steps names the stages that changed something, so a rule that
// never fires is visible as such rather than as an absence.
type PipelineResult struct {
	Dropped bool
	Steps   []string
}

// Apply runs the pipeline over the document in place.
//
// It never returns an error. A pipeline is operator-authored configuration on
// the hottest path in the service, and an alert that is not delivered because a
// rule was wrong is worse than an alert delivered unenriched — the first is a
// missed incident, the second is a cosmetic problem. So a stage that cannot run
// is skipped, logged once with the integration it came from, and counted.
func (p *AlertPipeline) Apply(d *pipelineDoc, integrationID string) PipelineResult {
	var res PipelineResult
	if p == nil {
		return res
	}
	for i, st := range p.stages {
		if !st.cond.eval(d) {
			continue
		}
		changed, err := applyStage(st, d)
		if err != nil {
			slog.Warn("alert_pipeline_stage_skipped",
				"integration_id", integrationID, "stage", i+1, "action", st.kind, "error", err)
			continue
		}
		if st.kind == "drop" {
			res.Dropped = true
			res.Steps = append(res.Steps, fmt.Sprintf("%d:drop", i+1))
			return res
		}
		if changed {
			res.Steps = append(res.Steps, fmt.Sprintf("%d:%s", i+1, st.kind))
		}
	}
	return res
}

func applyStage(st pipelineStage, d *pipelineDoc) (bool, error) {
	switch st.kind {
	case "drop":
		return true, nil

	case "extract":
		v, ok := d.get(st.from)
		if !ok || v == "" {
			return false, nil
		}
		if len(v) > maxFieldLen {
			return false, fmt.Errorf("%s is %d bytes, over the %d limit", st.from, len(v), maxFieldLen)
		}
		m := st.pattern.FindStringSubmatch(v)
		if m == nil {
			return false, nil
		}
		changed := false
		for gi, name := range st.pattern.SubexpNames() {
			if name == "" || gi >= len(m) || m[gi] == "" {
				continue
			}
			// Extraction adds context; it does not overwrite what the source
			// stated. An operator who means to replace a label says so with a
			// later `set`, which reads as the override it is.
			if _, exists := d.labels[name]; exists {
				continue
			}
			d.labels[name] = m[gi]
			changed = true
		}
		return changed, nil

	case "set":
		for k, v := range st.pairs {
			d.labels[k] = v
		}
		return true, nil

	case "rename":
		changed := false
		for from, to := range st.pairs {
			v, ok := d.labels[from]
			if !ok {
				continue
			}
			delete(d.labels, from)
			d.labels[to] = v
			changed = true
		}
		return changed, nil

	case "remove":
		changed := false
		for _, n := range st.names {
			if _, ok := d.labels[n]; ok {
				delete(d.labels, n)
				changed = true
			}
		}
		return changed, nil

	case "gsub":
		v, ok := d.get(st.field)
		if !ok {
			return false, nil
		}
		if len(v) > maxFieldLen {
			return false, fmt.Errorf("%s is %d bytes, over the %d limit", st.field, len(v), maxFieldLen)
		}
		out := st.pattern.ReplaceAllString(v, st.replace)
		if out == v {
			return false, nil
		}
		d.set(st.field, out)
		return true, nil

	case "truncate":
		v, ok := d.get(st.field)
		if !ok || len(v) <= st.max {
			return false, nil
		}
		// Cut on a rune boundary: max counts bytes, and a title sliced
		// mid-character renders as a replacement glyph in every channel it
		// reaches.
		out := v[:st.max]
		for len(out) > 0 && !utf8.ValidString(out) {
			out = out[:len(out)-1]
		}
		d.set(st.field, out)
		return true, nil
	}
	return false, fmt.Errorf("unknown action %q", st.kind)
}

// applyAlertPipeline runs the integration's pipeline over the payload in place
// and reports whether the alert was dropped.
//
// The compiled pipeline is cached per integration under the definition itself,
// so an edit takes effect without a restart and an unchanged integration costs
// one map lookup per alert rather than a recompile.
//
// A pipeline that fails to compile is ignored, loudly. It was validated when the
// integration was saved, so reaching here means the row was written by an older
// version or edited around the API; either way, refusing the alert would turn a
// configuration problem into a missed incident.
func (e *Engine) applyAlertPipeline(integration, payload map[string]any) (bool, error) {
	raw, ok := integration["pipeline"]
	if !ok || raw == nil {
		return false, nil
	}
	p, err := e.compiledPipeline(integration, raw)
	if err != nil {
		slog.Warn("alert_pipeline_ignored",
			"integration_id", utils.StrVal(integration, "id"), "error", err)
		return false, nil
	}
	if p.Len() == 0 {
		return false, nil
	}

	labels, _ := utils.CoerceLabelMap(payload["labels"])
	severityLabelBefore := labels["severity"]
	doc := &pipelineDoc{
		title:    utils.StrVal(payload, "title"),
		message:  utils.StrVal(payload, "message"),
		severity: utils.StrVal(payload, "severity"),
		labels:   labels,
	}
	res := p.Apply(doc, utils.StrVal(integration, "id"))

	if res.Dropped {
		return true, nil
	}
	// Written back only where the pipeline set something, so an alert that
	// carried no title keeps not carrying one rather than gaining an empty
	// string that later code would treat as present.
	if doc.title != "" {
		payload["title"] = doc.title
	}
	if doc.message != "" {
		payload["message"] = doc.message
	}
	if doc.severity != "" {
		payload["severity"] = doc.severity
	}
	// A pipeline that rewrites the severity *label* means the severity, not just
	// the label: the source field it would otherwise lose to is exactly what the
	// rule was written to correct. Without this the group kept the severity the
	// source sent while the label said something else, and the two disagreed
	// everywhere they were shown side by side.
	if after := doc.labels["severity"]; after != "" && after != severityLabelBefore {
		payload["severity"] = after
	}
	payload["labels"] = labelsAny(doc.labels)
	return false, nil
}

// compiledPipeline returns the cached compilation for this integration's
// pipeline definition, compiling it once per distinct definition.
func (e *Engine) compiledPipeline(integration map[string]any, raw any) (*AlertPipeline, error) {
	key := utils.StrVal(integration, "id") + "\x00" + fingerprintPipeline(raw)
	if v, ok := pipelineCompileCache.Load(key); ok {
		c := v.(*cachedPipeline)
		return c.p, c.err
	}
	p, err := CompileAlertPipeline(raw)
	pipelineCompileCache.Store(key, &cachedPipeline{p: p, err: err})
	return p, err
}

type cachedPipeline struct {
	p   *AlertPipeline
	err error
}

var pipelineCompileCache sync.Map // integrationID\x00fingerprint -> *cachedPipeline

// fingerprintPipeline renders the definition deterministically so an edit
// produces a different cache key. Cheap because a pipeline is small and this
// runs once per alert against an already-decoded structure.
func fingerprintPipeline(raw any) string {
	var sb strings.Builder
	writePipelineFingerprint(&sb, raw)
	return sb.String()
}

func writePipelineFingerprint(sb *strings.Builder, v any) {
	switch t := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		sb.WriteByte('{')
		for _, k := range keys {
			sb.WriteString(k)
			sb.WriteByte(':')
			writePipelineFingerprint(sb, t[k])
			sb.WriteByte(',')
		}
		sb.WriteByte('}')
	case []any:
		sb.WriteByte('[')
		for _, item := range t {
			writePipelineFingerprint(sb, item)
			sb.WriteByte(',')
		}
		sb.WriteByte(']')
	default:
		fmt.Fprintf(sb, "%v", v)
	}
}
