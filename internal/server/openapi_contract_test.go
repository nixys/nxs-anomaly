package server

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// This file is the BETA-042 contract/drift gate between docs/openapi.json and the
// real router. It gates three directions:
//
//   - spec → code: every documented route is dispatched by the router;
//   - code → spec: every /api/v1 route literal has a documented path, matched
//     segment by segment rather than by loose prefix;
//   - code → spec (nested): every suffix-matched sub-resource (".../duty-on",
//     ".../timeline", …) is the last segment of some documented path. The route
//     literal regexp cannot see these — the literal is "/duty-on", not a path —
//     so without this direction a nested route could ship undocumented.
//
// It needs no network, no PostgreSQL and no new dependency.

const openapiPath = "../../docs/openapi.json"

type openapiDoc struct {
	Paths map[string]map[string]json.RawMessage `json:"paths"`
}

func loadOpenAPI(t *testing.T) openapiDoc {
	t.Helper()
	raw, err := os.ReadFile(openapiPath)
	if err != nil {
		t.Fatalf("read %s: %v", openapiPath, err)
	}
	var doc openapiDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse %s: %v", openapiPath, err)
	}
	if len(doc.Paths) == 0 {
		t.Fatal("openapi.json has no paths")
	}
	return doc
}

var httpMethods = map[string]bool{"get": true, "post": true, "put": true, "patch": true, "delete": true}

// routesHandledBeforeRouteAPI are dispatched by handleAPI/session/oidc before the
// routeAPI switch, so srv.do (which calls routeAPI directly) cannot reach them.
// They have their own tests (session_test.go, oidc_test.go).
var routesHandledBeforeRouteAPI = map[string]bool{
	"POST /api/v1/auth/login":        true,
	"POST /api/v1/auth/logout":       true,
	"GET /api/v1/auth/methods":       true,
	"GET /api/v1/auth/oidc/login":    true,
	"GET /api/v1/auth/oidc/callback": true,
}

// TestOpenAPISpecRoutesAreReachable is the contract test for every documented
// route: the router must dispatch it (i.e. not fall through to the default "route
// not found").
//
// A panic is a failure, not a pass. An earlier version of this gate counted a
// panicking handler as "reachable" on the theory that the handler had at least
// run; that made the strongest possible signal of a broken route — a crash —
// indistinguishable from success. No documented route panics on a bare request
// today, so nothing needs the amnesty.
func TestOpenAPISpecRoutesAreReachable(t *testing.T) {
	doc := loadOpenAPI(t)
	srv, _ := newTestServer()

	for path, ops := range doc.Paths {
		for method := range ops {
			if !httpMethods[method] {
				continue // "parameters" and other non-operation keys
			}
			m := strings.ToUpper(method)
			if routesHandledBeforeRouteAPI[m+" "+path] {
				continue
			}
			concrete := regexp.MustCompile(`\{[^}]+\}`).ReplaceAllString(path, "x")
			body := ""
			if m == http.MethodPost || m == http.MethodPut || m == http.MethodPatch {
				body = "{}"
			}
			reachable, panicked := callReachable(srv, m, concrete, body)
			switch {
			case panicked != nil:
				t.Errorf("documented route %s %s panicked: %v\n"+
					"A route in the spec must survive a bare request. If it genuinely needs "+
					"an auth context, give it a real handler test and keep it out of this loop.",
					m, path, panicked)
			case !reachable:
				t.Errorf("documented route %s %s is not reachable (router returned the default 'route not found')", m, path)
			}
		}
	}
}

// callReachable reports whether routeAPI dispatched the request instead of hitting
// the default 404, and separately whether the handler panicked.
func callReachable(srv *Server, method, path, body string) (reachable bool, panicked any) {
	defer func() {
		if rec := recover(); rec != nil {
			panicked = rec
		}
	}()
	w := srv.do(method, path, body)
	// Only the router's fall-through writes this exact error; an entity-level 404
	// carries the engine's own message.
	if w.Code == http.StatusNotFound && strings.Contains(w.Body.String(), "route not found") {
		return false, nil
	}
	return true, nil
}

// routerSources returns the package's own source files. It is a glob rather
// than a hand-kept list so that a newly added router file is gated the moment
// it appears, and so that a file that is absent from a build cannot leave the
// gate pointing at a path that no longer exists.
func routerSources(t *testing.T) []string {
	t.Helper()
	all, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob package sources: %v", err)
	}
	var out []string
	for _, f := range all {
		if !strings.HasSuffix(f, "_test.go") {
			out = append(out, f)
		}
	}
	if len(out) == 0 {
		t.Fatal("no package sources found; the gate would pass vacuously")
	}
	return out
}

// prefixCheck matches a literal used as an authorization or routing *prefix*
// rather than as a route of its own: "/api/v1/alert-groups/bulk-" selects a
// family of routes by partial segment, which is meaningful for authorization
// and meaningless for routing. Dropping these is what lets the glob above take
// in auth.go without mistaking its scope rules for routes;
// TestAuthzScopesCoverDocumentedPaths is the gate that does hold them honest.
var prefixCheck = regexp.MustCompile(`(?:strings\.HasPrefix|hasPrefixPath)\(\s*path,\s*"[^"]*"\)`)

// readCode returns the file with pure-comment lines dropped, so example paths in
// prose (e.g. an "/api/v1/usersearch does not match" note) are not mistaken for
// routes.
func readCode(t *testing.T, file string) string {
	t.Helper()
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("read %s: %v", file, err)
	}
	var code strings.Builder
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "//") {
			continue
		}
		code.WriteString(line)
		code.WriteByte('\n')
	}
	return code.String()
}

// TestOpenAPICoversEveryRouteLiteral is the code → spec direction: every /api/v1
// route literal in the router source must be covered by a documented path, so a
// newly added route cannot ship without a spec entry.
func TestOpenAPICoversEveryRouteLiteral(t *testing.T) {
	doc := loadOpenAPI(t)

	var specPaths [][]string
	for path := range doc.Paths {
		specPaths = append(specPaths, segments(path))
	}

	lit := regexp.MustCompile(`"(/api/v1/[a-zA-Z0-9/_-]*)"`)
	for _, file := range routerSources(t) {
		code := prefixCheck.ReplaceAllString(readCode(t, file), "")
		for _, m := range lit.FindAllStringSubmatch(code, -1) {
			literal := m[1]
			trimmed := strings.TrimRight(literal, "/")
			if trimmed == "/api/v1" || trimmed == "/api/v1/auth" || trimmed == "/api/v1/auth/oidc" {
				continue // bare grouping prefixes, not routes themselves
			}
			if !coveredBySpec(literal, specPaths) {
				t.Errorf("route literal %q found in %s has no matching path in docs/openapi.json", literal, file)
			}
		}
	}
}

// TestOpenAPICoversEverySuffixRoute is the nested-route direction. Sub-resources
// are routed by suffix (`strings.HasSuffix(path, "/timeline")`), so their route
// literal is a bare suffix that the /api/v1 regexp above never sees. Requiring
// each suffix to end some documented path closes that blind spot.
func TestOpenAPICoversEverySuffixRoute(t *testing.T) {
	doc := loadOpenAPI(t)

	documented := map[string]bool{}
	for path := range doc.Paths {
		seg := segments(path)
		last := seg[len(seg)-1]
		if !strings.HasPrefix(last, "{") {
			documented["/"+last] = true
		}
	}

	suffixLit := regexp.MustCompile(`HasSuffix\(path, "(/[a-zA-Z0-9/_-]+)"\)`)
	for _, file := range routerSources(t) {
		for _, m := range suffixLit.FindAllStringSubmatch(readCode(t, file), -1) {
			if !documented[m[1]] {
				t.Errorf("sub-resource suffix %q routed in %s ends no documented path in docs/openapi.json", m[1], file)
			}
		}
	}
}

// TestAuthzScopesCoverDocumentedPaths keeps requiredAction/isRespondPath honest.
// Those rules select a permission by path prefix, so a typo or a renamed
// resource turns into a silently dead rule — and a dead rule usually means the
// path falls through to a weaker default. Every /api/v1 prefix in auth.go must
// therefore still lead somewhere documented.
//
// Prefixes here are matched as plain strings, not segments: "/api/v1/
// alert-groups/bulk-" intentionally covers a family of routes by partial
// segment, which is meaningful for authorization and meaningless for routing.
func TestAuthzScopesCoverDocumentedPaths(t *testing.T) {
	doc := loadOpenAPI(t)

	lit := regexp.MustCompile(`"(/api/v1/[a-zA-Z0-9/_-]*)"`)
	for _, m := range lit.FindAllStringSubmatch(readCode(t, "auth.go"), -1) {
		prefix := m[1]
		covered := false
		for path := range doc.Paths {
			if strings.HasPrefix(path, prefix) {
				covered = true
				break
			}
		}
		if !covered {
			t.Errorf("authorization scope %q in auth.go matches no documented path in docs/openapi.json "+
				"(dead rule: requests it was meant to cover fall through to a weaker default)", prefix)
		}
	}
}

// segments splits a path into its non-empty segments.
func segments(path string) []string {
	var out []string
	for _, s := range strings.Split(path, "/") {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

// coveredBySpec reports whether a source route literal matches a documented path.
//
// A literal is used by the router in one of two ways, and each gets its own rule:
//
//   - exact match (`path == "/api/v1/users"`): it must equal a documented path
//     segment for segment;
//   - prefix match (`strings.HasPrefix(path, "/api/v1/users/")`, always written
//     with a trailing slash): some documented path must extend it by at least one
//     more segment.
//
// Route literals in the source are always fully concrete — the router never
// writes a "{param}" — so every compared segment must match the spec's segment
// literally. A spec "{param}" in a compared position therefore does NOT match:
// that means the literal is a static route sitting at the same depth as a
// documented path parameter (e.g. "/api/v1/users/secrets" against
// "/api/v1/users/{id}"), which is a distinct route the spec does not describe.
//
// The earlier version accepted any literal that merely started with a documented
// static prefix, which let an undocumented "/api/v1/users/<anything>" pass
// because "/api/v1/users" was documented — exactly the nested-route blind spot
// this closes.
func coveredBySpec(literal string, specPaths [][]string) bool {
	isPrefix := strings.HasSuffix(literal, "/")
	lit := segments(literal)
	for _, spec := range specPaths {
		if isPrefix {
			if len(spec) <= len(lit) {
				continue
			}
		} else if len(spec) != len(lit) {
			continue
		}
		if segmentsMatch(lit, spec[:len(lit)]) {
			return true
		}
	}
	return false
}

// segmentsMatch reports whether every literal segment equals the spec segment in
// the same position. A "{param}" spec segment never matches (see coveredBySpec).
func segmentsMatch(lit, spec []string) bool {
	for i := range lit {
		if lit[i] != spec[i] {
			return false
		}
	}
	return true
}

// TestOpenAPIContractGateRejectsUndocumentedNesting guards the gate itself: the
// matching rules must actually reject the shapes they claim to reject, otherwise
// a loosened matcher would silently pass everything.
func TestOpenAPIContractGateRejectsUndocumentedNesting(t *testing.T) {
	spec := [][]string{
		segments("/api/v1/users"),
		segments("/api/v1/users/{id}"),
		segments("/api/v1/users/{id}/duty-on"),
	}
	cases := []struct {
		literal string
		want    bool
		why     string
	}{
		{"/api/v1/users", true, "exact documented path"},
		{"/api/v1/users/", true, "prefix extended by /users/{id}"},
		{"/api/v1/users/secrets", false, "static route shadowing the documented {id} at the same depth"},
		{"/api/v1/users/x/duty-off", false, "undocumented sub-resource under a documented parent"},
		{"/api/v1/usersearch", false, "shares a textual prefix but not a segment"},
		{"/api/v1/teams", false, "not documented at all"},
		{"/api/v1/teams/", false, "prefix of nothing documented"},
	}
	for _, c := range cases {
		if got := coveredBySpec(c.literal, spec); got != c.want {
			t.Errorf("coveredBySpec(%q) = %v, want %v (%s)", c.literal, got, c.want, c.why)
		}
	}
}
