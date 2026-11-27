package engine

import (
	"bufio"
	"fmt"
	"net"
	"regexp"
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
		if s == "delivered" || s == amiUnconfirmed {
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

const amiUnconfirmed = "asterisk_originate_unconfirmed"

var trunkNumber = regexp.MustCompile(`^\+?[0-9]+$`)

func sendAsteriskCallToInstance(inst asteriskInstance, phone, message string) (status, errMsg, providerResp string) {
	if inst.host == "" || inst.username == "" || inst.secret == "" ||
		inst.channel == "" || inst.context == "" || inst.exten == "" || phone == "" {
		return "failed", fmt.Sprintf("Asterisk AMI settings incomplete for %s", inst.host), ""
	}
	if strings.ContainsAny(phone, "\r\n") {
		return "failed", "Invalid phone number", ""
	}
	var ch string
	if strings.Contains(inst.channel, "{phone}") {
		if !trunkNumber.MatchString(phone) {
			return "failed", "Trunk numbers must contain digits with an optional leading +", ""
		}
		ch = strings.ReplaceAll(inst.channel, "{phone}", phone)
	} else {
		ch = strings.TrimSuffix(inst.channel, "/") + "/" + phone
	}
	message = strings.NewReplacer("\r", " ", "\n", " ").Replace(message)

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
		"ActionID: nxs-login",
		"Username: " + inst.username,
		"Secret: " + inst.secret,
		"Events: off",
		"",
		"Action: Originate",
		"ActionID: nxs-originate",
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
	if n, err := conn.Write([]byte(cmd + "\r\n")); err != nil {
		// A complete Originate can already be queued even if Logoff failed to write.
		if n >= strings.LastIndex(cmd, "Action: Logoff") {
			return amiUnconfirmed, "Asterisk Originate confirmation lost: " + err.Error(), ""
		}
		return "failed", err.Error(), ""
	}
	// Each attempt owns its connection. Only a complete response to our
	// Originate confirms queueing. Login success and Logoff do not.
	scanner := bufio.NewScanner(conn)
	var actionID, response, detail string
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if response == "Error" && (actionID == "nxs-login" || actionID == "nxs-originate") {
				return "failed", "Asterisk AMI: " + detail, ""
			}
			if actionID == "nxs-originate" && response == "Success" {
				return "delivered", "", "asterisk_ami_originate"
			}
			actionID, response, detail = "", "", ""
			continue
		}
		key, value, _ := strings.Cut(line, ": ")
		switch key {
		case "ActionID":
			actionID = value
		case "Response":
			response = value
		case "Message":
			detail = value
		}
	}
	if err := scanner.Err(); err != nil {
		return amiUnconfirmed, "Asterisk Originate confirmation lost: " + err.Error(), ""
	}
	return amiUnconfirmed, "Asterisk AMI closed before confirming Originate", ""
}
