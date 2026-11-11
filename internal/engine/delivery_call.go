package engine

import (
	"fmt"
	"io"
	"net"
	"strings"
	"time"

	"github.com/nixys/nxs-anomaly/internal/utils"
)

// Voice-call delivery through Asterisk AMI.

func callMessage(payload map[string]any) string {
	severity := strDefault(utils.StrVal(payload, "severity"), "unknown")
	title := strDefault(utils.StrVal(payload, "title"), "alert")
	if severity == "critical" {
		return fmt.Sprintf("Emergency alert. %s. See messages for details.", title)
	}
	return fmt.Sprintf("Alert. %s. See messages for details.", title)
}

func sendAsteriskCall(phone, message string, instances []asteriskInstance) (status, errMsg, providerResp string) {
	if len(instances) == 0 {
		return "failed", "No Asterisk instance configured", ""
	}
	var lastErr string
	for _, inst := range instances {
		s, e, r := sendAsteriskCallToInstance(inst, phone, message)
		if s == "delivered" {
			return s, e, r
		}
		lastErr = e
	}
	if lastErr == "" {
		lastErr = "All Asterisk instances failed"
	}
	return "failed", lastErr, ""
}

type asteriskInstance struct {
	host, username, secret, channel, context, exten, priority, callerID, variableName string
	// dial, when set, opens the AMI socket through the configured SOCKS5 proxy.
	// nil is the direct dial this has always done.
	dial func(network, addr string) (net.Conn, error)
}

func sendAsteriskCallToInstance(inst asteriskInstance, phone, message string) (status, errMsg, providerResp string) {
	if inst.host == "" || inst.username == "" || inst.secret == "" ||
		inst.channel == "" || inst.context == "" || inst.exten == "" || phone == "" {
		return "failed", fmt.Sprintf("Asterisk AMI settings incomplete for %s", inst.host), ""
	}
	ch := inst.channel
	if !strings.HasSuffix(ch, "/") {
		ch += "/"
	}
	ch += phone

	dial := inst.dial
	if dial == nil {
		dial = func(network, addr string) (net.Conn, error) {
			return net.DialTimeout(network, addr, 5*time.Second)
		}
	}
	conn, err := dial("tcp", inst.host+":5038")
	if err != nil {
		return "failed", err.Error(), ""
	}
	defer conn.Close() //nolint:errcheck

	cmd := strings.Join([]string{
		"Action: Login",
		"Username: " + inst.username,
		"Secret: " + inst.secret,
		"Events: off",
		"",
		"Action: Originate",
		"Channel: " + ch,
		"Context: " + inst.context,
		"Exten: " + inst.exten,
		"Priority: " + inst.priority,
		"CallerID: " + inst.callerID,
		"Async: true",
		"Variable: " + inst.variableName + "=" + message,
		"",
		"Action: Logoff",
		"",
	}, "\r\n")

	// Set a deadline so we never block past the AMI exchange window.
	conn.SetDeadline(time.Now().Add(10 * time.Second)) //nolint:errcheck
	if _, err := conn.Write([]byte(cmd + "\r\n")); err != nil {
		return "failed", err.Error(), ""
	}
	// ReadAll reads until Asterisk closes the connection after Logoff.
	respBytes, _ := io.ReadAll(conn)
	resp := string(respBytes)
	if strings.Contains(resp, "Response: Error") {
		return "failed", resp, ""
	}
	return "delivered", "", "asterisk_ami_originate"
}
