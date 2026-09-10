package server

import (
	"strings"
	"testing"

	"github.com/nixys/nxs-anomaly/internal/authz"
)

// The buttons a responder reaches for at three in the morning, driven through
// the real handler. Acknowledge and resolve were the only two for a long time,
// and neither is the right answer to noise: one claims the incident is being
// worked, the other claims it is over.

func TestTelegramSilenceButtonSilencesForTheDurationItNames(t *testing.T) {
	srv, st := alertsServer(t, 1)
	seedTelegramUser(t, st, "usr-bob", "4242", string(authz.RoleResponder))

	tapPager(t, srv, 4242, "silence:240:grp-00", 555)

	group := st.Row("alert_groups", "grp-00")
	if group["status"] != "silenced" {
		t.Fatalf("status = %v, want silenced", group["status"])
	}
	if group["silenced_until"] == nil || group["silenced_until"] == "" {
		t.Error("silenced_until is empty; a silence with no end never lifts")
	}
	// next_run_at is what the worker reads to escalate. A silence that leaves it
	// set would keep paging the person who just asked it to stop.
	if group["next_run_at"] != nil {
		t.Errorf("next_run_at = %v, want escalation stopped", group["next_run_at"])
	}
}

func TestTelegramSilenceRejectsANonsenseDuration(t *testing.T) {
	srv, st := alertsServer(t, 1)
	seedTelegramUser(t, st, "usr-bob", "4242", string(authz.RoleResponder))

	tapPager(t, srv, 4242, "silence:0:grp-00", 555)

	if got := st.Row("alert_groups", "grp-00")["status"]; got != "open" {
		t.Errorf("status = %v, want the group untouched", got)
	}
}

// The undo pair is the point of the settled message: a mis-tapped verdict from a
// phone used to be unfixable without opening the web UI.
func TestTelegramUndoButtonsReverseTheVerdict(t *testing.T) {
	srv, st := alertsServer(t, 1)
	seedTelegramUser(t, st, "usr-bob", "4242", string(authz.RoleResponder))

	tapPager(t, srv, 4242, "ack:grp-00", 555)
	if got := st.Row("alert_groups", "grp-00")["status"]; got != "acknowledged" {
		t.Fatalf("status = %v, want acknowledged before the undo", got)
	}

	tapPager(t, srv, 4242, "unack:grp-00", 555)
	group := st.Row("alert_groups", "grp-00")
	if group["status"] != "open" {
		t.Errorf("status = %v, want the acknowledgement taken back", group["status"])
	}
	// Escalation has to resume, or an undo would leave the group open and
	// silently unattended — worse than the state it was undoing.
	if group["next_run_at"] == nil || group["next_run_at"] == "" {
		t.Error("next_run_at is empty; escalation must resume after an undo")
	}
}

func TestTelegramReopenButtonUnresolves(t *testing.T) {
	srv, st := alertsServer(t, 1)
	seedTelegramUser(t, st, "usr-bob", "4242", string(authz.RoleResponder))

	tapPager(t, srv, 4242, "resolve:grp-00", 555)
	tapPager(t, srv, 4242, "unresolve:grp-00", 555)

	if got := st.Row("alert_groups", "grp-00")["status"]; got != "open" {
		t.Errorf("status = %v, want the group reopened", got)
	}
}

// Opening a group must not act on it. The listing row used to be one button
// labelled with the group's title whose action was acknowledge.
func TestTelegramShowOpensWithoutActing(t *testing.T) {
	srv, st := alertsServer(t, 1)
	seedTelegramUser(t, st, "usr-bob", "4242", string(authz.RoleResponder))

	reply := tapPager(t, srv, 4242, "show:grp-00", 555)

	if got := st.Row("alert_groups", "grp-00")["status"]; got != "open" {
		t.Errorf("status = %v, want opening the card to change nothing", got)
	}
	// A new message, not an edit: the listing is what the person will go back to.
	if reply["method"] != "sendMessage" {
		t.Errorf("method = %v, want the card posted alongside the listing", reply["method"])
	}
	if text, _ := reply["text"].(string); !strings.Contains(text, "alert 0") {
		t.Errorf("card text = %q, want it to name the group", text)
	}
	// The card is where the actions live, including the ones an alert
	// notification has no room for.
	rows := keyboardRows(t, reply)
	if len(rows) == 0 {
		t.Error("the card carries no actions; it would be a dead end")
	}
}

// A storm is the case the per-group buttons cannot serve: forty groups from one
// cluster failure, and a keyboard that acts on one at a time.
func TestTelegramBulkAcknowledgesEveryOpenGroup(t *testing.T) {
	srv, st := alertsServer(t, 3)
	seedTelegramUser(t, st, "usr-bob", "4242", string(authz.RoleResponder))

	reply := tapPager(t, srv, 4242, "bulk:ack", 555)

	for _, id := range []string{"grp-00", "grp-01", "grp-02"} {
		if got := st.Row("alert_groups", id)["status"]; got != "acknowledged" {
			t.Errorf("%s status = %v, want acknowledged", id, got)
		}
	}
	if text, _ := reply["text"].(string); !strings.Contains(text, "3") {
		t.Errorf("reply = %q, want it to state how many were acted on", text)
	}
}

func TestTelegramBulkSilenceUsesTheDurationOnTheButton(t *testing.T) {
	srv, st := alertsServer(t, 2)
	seedTelegramUser(t, st, "usr-bob", "4242", string(authz.RoleResponder))

	tapPager(t, srv, 4242, "bulk:silence:60", 555)

	for _, id := range []string{"grp-00", "grp-01"} {
		if got := st.Row("alert_groups", id)["status"]; got != "silenced" {
			t.Errorf("%s status = %v, want silenced", id, got)
		}
	}
}

// A viewer may read the listing and must not be able to empty it.
func TestTelegramBulkIsRefusedWithoutTheResponderRole(t *testing.T) {
	srv, st := alertsServer(t, 2)
	seedTelegramUser(t, st, "usr-vera", "4343", string(authz.RoleViewer))

	tapPager(t, srv, 4343, "bulk:ack", 555)

	for _, id := range []string{"grp-00", "grp-01"} {
		if got := st.Row("alert_groups", id)["status"]; got != "open" {
			t.Errorf("%s status = %v, want it untouched", id, got)
		}
	}
}

// First contact with the bot used to be an error, and — because an errored
// command produced no reply at all — in practice it was silence.
func TestTelegramStartAnswersWithAKeyboard(t *testing.T) {
	srv, st := alertsServer(t, 0)
	seedTelegramUser(t, st, "usr-bob", "4242", string(authz.RoleResponder))

	reply := sendCommand(t, srv, 4242, "/start")

	if reply["method"] != "sendMessage" {
		t.Fatalf("method = %v, want an answer Telegram will render", reply["method"])
	}
	if text, _ := reply["text"].(string); text == "" {
		t.Error("greeting is empty")
	}
	if rows := keyboardRows(t, reply); len(rows) == 0 {
		t.Error("greeting carries no buttons; the person is left to guess the commands")
	}
}

// An unlinked account is told so, because every command that needs a person will
// otherwise refuse with a message that reads like a bug.
func TestTelegramStartSaysWhenTheAccountIsNotLinked(t *testing.T) {
	srv, _ := alertsServer(t, 0)

	reply := sendCommand(t, srv, 9999, "/start")

	text, _ := reply["text"].(string)
	if !strings.Contains(text, "not linked") {
		t.Errorf("greeting = %q, want it to name the missing link", text)
	}
}

// A refused or unknown command has to reach the person. The webhook used to
// answer a JSON error object with a 4xx: Telegram ignores any body that is not a
// method call, and treats the non-2xx as a delivery to retry — so the command
// answered nothing and was redelivered.
func TestTelegramUnknownCommandAnswersInsteadOfFailing(t *testing.T) {
	srv, st := alertsServer(t, 0)
	seedTelegramUser(t, st, "usr-bob", "4242", string(authz.RoleResponder))

	reply := sendCommand(t, srv, 4242, "/nonsense")

	if reply["method"] != "sendMessage" {
		t.Fatalf("method = %v, want the refusal delivered as a method call", reply["method"])
	}
	if text, _ := reply["text"].(string); text == "" {
		t.Error("refusal carries no text; the person sees nothing")
	}
	if reply["chat_id"] != "-100500" {
		t.Errorf("chat_id = %v, want the chat the command came from", reply["chat_id"])
	}
}
