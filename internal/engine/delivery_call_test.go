package engine

import (
	"bufio"
	"io"
	"net"
	"strings"
	"testing"
)

func TestAsteriskAMIConfirmation(t *testing.T) {
	const login = "Response: Success\r\nMessage: Authentication accepted\r\n\r\n"
	const queued = "Response: Success\r\nActionID: nxs-originate\r\nMessage: Originate successfully queued\r\n\r\n"
	for _, tc := range []struct{ name, channel, wantChannel, reply, status string }{
		{"bundled trunk", "PJSIP/{phone}@trunk", "PJSIP/+123@trunk", login + queued, "delivered"},
		{"existing prefix", "PJSIP", "PJSIP/+123", login + queued, "delivered"},
		{"empty", "PJSIP/", "PJSIP/+123", "", amiUnconfirmed},
		{"login only", "PJSIP/", "PJSIP/+123", login, amiUnconfirmed},
		{"wrong action", "PJSIP/", "PJSIP/+123", strings.Replace(queued, "nxs-originate", "other", 1), amiUnconfirmed},
		{"partial", "PJSIP/", "PJSIP/+123", strings.TrimSuffix(queued, "\r\n"), amiUnconfirmed},
		{"rejected", "PJSIP/", "PJSIP/+123", login + "Response: Error\r\nActionID: nxs-originate\r\nMessage: rejected\r\n\r\n", "failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, server := net.Pipe()
			request := make(chan string, 1)
			go func() {
				defer func() { _ = server.Close() }()
				var b strings.Builder
				scan := bufio.NewScanner(server)
				for scan.Scan() {
					b.WriteString(scan.Text() + "\n")
					if strings.HasSuffix(b.String(), "Action: Logoff\n\n") {
						break
					}
				}
				request <- b.String()
				_, _ = io.WriteString(server, tc.reply)
			}()
			inst := asteriskInstance{host: "local", username: "test", secret: "test", channel: tc.channel, context: "oncall", exten: "s", variableName: "MSG", dial: func(string, string) (net.Conn, error) { return client, nil }}
			instances := []asteriskInstance{inst}
			if tc.status == amiUnconfirmed {
				backup := inst
				backup.dial = func(string, string) (net.Conn, error) { t.Fatal("retried an unconfirmed Originate"); return nil, nil }
				instances = append(instances, backup)
			}
			status, detail, _ := sendAsteriskCall("+123", "alert\r\ncontinued", instances)
			if status != tc.status {
				t.Fatalf("got %s (%s), want %s", status, detail, tc.status)
			}
			sent := <-request
			for _, field := range []string{"Channel: " + tc.wantChannel + "\n", "ActionID: nxs-originate\n", "Variable: MSG=alert  continued\n"} {
				if !strings.Contains(sent, field) {
					t.Errorf("missing %q in %q", field, sent)
				}
			}
		})
	}
}

func TestAsteriskTrunkRejectsInvalidNumber(t *testing.T) {
	inst := asteriskInstance{host: "local", username: "test", secret: "test", channel: "PJSIP/{phone}@trunk", context: "oncall", exten: "s",
		dial: func(string, string) (net.Conn, error) { t.Fatal("unexpected dial"); return nil, nil }}
	for _, phone := range []string{"123@other", "12\r\n3"} {
		if status, _, _ := sendAsteriskCallToInstance(inst, phone, "test"); status != "failed" {
			t.Fatal(status)
		}
	}
}

// AMI actions share a TCP write. A short write can include the complete
// Originate frame even when the trailing Logoff was not sent.
type shortAMIWrite struct {
	net.Conn
	queued bool
}

func (w shortAMIWrite) Write(p []byte) (int, error) {
	n := strings.LastIndex(string(p), "Action: Logoff")
	if !w.queued {
		n--
	}
	return n, io.ErrClosedPipe
}

func TestAsteriskPartialWriteRetryBoundary(t *testing.T) {
	for _, queued := range []bool{false, true} {
		client, peer := net.Pipe()
		inst := asteriskInstance{
			host: "pbx", username: "user", secret: "secret", channel: "PJSIP", context: "oncall", exten: "s",
			dial: func(string, string) (net.Conn, error) { return shortAMIWrite{client, queued}, nil },
		}
		triedBackup := false
		backup := inst
		backup.dial = func(string, string) (net.Conn, error) {
			triedBackup = true
			return nil, io.ErrClosedPipe
		}
		status, _, _ := sendAsteriskCall("+123", "test", []asteriskInstance{inst, backup})
		_ = peer.Close()
		if triedBackup == queued || (status == amiUnconfirmed) != queued {
			t.Fatalf("queued=%v status=%s backup=%v", queued, status, triedBackup)
		}
	}
}
