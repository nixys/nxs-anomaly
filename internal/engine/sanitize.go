package engine

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/nixys/nxs-anomaly/internal/utils"
)

func sanitizePriority(raw any) (string, error) {
	if raw == nil {
		return "medium", nil
	}
	p := strings.ToLower(fmt.Sprintf("%v", raw))
	if !supportedPriorities[p] {
		return "", errValidation(fmt.Sprintf("unsupported priority: %s", p))
	}
	return p, nil
}

// sanitizeNotificationTargets validates a user's notification targets against
// both what the build supports and what this installation permits.
//
// The policy is applied at configuration time as well as at delivery time. Only
// checking at delivery would mean an operator can save a Telegram target on a
// deployment that refuses Telegram, see it accepted, and find out that nobody
// was ever paged on it the night it mattered.
func sanitizeNotificationTargets(raw any, policy ChannelPolicy) ([]any, error) {
	if raw == nil {
		return []any{map[string]any{"type": "log", "target": ""}}, nil
	}
	list, ok := raw.([]any)
	if !ok || len(list) == 0 {
		return nil, errValidation("notification_targets must be a non-empty list")
	}
	var targets []any
	for _, item := range list {
		m, ok := item.(map[string]any)
		if !ok {
			return nil, errValidation("notification target must be an object")
		}
		t := strings.ToLower(fmt.Sprintf("%v", m["type"]))
		if t == "" {
			t = "log"
		}
		if !supportedNotificationTargets[t] {
			return nil, errValidation(fmt.Sprintf("unsupported notification target type: %s", t))
		}
		target := fmt.Sprintf("%v", m["target"])
		if err := policy.validateTarget(t, target); err != nil {
			return nil, err
		}
		targets = append(targets, map[string]any{"type": t, "target": target})
	}
	return targets, nil
}

// sanitizeNotificationPolicies validates a user's personal notification policies
// (BETA-031): an object with "default" and/or "important" keys, each an ordered
// list of {channel, target?, wait_minutes} steps. Returns nil when there are no
// usable steps, which keeps the user on the legacy target-blast path.
// rejectInlineSecret refuses a new plaintext secret while the production profile
// is active: a secret in a create/update request must be an "env:VAR" reference
// so it never lands in the database as plaintext. An empty value and an existing
// reference are fine, and legacy inline values already stored keep being read —
// only the creation of new inline secrets is blocked.
func rejectInlineSecret(field, value string) error {
	if value == "" || utils.IsSecretRef(value) || !utils.ProductionProfile() {
		return nil
	}
	return errValidation(fmt.Sprintf(
		"%s must be an env: reference in the production profile (e.g. env:NXS_ANOMALY_MY_SECRET), not an inline secret",
		field))
}

func sanitizeNotificationPolicies(raw any, policy ChannelPolicy) (map[string]any, error) {
	if raw == nil {
		return nil, nil
	}
	m, ok := raw.(map[string]any)
	if !ok {
		return nil, errValidation("notification_policies must be an object")
	}
	out := map[string]any{}
	for _, name := range []string{"default", "important"} {
		v, present := m[name]
		if !present || v == nil {
			continue
		}
		list, ok := v.([]any)
		if !ok {
			return nil, errValidation(fmt.Sprintf("notification_policies.%s must be a list", name))
		}
		steps := make([]any, 0, len(list))
		for _, raw := range list {
			step, ok := raw.(map[string]any)
			if !ok {
				return nil, errValidation("notification policy step must be an object")
			}
			ch := strings.ToLower(strings.TrimSpace(fmt.Sprintf("%v", step["channel"])))
			if !policyChannels[ch] {
				return nil, errValidation(fmt.Sprintf("unsupported notification policy channel: %s", ch))
			}
			target := strings.TrimSpace(fmt.Sprintf("%v", strDefault(utils.StrVal(step, "target"), "")))
			if err := policy.validateTarget(ch, target); err != nil {
				return nil, err
			}
			wait := maxInt(intFromAny(step["wait_minutes"], 0), 0)
			steps = append(steps, map[string]any{
				"channel":      ch,
				"target":       target,
				"wait_minutes": wait,
			})
		}
		if len(steps) > 0 {
			out[name] = steps
		}
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

func (e *Engine) sanitizeNotificationPolicy(ctx context.Context, raw any) (map[string]any, error) {
	policy := copyMap(defaultNotificationPolicy)
	if raw == nil {
		return policy, nil
	}
	m, ok := raw.(map[string]any)
	if !ok {
		return nil, errValidation("notification_policy must be an object")
	}
	if ch, err := utils.CoerceStringList(m["channels"]); err == nil && len(ch) > 0 {
		for _, c := range ch {
			if !supportedNotificationTargets[c] {
				return nil, errValidation(fmt.Sprintf("unsupported notification policy channel: %s", c))
			}
		}
		anyList := make([]any, len(ch))
		for i, v := range ch {
			anyList[i] = v
		}
		policy["channels"] = anyList
	}
	policy["batch_timeout_seconds"] = maxInt(intFromAny(m["batch_timeout_seconds"], 0), 0)
	policy["batch_deadline_seconds"] = maxInt(intFromAny(m["batch_deadline_seconds"], 0), 0)
	policy["epic_threshold_count"] = maxInt(intFromAny(m["epic_threshold_count"], 0), 0)
	policy["epic_threshold_seconds"] = maxInt(intFromAny(m["epic_threshold_seconds"], 0), 0)

	if uid, ok := m["emergency_user_id"]; ok && uid != nil && uid != "" {
		s := fmt.Sprintf("%v", uid)
		if err := e.ensureUsersExist(ctx, []string{s}); err != nil {
			return nil, err
		}
		policy["emergency_user_id"] = s
	}
	if uid, ok := m["epic_user_id"]; ok && uid != nil && uid != "" {
		s := fmt.Sprintf("%v", uid)
		if err := e.ensureUsersExist(ctx, []string{s}); err != nil {
			return nil, err
		}
		policy["epic_user_id"] = s
	}
	return policy, nil
}

func (e *Engine) sanitizeLegacyPool(ctx context.Context, raw any) (map[string]any, error) {
	if raw == nil {
		return nil, nil
	}
	m, ok := raw.(map[string]any)
	if !ok {
		return nil, errValidation("legacy_pool must be an object")
	}
	emails, _ := utils.CoerceStringList(m["emails"])
	anyEmails := make([]any, len(emails))
	for i, v := range emails {
		anyEmails[i] = v
	}
	pool := map[string]any{
		"name":              fmt.Sprintf("%v", strOrEmpty(m, "name")),
		"description":       fmt.Sprintf("%v", strOrEmpty(m, "description")),
		"emergency_user_id": nilOrStr(m, "emergency_user_id"),
		"duty_user_id":      nilOrStr(m, "duty_user_id"),
		"emails":            anyEmails,
	}
	var userIDs []string
	if pool["emergency_user_id"] != nil {
		userIDs = append(userIDs, pool["emergency_user_id"].(string))
	}
	if pool["duty_user_id"] != nil {
		userIDs = append(userIDs, pool["duty_user_id"].(string))
	}
	if len(userIDs) > 0 {
		if err := e.ensureUsersExist(ctx, userIDs); err != nil {
			return nil, err
		}
	}
	return pool, nil
}

func sanitizeTemplates(raw any) (map[string]any, error) {
	if raw == nil {
		return map[string]any{}, nil
	}
	m, ok := raw.(map[string]any)
	if !ok {
		return nil, errValidation("templates must be an object")
	}
	result := map[string]any{}
	for k, v := range m {
		if k != "" && v != nil && v != "" {
			result[k] = fmt.Sprintf("%v", v)
		}
	}
	return result, nil
}

func (e *Engine) sanitizeStep(ctx context.Context, raw any, index int) (map[string]any, error) {
	m, ok := raw.(map[string]any)
	if !ok {
		return nil, errValidation("step must be an object")
	}
	kind := strings.ToUpper(fmt.Sprintf("%v", m["kind"]))
	if !supportedSteps[kind] {
		return nil, errValidation(fmt.Sprintf("unsupported escalation step: %s", kind))
	}
	stepID := utils.StrVal(m, "id")
	if stepID == "" {
		stepID = utils.MakeID("step")
	}
	step := map[string]any{
		"id":       stepID,
		"kind":     kind,
		"position": index,
	}

	switch kind {
	case StepWait:
		step["delay_minutes"] = maxInt(intFromAny(m["delay_minutes"], 0), 0)

	case StepNotifyUser:
		ids, err := utils.CoerceStringList(m["user_ids"])
		if err != nil {
			return nil, err
		}
		if len(ids) > 0 {
			if err := e.ensureUsersExist(ctx, ids); err != nil {
				return nil, err
			}
		}
		step["user_ids"] = toAnySlice(ids)

	case StepNotifySchedule:
		schedID := utils.StrVal(m, "schedule_id")
		// A schedule with holes pages nobody for the length of the hole, and
		// that failure is silent at the moment it matters. Attaching one to a
		// chain therefore requires the caller to say so explicitly.
		allowUncovered := utils.BoolVal(m, "allow_uncovered", false)
		if schedID != "" {
			if err := e.ensureSchedulesExist(ctx, []string{schedID}); err != nil {
				return nil, err
			}
			if !allowUncovered {
				if err := e.assertScheduleCovered(ctx, schedID); err != nil {
					return nil, err
				}
			}
		}
		step["schedule_id"] = schedID
		step["allow_uncovered"] = allowUncovered

	case StepNotifyTeam:
		teamID := utils.StrVal(m, "team_id")
		if teamID != "" {
			if err := e.ensureTeamsExist(ctx, []string{teamID}); err != nil {
				return nil, err
			}
		}
		step["team_id"] = teamID

	case StepNotifyEmergency:
		userID := utils.StrVal(m, "user_id")
		if userID != "" {
			if err := e.ensureUsersExist(ctx, []string{userID}); err != nil {
				return nil, err
			}
		}
		step["user_id"] = nilIfEmpty(userID)

	case StepNotifyDutyUsers:
		teamID := utils.StrVal(m, "team_id")
		if teamID != "" {
			if err := e.ensureTeamsExist(ctx, []string{teamID}); err != nil {
				return nil, err
			}
		}
		step["team_id"] = nilIfEmpty(teamID)
		step["fallback_to_all"] = utils.BoolVal(m, "fallback_to_all", true)

	case StepTriggerWebhook:
		hookURL := utils.StrVal(m, "webhook_url")
		if err := e.deliveryCfg.Channels.validateTarget("webhook", hookURL); err != nil {
			return nil, err
		}
		step["webhook_url"] = hookURL

	case StepCreateIssue:
		if err := utils.EnsureRequired(m, []string{"url"}); err != nil {
			return nil, errValidation(err.Error())
		}
		step["tracker_type"] = strDefault(utils.StrVal(m, "tracker_type"), "redmine")
		trackerURL := strings.TrimRight(utils.StrVal(m, "url"), "/")
		if err := e.deliveryCfg.Channels.validateTarget("webhook", trackerURL); err != nil {
			return nil, err
		}
		step["url"] = trackerURL
		step["token"] = utils.StrVal(m, "token")
		step["token_env"] = utils.StrVal(m, "token_env")
		step["project"] = utils.StrVal(m, "project")
		step["subject_template"] = strDefault(utils.StrVal(m, "subject_template"), "[{{ severity }}] {{ title }}")
		step["body_template"] = strDefault(utils.StrVal(m, "body_template"), "Alert group: {{ group_id }}\n\nTitle: {{ title }}\nSeverity: {{ severity }}")

	case StepResolve:
		// no extra fields

	case StepRepeat:
		step["from_position"] = maxInt(intFromAny(m["from_position"], 0), 0)
		step["max_repeat_count"] = maxInt(intFromAny(m["max_repeat_count"], 50), 1)
		step["cooldown_minutes"] = maxInt(intFromAny(m["cooldown_minutes"], 60), 0)
	}
	return step, nil
}

func sanitizeRoutes(payload map[string]any) ([]any, error) {
	rawRoutes, hasRoutes := payload["routes"]
	var routeList []any

	if !hasRoutes || rawRoutes == nil {
		chainID := nilIfEmpty(utils.StrVal(payload, "default_chain_id"))
		routeList = []any{
			map[string]any{
				"name":                "default",
				"match_type":          "all",
				"is_default":          true,
				"escalation_chain_id": chainID,
			},
		}
	} else {
		var ok bool
		routeList, ok = rawRoutes.([]any)
		if !ok || len(routeList) == 0 {
			return nil, errValidation("integration routes must be a non-empty list")
		}
	}

	defaultCount := 0
	var routes []any
	for _, rawRoute := range routeList {
		rm, ok := rawRoute.(map[string]any)
		if !ok {
			return nil, errValidation("route must be an object")
		}
		chainIDRaw := rm["escalation_chain_id"]
		var chainID any // nil allowed
		if chainIDRaw != nil && chainIDRaw != "" {
			chainID = fmt.Sprintf("%v", chainIDRaw)
		}
		matchType := strings.ToLower(utils.StrVal(rm, "match_type"))
		if matchType == "" {
			matchType = "all"
		}
		if matchType != "all" && matchType != "labels" && matchType != "regex" {
			return nil, errValidation(fmt.Sprintf("unsupported route match_type: %s", matchType))
		}
		isDefault := utils.BoolVal(rm, "is_default", matchType == "all")
		route := map[string]any{
			"id":                  utils.MakeID("route"),
			"name":                strDefault(utils.StrVal(rm, "name"), matchType),
			"match_type":          matchType,
			"is_default":          isDefault,
			"labels":              map[string]any{},
			"pattern":             "",
			"escalation_chain_id": chainID,
		}
		if matchType == "labels" {
			labels, err := utils.CoerceLabelMap(rm["labels"])
			if err != nil || len(labels) == 0 {
				return nil, errValidation("labels route requires labels map")
			}
			anyLabels := map[string]any{}
			for k, v := range labels {
				anyLabels[k] = v
			}
			route["labels"] = anyLabels
			route["is_default"] = utils.BoolVal(rm, "is_default", false)
		}
		if matchType == "regex" {
			pattern := utils.StrVal(rm, "pattern")
			if pattern == "" {
				return nil, errValidation("regex route requires pattern")
			}
			if _, err := regexp.Compile(pattern); err != nil {
				return nil, errValidation(fmt.Sprintf("invalid route pattern: %v", err))
			}
			route["pattern"] = pattern
			route["is_default"] = utils.BoolVal(rm, "is_default", false)
		}
		if route["is_default"].(bool) {
			defaultCount++
		}
		routes = append(routes, route)
	}
	if defaultCount != 1 {
		return nil, errValidation("integration must contain exactly one default route")
	}
	return routes, nil
}

// selectRoute picks the matching route from an integration for an incoming payload.
