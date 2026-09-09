package server

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/nixys/nxs-anomaly/internal/utils"
)

// server_api.go contains the management API router, all routing helpers,
// request/response utilities, and HTTP middleware.
// The Server struct, lifecycle, metrics, and webhook handlers live in server.go.

func (srv *Server) routeAPI(w http.ResponseWriter, r *http.Request) {
	method := r.Method
	path := r.URL.Path
	ctx := r.Context()

	eng := srv.eng

	switch {
	// Identity. The unauthenticated half (/auth/login, /auth/logout,
	// /auth/methods) is handled before this router — see handleAPI.
	case method == http.MethodGet && path == "/api/v1/auth/me":
		srv.handleMe(w, r)
	case method == http.MethodPut && path == "/api/v1/auth/preferences":
		srv.handleUpdateOwnPreferences(w, r)
	case method == http.MethodPost && path == "/api/v1/auth/password":
		srv.handleChangeOwnPassword(w, r)
	case method == http.MethodGet && path == "/api/v1/auth/sessions":
		srv.handleListSessions(w, r)
	case method == http.MethodDelete && pathDepth(path, "/api/v1/auth/sessions/") == 1:
		srv.handleRevokeSession(w, r, lastSegment(path))
	case method == http.MethodDelete && strings.HasSuffix(path, "/sessions") && strings.HasPrefix(path, "/api/v1/users/"):
		srv.handleRevokeUserSessions(w, r, segment(path, 3))
	case (method == http.MethodPut || method == http.MethodDelete) &&
		strings.HasSuffix(path, "/password") && strings.HasPrefix(path, "/api/v1/users/"):
		srv.handleSetUserPassword(w, r, segment(path, 3))

	// Users
	case method == http.MethodGet && path == "/api/v1/users":
		v, err := eng.ListCollectionPage(ctx, "users", pageParams(r))
		writeResult(w, http.StatusOK, v, err)
	case method == http.MethodPost && path == "/api/v1/users":
		body, ok := readJSON(w, r)
		if !ok {
			return
		}
		v, err := eng.CreateUser(ctx, body)
		writeResult(w, http.StatusCreated, v, err)
	case method == http.MethodGet && pathDepth(path, "/api/v1/users/") == 1:
		v, err := eng.GetItem(ctx, "users", lastSegment(path))
		writeResult(w, http.StatusOK, v, err)
	case method == http.MethodPost && strings.HasSuffix(path, "/duty-on") && strings.HasPrefix(path, "/api/v1/users/"):
		v, err := eng.ToggleUserDuty(ctx, segment(path, 3), true)
		writeResult(w, http.StatusOK, v, err)
	case method == http.MethodPost && strings.HasSuffix(path, "/duty-off") && strings.HasPrefix(path, "/api/v1/users/"):
		v, err := eng.ToggleUserDuty(ctx, segment(path, 3), false)
		writeResult(w, http.StatusOK, v, err)
	case (method == http.MethodPatch || method == http.MethodPut) && pathDepth(path, "/api/v1/users/") == 1:
		body, ok := readJSON(w, r)
		if !ok {
			return
		}
		v, err := eng.UpdateUser(ctx, lastSegment(path), body)
		writeResult(w, http.StatusOK, v, err)
	case method == http.MethodDelete && pathDepth(path, "/api/v1/users/") == 1:
		v, err := eng.DeleteEntity(ctx, "users", lastSegment(path))
		writeResult(w, http.StatusOK, v, err)

	// Personal data: everything held about one person, and its erasure. Both are
	// admin-only (see auth.go) — the export is a GET, and the read floor would
	// otherwise let any authenticated principal dump somebody's paging history,
	// addresses and audit trail.
	case method == http.MethodGet && strings.HasSuffix(path, "/export") && strings.HasPrefix(path, "/api/v1/users/"):
		v, err := eng.ExportUserData(ctx, segment(path, 3))
		writeResult(w, http.StatusOK, v, err)
	case method == http.MethodPost && strings.HasSuffix(path, "/erase") && strings.HasPrefix(path, "/api/v1/users/"):
		v, err := eng.EraseUser(ctx, segment(path, 3))
		writeResult(w, http.StatusOK, v, err)

	// Teams
	case method == http.MethodGet && path == "/api/v1/teams":
		v, err := eng.ListCollectionPage(ctx, "teams", pageParams(r))
		writeResult(w, http.StatusOK, v, err)
	case method == http.MethodPost && path == "/api/v1/teams":
		body, ok := readJSON(w, r)
		if !ok {
			return
		}
		v, err := eng.CreateTeam(ctx, body)
		writeResult(w, http.StatusCreated, v, err)
	case method == http.MethodGet && pathDepth(path, "/api/v1/teams/") == 1:
		v, err := eng.GetItem(ctx, "teams", lastSegment(path))
		writeResult(w, http.StatusOK, v, err)
	case method == http.MethodPut && pathDepth(path, "/api/v1/teams/") == 1:
		body, ok := readJSON(w, r)
		if !ok {
			return
		}
		v, err := eng.UpdateTeam(ctx, lastSegment(path), body)
		writeResult(w, http.StatusOK, v, err)
	case method == http.MethodDelete && pathDepth(path, "/api/v1/teams/") == 1:
		v, err := eng.DeleteEntity(ctx, "teams", lastSegment(path))
		writeResult(w, http.StatusOK, v, err)

	// Prove that mobile push works, or say plainly that it does not: the
	// response carries the real per-device provider verdict.
	case method == http.MethodPost && strings.HasSuffix(path, "/test-push") && strings.HasPrefix(path, "/api/v1/users/"):
		v, err := eng.SendTestPush(ctx, segment(path, 3))
		writeResult(w, http.StatusOK, v, err)

	// Backfill a default personal policy from legacy notification_targets for
	// every user that has targets but no policy yet (BETA-031, idempotent).
	case method == http.MethodPost && path == "/api/v1/users/migrate-notification-policies":
		n, err := eng.MigrateUserTargetsToDefaultPolicy(ctx)
		writeResult(w, http.StatusOK, map[string]any{"migrated": n}, err)

	// Send one test notification through a real provider and report the verdict,
	// so the UI can prove a personal channel is configured (BETA-031).
	case method == http.MethodPost && strings.HasSuffix(path, "/test-notification") && strings.HasPrefix(path, "/api/v1/users/"):
		body, ok := readJSON(w, r)
		if !ok {
			return
		}
		channel, _ := body["channel"].(string)
		v, err := eng.SendTestNotification(ctx, segment(path, 3), channel)
		writeResult(w, http.StatusOK, v, err)

	// Schedules
	case method == http.MethodGet && path == "/api/v1/schedules":
		v, err := eng.ListCollectionPage(ctx, "schedules", pageParams(r))
		writeResult(w, http.StatusOK, v, err)
	case method == http.MethodPost && path == "/api/v1/schedules":
		body, ok := readJSON(w, r)
		if !ok {
			return
		}
		v, err := eng.CreateSchedule(ctx, body)
		writeResult(w, http.StatusCreated, v, err)
	case method == http.MethodGet && path == "/api/v1/on-call":
		// Who is on call right now across every schedule (Schedule v2 resolver,
		// not the manual on_duty flag).
		v, err := eng.CurrentlyOnCall(ctx)
		writeResult(w, http.StatusOK, v, err)
	case method == http.MethodGet && strings.HasSuffix(path, "/on-call") && strings.HasPrefix(path, "/api/v1/schedules/"):
		at := r.URL.Query().Get("at")
		v, err := eng.GetScheduleOncall(ctx, segment(path, 3), at)
		writeResult(w, http.StatusOK, v, err)
	// Standing coverage check across all schedules. Before the {id} routes:
	// "coverage" is not a schedule id.
	case method == http.MethodGet && path == "/api/v1/schedules/coverage":
		v, err := eng.ScheduleCoverageReport(ctx)
		writeResult(w, http.StatusOK, v, err)
	case method == http.MethodGet && strings.HasSuffix(path, "/preview") && strings.HasPrefix(path, "/api/v1/schedules/"):
		q := r.URL.Query()
		v, err := eng.PreviewSchedule(ctx, segment(path, 3), q.Get("from"), q.Get("to"))
		writeResult(w, http.StatusOK, v, err)
	case method == http.MethodPost && (strings.HasSuffix(path, "/override") || strings.HasSuffix(path, "/overrides")) &&
		strings.HasPrefix(path, "/api/v1/schedules/"):
		body, ok := readJSON(w, r)
		if !ok {
			return
		}
		v, err := eng.CreateScheduleOverride(ctx, segment(path, 3), body)
		writeResult(w, http.StatusOK, v, err)
	case (method == http.MethodPut || method == http.MethodPatch) &&
		pathDepth(path, "/api/v1/schedules/") == 3 && segment(path, 4) == "overrides":
		body, ok := readJSON(w, r)
		if !ok {
			return
		}
		v, err := eng.UpdateScheduleOverride(ctx, segment(path, 3), lastSegment(path), body)
		writeResult(w, http.StatusOK, v, err)
	case method == http.MethodDelete && pathDepth(path, "/api/v1/schedules/") == 3 && segment(path, 4) == "overrides":
		v, err := eng.DeleteScheduleOverride(ctx, segment(path, 3), lastSegment(path))
		writeResult(w, http.StatusOK, v, err)
	case method == http.MethodGet && pathDepth(path, "/api/v1/schedules/") == 1:
		v, err := eng.GetItem(ctx, "schedules", lastSegment(path))
		writeResult(w, http.StatusOK, v, err)
	case method == http.MethodPut && pathDepth(path, "/api/v1/schedules/") == 1:
		body, ok := readJSON(w, r)
		if !ok {
			return
		}
		v, err := eng.UpdateSchedule(ctx, lastSegment(path), body)
		writeResult(w, http.StatusOK, v, err)
	case method == http.MethodDelete && pathDepth(path, "/api/v1/schedules/") == 1:
		v, err := eng.DeleteEntity(ctx, "schedules", lastSegment(path))
		writeResult(w, http.StatusOK, v, err)

	// Escalation chains
	case method == http.MethodGet && path == "/api/v1/escalation-chains":
		v, err := eng.ListCollectionPage(ctx, "escalation_chains", pageParams(r))
		writeResult(w, http.StatusOK, v, err)
	case method == http.MethodPost && path == "/api/v1/escalation-chains":
		body, ok := readJSON(w, r)
		if !ok {
			return
		}
		v, err := eng.CreateEscalationChain(ctx, body)
		writeResult(w, http.StatusCreated, v, err)
	case method == http.MethodGet && pathDepth(path, "/api/v1/escalation-chains/") == 1:
		v, err := eng.GetItem(ctx, "escalation_chains", lastSegment(path))
		writeResult(w, http.StatusOK, v, err)
	case method == http.MethodPut && pathDepth(path, "/api/v1/escalation-chains/") == 1:
		body, ok := readJSON(w, r)
		if !ok {
			return
		}
		v, err := eng.UpdateEscalationChain(ctx, lastSegment(path), body)
		writeResult(w, http.StatusOK, v, err)
	case method == http.MethodDelete && pathDepth(path, "/api/v1/escalation-chains/") == 1:
		v, err := eng.DeleteEntity(ctx, "escalation_chains", lastSegment(path))
		writeResult(w, http.StatusOK, v, err)

	// Integrations
	case method == http.MethodGet && path == "/api/v1/integrations":
		v, err := eng.ListCollectionPage(ctx, "integrations", pageParams(r))
		writeResult(w, http.StatusOK, v, err)
	case method == http.MethodPost && path == "/api/v1/integrations":
		body, ok := readJSON(w, r)
		if !ok {
			return
		}
		v, err := eng.CreateIntegration(ctx, body)
		writeResult(w, http.StatusCreated, v, err)
	case method == http.MethodGet && pathDepth(path, "/api/v1/integrations/") == 1:
		v, err := eng.GetItem(ctx, "integrations", lastSegment(path))
		writeResult(w, http.StatusOK, v, err)
	case method == http.MethodPut && pathDepth(path, "/api/v1/integrations/") == 1:
		body, ok := readJSON(w, r)
		if !ok {
			return
		}
		v, err := eng.UpdateIntegration(ctx, lastSegment(path), body)
		writeResult(w, http.StatusOK, v, err)
	case method == http.MethodDelete && pathDepth(path, "/api/v1/integrations/") == 1:
		v, err := eng.DeleteEntity(ctx, "integrations", lastSegment(path))
		writeResult(w, http.StatusOK, v, err)
	case method == http.MethodPost && strings.HasSuffix(path, "/rotate-key") && strings.HasPrefix(path, "/api/v1/integrations/"):
		v, err := eng.RotateIntegrationKey(ctx, segment(path, 3))
		writeResult(w, http.StatusOK, v, err)

	// Maintenance windows
	case method == http.MethodGet && path == "/api/v1/maintenance-windows":
		v, err := eng.ListCollectionPage(ctx, "maintenance_windows", pageParams(r))
		writeResult(w, http.StatusOK, v, err)
	case method == http.MethodPost && path == "/api/v1/maintenance-windows":
		body, ok := readJSON(w, r)
		if !ok {
			return
		}
		v, err := eng.CreateMaintenanceWindow(ctx, body)
		writeResult(w, http.StatusCreated, v, err)
	case method == http.MethodGet && pathDepth(path, "/api/v1/maintenance-windows/") == 1:
		v, err := eng.GetItem(ctx, "maintenance_windows", lastSegment(path))
		writeResult(w, http.StatusOK, v, err)
	case method == http.MethodPut && pathDepth(path, "/api/v1/maintenance-windows/") == 1:
		body, ok := readJSON(w, r)
		if !ok {
			return
		}
		v, err := eng.UpdateMaintenanceWindow(ctx, lastSegment(path), body)
		writeResult(w, http.StatusOK, v, err)
	case method == http.MethodDelete && pathDepth(path, "/api/v1/maintenance-windows/") == 1:
		v, err := eng.DeleteEntity(ctx, "maintenance_windows", lastSegment(path))
		writeResult(w, http.StatusOK, v, err)

	// Chatops
	case method == http.MethodGet && path == "/api/v1/chatops/channels":
		v, err := eng.ListCollectionPage(ctx, "chatops_channels", pageParams(r))
		writeResult(w, http.StatusOK, v, err)
	case method == http.MethodPost && path == "/api/v1/chatops/channels":
		body, ok := readJSON(w, r)
		if !ok {
			return
		}
		v, err := eng.CreateChatopsChannel(ctx, body)
		writeResult(w, http.StatusCreated, v, err)
	case method == http.MethodGet && pathDepth(path, "/api/v1/chatops/channels/") == 1:
		v, err := eng.GetItem(ctx, "chatops_channels", lastSegment(path))
		writeResult(w, http.StatusOK, v, err)
	case method == http.MethodPut && pathDepth(path, "/api/v1/chatops/channels/") == 1:
		body, ok := readJSON(w, r)
		if !ok {
			return
		}
		v, err := eng.UpdateChatopsChannel(ctx, lastSegment(path), body)
		writeResult(w, http.StatusOK, v, err)
	case method == http.MethodDelete && pathDepth(path, "/api/v1/chatops/channels/") == 1:
		v, err := eng.DeleteEntity(ctx, "chatops_channels", lastSegment(path))
		writeResult(w, http.StatusOK, v, err)
	case method == http.MethodGet && path == "/api/v1/chatops/messages":
		v, err := eng.ListCollectionPage(ctx, "chatops_messages", pageParams(r))
		writeResult(w, http.StatusOK, v, err)
	case method == http.MethodPost && path == "/api/v1/chatops/commands":
		body, ok := readJSON(w, r)
		if !ok {
			return
		}
		v, err := eng.PostChatopsCommand(ctx, body)
		writeResult(w, http.StatusOK, v, err)

	// Mobile
	case method == http.MethodGet && path == "/api/v1/mobile/devices":
		v, err := eng.ListCollectionPage(ctx, "mobile_devices", pageParams(r))
		writeResult(w, http.StatusOK, v, err)
	case method == http.MethodPost && path == "/api/v1/mobile/devices":
		body, ok := readJSON(w, r)
		if !ok {
			return
		}
		v, err := eng.RegisterMobileDevice(ctx, body)
		writeResult(w, http.StatusCreated, v, err)
	case method == http.MethodPost && path == "/api/v1/mobile/sessions":
		body, ok := readJSON(w, r)
		if !ok {
			return
		}
		v, err := eng.CreateMobileSession(ctx, body)
		writeResult(w, http.StatusCreated, v, err)
	case method == http.MethodGet && path == "/api/v1/mobile/dashboard":
		token := r.Header.Get("X-Mobile-Session")
		if token == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "X-Mobile-Session header is required"})
			return
		}
		v, err := eng.GetMobileDashboard(ctx, token)
		writeResult(w, http.StatusOK, v, err)
	case method == http.MethodPost && strings.HasSuffix(path, "/acknowledge") && strings.HasPrefix(path, "/api/v1/mobile/alert-groups/"):
		token := r.Header.Get("X-Mobile-Session")
		if token == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "X-Mobile-Session header is required"})
			return
		}
		v, err := eng.MobileAcknowledgeGroup(ctx, token, segment(path, 4))
		writeResult(w, http.StatusOK, v, err)
	case method == http.MethodPost && strings.HasSuffix(path, "/resolve") && strings.HasPrefix(path, "/api/v1/mobile/alert-groups/"):
		token := r.Header.Get("X-Mobile-Session")
		if token == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "X-Mobile-Session header is required"})
			return
		}
		v, err := eng.MobileResolveGroup(ctx, token, segment(path, 4))
		writeResult(w, http.StatusOK, v, err)

	// Alert groups
	case method == http.MethodGet && path == "/api/v1/alert-groups":
		v, err := eng.ListCollectionPage(ctx, "alert_groups", pageParams(r))
		writeResult(w, http.StatusOK, v, err)
	case method == http.MethodGet && strings.HasSuffix(path, "/timeline") && strings.HasPrefix(path, "/api/v1/alert-groups/"):
		groupID := segment(path, 3)
		group, err := eng.GetItem(ctx, "alert_groups", groupID)
		if err != nil {
			writeEngineError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"id": groupID, "timeline": group["logs"]})
	case method == http.MethodGet && pathDepth(path, "/api/v1/alert-groups/") == 1:
		v, err := eng.GetItem(ctx, "alert_groups", lastSegment(path))
		writeResult(w, http.StatusOK, v, err)
	case method == http.MethodPost && path == "/api/v1/alert-groups/bulk-resolve":
		body, ok := readJSON(w, r)
		if !ok {
			return
		}
		v, err := eng.BulkResolveGroups(ctx, extractGroupIDs(body))
		writeResult(w, http.StatusOK, v, err)
	case method == http.MethodPost && path == "/api/v1/alert-groups/bulk-acknowledge":
		body, ok := readJSON(w, r)
		if !ok {
			return
		}
		v, err := eng.BulkAcknowledgeGroups(ctx, extractGroupIDs(body))
		writeResult(w, http.StatusOK, v, err)
	case method == http.MethodPost && path == "/api/v1/alert-groups/bulk-silence":
		body, ok := readJSON(w, r)
		if !ok {
			return
		}
		durationMin := parseIntParam(r.URL.Query().Get("duration_minutes"), 60)
		if v, ok := body["duration_minutes"]; ok {
			durationMin = parseIntParam(fmt.Sprintf("%v", v), durationMin)
		}
		v, err := eng.BulkSilenceGroups(ctx, extractGroupIDs(body), durationMin)
		writeResult(w, http.StatusOK, v, err)
	case method == http.MethodPost && strings.HasSuffix(path, "/acknowledge") && strings.HasPrefix(path, "/api/v1/alert-groups/"):
		v, err := eng.AcknowledgeGroup(ctx, segment(path, 3))
		writeResult(w, http.StatusOK, v, err)
	case method == http.MethodPost && strings.HasSuffix(path, "/unacknowledge") && strings.HasPrefix(path, "/api/v1/alert-groups/"):
		v, err := eng.UnacknowledgeGroup(ctx, segment(path, 3))
		writeResult(w, http.StatusOK, v, err)
	case method == http.MethodPost && strings.HasSuffix(path, "/resolve") && strings.HasPrefix(path, "/api/v1/alert-groups/"):
		v, err := eng.ResolveGroup(ctx, segment(path, 3))
		writeResult(w, http.StatusOK, v, err)
	case method == http.MethodPost && strings.HasSuffix(path, "/unresolve") && strings.HasPrefix(path, "/api/v1/alert-groups/"):
		v, err := eng.UnresolveGroup(ctx, segment(path, 3))
		writeResult(w, http.StatusOK, v, err)
	case method == http.MethodPost && strings.HasSuffix(path, "/silence") && strings.HasPrefix(path, "/api/v1/alert-groups/"):
		body, ok := readJSON(w, r)
		if !ok {
			return
		}
		durationMin := parseIntParam(r.URL.Query().Get("duration_minutes"), 60)
		if v, ok := body["duration_minutes"]; ok {
			durationMin = parseIntParam(fmt.Sprintf("%v", v), durationMin)
		}
		v, err := eng.SilenceGroup(ctx, segment(path, 3), durationMin)
		writeResult(w, http.StatusOK, v, err)

	// Alerts, notifications, history
	case method == http.MethodGet && path == "/api/v1/alerts":
		v, err := eng.ListCollectionPage(ctx, "alerts", pageParams(r))
		writeResult(w, http.StatusOK, v, err)
	case method == http.MethodGet && path == "/api/v1/notifications":
		v, err := eng.ListCollectionPage(ctx, "notifications", pageParams(r))
		writeResult(w, http.StatusOK, v, err)
	case method == http.MethodGet && path == "/api/v1/delivery-attempts":
		notifID := r.URL.Query().Get("notification_id")
		v, err := eng.GetDeliveryAttempts(ctx, notifID)
		writeResult(w, http.StatusOK, v, err)
	// Audit trail. Admin-only (see requiredAction); newest-first, filterable by
	// actor, entity and action so "everything this key did" and "everything that
	// happened to this group" are both one query.
	case method == http.MethodGet && path == "/api/v1/audit":
		q := r.URL.Query()
		filters := map[string]any{
			"actor_id":    q.Get("actor_id"),
			"entity_type": q.Get("entity_type"),
			"entity_id":   q.Get("entity_id"),
			"action":      q.Get("action"),
			// "everything that one request did", which is the query an
			// incident review actually starts from.
			"request_id": q.Get("request_id"),
			// The same question one level up: everything across every process
			// that belongs to one trace. A request id cannot reach the worker
			// cycle that escalated the group an hour after it was ingested.
			"trace_id": q.Get("trace_id"),
		}
		q2 := r.URL.Query()
		v, err := eng.ListAuditEvents(ctx, filters,
			parseIntParam(q2.Get("limit"), 100), parseIntParam(q2.Get("offset"), 0))
		writeResult(w, http.StatusOK, v, err)
	case method == http.MethodGet && path == "/api/v1/insights/summary":
		q := r.URL.Query()
		v, err := eng.GetInsightsSummary(ctx, map[string]any{
			"from":           q.Get("from"),
			"to":             q.Get("to"),
			"integration_id": q.Get("integration_id"),
		})
		writeResult(w, http.StatusOK, v, err)
	case method == http.MethodGet && path == "/api/v1/history":
		q := r.URL.Query()
		filters := map[string]any{
			"from":        q.Get("from"),
			"to":          q.Get("to"),
			"integration": q.Get("integration"),
			"severity":    q.Get("severity"),
			"status":      q.Get("status"),
			"channel":     q.Get("channel"),
			"user":        q.Get("user"),
			"team":        q.Get("team"),
			"limit":       parseIntParam(q.Get("limit"), 50),
			"offset":      parseIntParam(q.Get("offset"), 0),
		}
		v, err := eng.GetHistory(ctx, filters)
		writeResult(w, http.StatusOK, v, err)

	// Debug / escalations
	case method == http.MethodPost && strings.HasPrefix(path, "/api/v1/routes/debug/"):
		key := lastSegment(path)
		body, ok := readJSON(w, r)
		if !ok {
			return
		}
		v, err := eng.DebugRoute(ctx, key, body)
		writeResult(w, http.StatusOK, v, err)
	case method == http.MethodPost && path == "/api/v1/escalations/run":
		items, err := eng.ProcessDueEscalations(ctx)
		writeResultSlice(w, http.StatusOK, items, err)

	// What this edition and this deployment can do, and why not when they
	// cannot. See capabilities.go.
	case method == http.MethodGet && path == "/api/v1/capabilities":
		srv.handleCapabilities(w, r)

	// Setup readiness (BETA-051)
	case method == http.MethodGet && path == "/api/v1/readiness":
		v, err := eng.Readiness(ctx)
		writeResult(w, http.StatusOK, v, err)
	case method == http.MethodPost && path == "/api/v1/readiness/acknowledge":
		body, ok := readJSON(w, r)
		if !ok {
			return
		}
		v, err := eng.AcknowledgeReadiness(ctx, body)
		writeResult(w, http.StatusOK, v, err)
	case method == http.MethodPost && path == "/api/v1/backups/report":
		body, ok := readJSON(w, r)
		if !ok {
			return
		}
		v, err := eng.ReportBackup(ctx, body)
		writeResult(w, http.StatusOK, v, err)

	default:
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "route not found"})
	}
}

// ---- helpers ----

func (srv *Server) verifyWebhookSig(r *http.Request, integrationKey string, rawBody map[string]any) error {
	integration, err := srv.store.FindIntegrationByKey(r.Context(), integrationKey)
	if err != nil {
		return fmt.Errorf("lookup integration: %w", err)
	}
	if integration == nil {
		return nil
	}
	rawSecret, _ := integration["webhook_secret"].(string)
	secret := utils.ResolveSecretRef(rawSecret)
	if secret == "" {
		return nil
	}
	raw, err := json.Marshal(rawBody)
	if err != nil {
		return nil
	}
	sigHeader := r.Header.Get("X-Hub-Signature-256")
	if sigHeader == "" {
		sigHeader = r.Header.Get("X-Anomaly-Signature")
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(raw)
	computed := "sha256=" + fmt.Sprintf("%x", mac.Sum(nil))
	if !hmac.Equal([]byte(sigHeader), []byte(computed)) {
		return errWebhookSigInvalid
	}
	return nil
}
