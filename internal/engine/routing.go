package engine

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/nixys/nxs-anomaly/internal/utils"
)

func selectRoute(integration map[string]any, payload map[string]any) (map[string]any, error) {
	labels, _ := utils.CoerceLabelMap(payload["labels"])
	payloadJSON := utils.JSONDumps(payload)
	routes, _ := integration["routes"].([]any)
	var defaultRoute map[string]any
	for _, r := range routes {
		route, ok := r.(map[string]any)
		if !ok {
			continue
		}
		if utils.BoolVal(route, "is_default", false) {
			defaultRoute = route
			continue
		}
		switch route["match_type"] {
		case "labels":
			routeLabels, _ := route["labels"].(map[string]any)
			matched := true
			for k, v := range routeLabels {
				if labels[k] != fmt.Sprintf("%v", v) {
					matched = false
					break
				}
			}
			if matched {
				return route, nil
			}
		case "regex":
			pattern := utils.StrVal(route, "pattern")
			if pattern != "" {
				if ok, _ := regexp.MatchString(pattern, payloadJSON); ok {
					return route, nil
				}
			}
		}
	}
	if defaultRoute == nil {
		return nil, fmt.Errorf("integration default route is missing")
	}
	return defaultRoute, nil
}

func buildDedupeKey(integration map[string]any, labels map[string]string, title string) string {
	groupBy, _ := integration["group_by"].([]any)
	var parts []string
	for _, k := range groupBy {
		key := fmt.Sprintf("%v", k)
		if v, ok := labels[key]; ok {
			parts = append(parts, key+"="+v)
		}
	}
	if len(parts) == 0 {
		parts = []string{"title=" + title}
	}
	return strings.Join(parts, "|")
}

func alertmanagerGroupLabelsKey(groupLabels map[string]string) string {
	if len(groupLabels) == 0 {
		return ""
	}
	keys := make([]string, 0, len(groupLabels))
	for k := range groupLabels {
		keys = append(keys, k)
	}
	sortStrings(keys)
	var parts []string
	for _, k := range keys {
		parts = append(parts, k+"="+groupLabels[k])
	}
	return "alertmanager_group:" + strings.Join(parts, "|")
}

// routeChainIDs extracts all non-empty escalation_chain_id values from an integration payload.
func routeChainIDs(payload map[string]any) []string {
	routeList, _ := payload["routes"].([]any)
	seen := map[string]bool{}
	var ids []string
	for _, r := range routeList {
		rm, _ := r.(map[string]any)
		if rm == nil {
			continue
		}
		if cid := utils.StrVal(rm, "escalation_chain_id"); cid != "" && !seen[cid] {
			ids = append(ids, cid)
			seen[cid] = true
		}
	}
	return ids
}
