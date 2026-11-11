package engine

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/nixys/nxs-anomaly/internal/utils"
)

// Issue-tracker delivery: Redmine and the generic issue endpoint.

func createRedmineIssue(ctx context.Context, client *http.Client, url, token, project, subject, body string) (status, issueID, errMsg string) {
	apiURL := strings.TrimRight(url, "/") + "/issues.json"
	payload := map[string]any{
		"issue": map[string]any{
			"project_id":  project,
			"subject":     subject,
			"description": body,
		},
	}
	bodyBytes := []byte(utils.JSONDumps(payload))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return "failed", "", err.Error()
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Redmine-API-Key", token)
	resp, err := client.Do(req)
	if err != nil {
		return "failed", "", err.Error()
	}
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "failed", "", fmt.Sprintf("HTTP %d", resp.StatusCode)
	}
	return "created", "", ""
}

func createGenericIssue(ctx context.Context, client *http.Client, url, token, subject, body string) (status, issueID, errMsg string) {
	payload := map[string]any{"subject": subject, "body": body}
	bodyBytes := []byte(utils.JSONDumps(payload))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(bodyBytes))
	if err != nil {
		return "failed", "", err.Error()
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := client.Do(req)
	if err != nil {
		return "failed", "", err.Error()
	}
	_ = resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "failed", "", fmt.Sprintf("HTTP %d", resp.StatusCode)
	}
	return "created", "", ""
}
