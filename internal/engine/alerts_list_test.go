package engine

import (
	"fmt"
	"strings"
	"testing"

	"github.com/nixys/nxs-anomaly/internal/model"
	"github.com/nixys/nxs-anomaly/internal/utils"
)

// `status` answers how many alerts are open; `alerts` answers which ones and
// hands the responder a button per group. Paging state rides in callback_data
// rather than in a stored session: a list is read by one person for a few
// seconds, and a session would outlive the reason it existed.

func listGroups(n int) []model.AlertGroup {
	var out []model.AlertGroup
	for i := 0; i < n; i++ {
		g := model.NewAlertGroup(model.NewAlertGroupParams{
			IntegrationID: "int-1",
			Title:         fmt.Sprintf("alert %d", i),
			Severity:      "warning",
			Timestamp:     utils.ToISO(utils.UTCNow()),
		})
		out = append(out, g)
	}
	return out
}

func TestAlertsPageCutsIntoPages(t *testing.T) {
	groups := listGroups(12)

	first := alertsPage(groups, 1)
	if got := len(first["alerts_page"].([]map[string]any)); got != alertsPageSize {
		t.Errorf("first page holds %d group(s), want %d", got, alertsPageSize)
	}
	if first["pages"] != 3 {
		t.Errorf("pages = %v, want 3", first["pages"])
	}

	last := alertsPage(groups, 3)
	if got := len(last["alerts_page"].([]map[string]any)); got != 2 {
		t.Errorf("last page holds %d group(s), want the 2 that remain", got)
	}
}

// Pages shrink under the reader as colleagues acknowledge and resolve. A tap on
// a "next" that was valid a minute ago should land on the last page, not on an
// error about a page that no longer exists.
func TestAlertsPageClampsPastTheEnd(t *testing.T) {
	page := alertsPage(listGroups(3), 99)

	if page["page"] != 1 || page["pages"] != 1 {
		t.Errorf("page/pages = %v/%v, want the request clamped to the only page", page["page"], page["pages"])
	}
	if got := len(page["alerts_page"].([]map[string]any)); got != 3 {
		t.Errorf("clamped page holds %d group(s), want all 3", got)
	}
}

func TestAlertsPageOnEmptyListSaysSo(t *testing.T) {
	page := alertsPage(nil, 1)

	if page["text"] != "No open alert groups" {
		t.Errorf("text = %q, want a plain statement that there is nothing", page["text"])
	}
	if got := len(page["alerts_page"].([]map[string]any)); got != 0 {
		t.Errorf("empty list yielded %d item(s)", got)
	}
}

func TestPageArgDefaultsToFirstPage(t *testing.T) {
	for _, args := range [][]string{nil, {}, {"0"}, {"-2"}, {"abc"}, {""}} {
		if got := pageArg(args); got != 1 {
			t.Errorf("pageArg(%v) = %d, want 1", args, got)
		}
	}
	if got := pageArg([]string{"4"}); got != 4 {
		t.Errorf("pageArg([4]) = %d, want 4", got)
	}
}

// Every listed group must be actionable from the list itself; that is the whole
// difference between this and `status`.
func TestTelegramReplyPayloadPutsAButtonOnEveryGroup(t *testing.T) {
	result := map[string]any{"response": alertsPage(listGroups(3), 1)}

	payload := telegramReplyPayload("-100500", "Open alert groups: 3", result, "")

	markup, ok := payload["reply_markup"].(map[string]any)
	if !ok {
		t.Fatalf("no keyboard on a list of 3 groups: %+v", payload)
	}
	rows := markup["inline_keyboard"].([]any)
	// One row per group, plus the bulk row; no pager, since three groups fit.
	if len(rows) != 4 {
		t.Fatalf("keyboard has %d row(s), want one per group plus bulk and no pager", len(rows))
	}
	for i, raw := range rows {
		for j, rawButton := range raw.([]any) {
			data, _ := rawButton.(map[string]any)["callback_data"].(string)
			if _, ok := ParseTelegramCallbackData(data); !ok {
				t.Errorf("row %d button %d renders callback_data %q the callback path rejects", i, j, data)
			}
		}
	}
}

// Opening a group and acting on it are two different buttons. They used to be
// one: a row labelled with the group's title whose action was acknowledge, so
// the label promised navigation and the tap changed the incident's state.
func TestTelegramListingSeparatesOpeningFromActing(t *testing.T) {
	result := map[string]any{"response": alertsPage(listGroups(1), 1)}

	payload := telegramReplyPayload("-100500", "Open alert groups: 1", result, "")

	rows := payload["reply_markup"].(map[string]any)["inline_keyboard"].([]any)
	row := rows[0].([]any)
	if len(row) != 2 {
		t.Fatalf("group row has %d button(s), want the label and the acknowledge", len(row))
	}
	open := row[0].(map[string]any)
	if got, _ := open["callback_data"].(string); !strings.HasPrefix(got, "show:") {
		t.Errorf("the labelled button runs %q, want a read-only show", got)
	}
	act := row[1].(map[string]any)
	if got, _ := act["callback_data"].(string); !strings.HasPrefix(got, "ack:") {
		t.Errorf("the second button runs %q, want the acknowledge", got)
	}
}

func TestTelegramReplyPayloadPagerOnlyOffersReachablePages(t *testing.T) {
	groups := listGroups(12)

	cases := []struct {
		page  int
		want  []string
		label string
	}{
		{1, []string{"alerts:2"}, "first page offers only next"},
		{2, []string{"alerts:1", "alerts:3"}, "middle page offers both"},
		{3, []string{"alerts:2"}, "last page offers only previous"},
	}
	for _, c := range cases {
		t.Run(c.label, func(t *testing.T) {
			payload := telegramReplyPayload("-100500", "x", map[string]any{"response": alertsPage(groups, c.page)}, "")
			rows := payload["reply_markup"].(map[string]any)["inline_keyboard"].([]any)
			pager := rows[len(rows)-1].([]any)
			if len(pager) != len(c.want) {
				t.Fatalf("pager has %d button(s), want %d", len(pager), len(c.want))
			}
			for i, want := range c.want {
				if got := pager[i].(map[string]any)["callback_data"]; got != want {
					t.Errorf("pager button %d = %v, want %q", i, got, want)
				}
			}
		})
	}
}

func TestTelegramReplyPayloadHasNoPagerWhenEverythingFits(t *testing.T) {
	payload := telegramReplyPayload("-100500", "x", map[string]any{"response": alertsPage(listGroups(2), 1)}, "")

	rows := payload["reply_markup"].(map[string]any)["inline_keyboard"].([]any)
	if len(rows) != 3 {
		t.Errorf("keyboard has %d row(s), want two groups plus bulk and no navigation", len(rows))
	}
}

// An ordinary command answer — an acknowledge, a help text — carries no list and
// must not grow a keyboard out of nothing.
func TestTelegramReplyPayloadWithoutAListHasNoKeyboard(t *testing.T) {
	payload := telegramReplyPayload("-100500", "Acknowledged grp-1",
		map[string]any{"response": map[string]any{"text": "Acknowledged grp-1"}}, "")

	if _, present := payload["reply_markup"]; present {
		t.Errorf("keyboard attached to a plain answer: %+v", payload["reply_markup"])
	}
}

func TestParseTelegramCallbackDataAcceptsPaging(t *testing.T) {
	got, ok := ParseTelegramCallbackData("alerts:3")
	if !ok || got != "alerts 3" {
		t.Errorf("ParseTelegramCallbackData(\"alerts:3\") = (%q, %v), want (\"alerts 3\", true)", got, ok)
	}
}
