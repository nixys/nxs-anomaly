# Voice calls through Asterisk AMI

## Bundled Asterisk in Kubernetes

Enable the optional single-replica gateway for a UDP SIP trunk. Create a Secret in the release namespace with the keys `ami-password` (a random AMI password) and `sip-password` (the provider password). Use single-line passwords containing letters, digits or characters from `_./=@%:+-`. Set these Helm values using your provider's host, SIP username, approved outbound number and signaling/media networks.

```yaml
asterisk:
  enabled: true
  existingSecret: oncall-voice
  trunkHost: sip.example.org
  trunkPort: 5060
  username: oncall
  callerID: "+15550100000"
  providerCIDRs: ["192.0.2.0/24"]
```

For an internal registry mirror, copy all image platforms preserving the digest and set `asterisk.image: mirror.example.org/voice/asterisk@sha256:<digest from values.yaml>`. If AMI disconnects after Originate was sent, check the PBX call history before manually retrying. The attempt records `asterisk_originate_unconfirmed` and stops automatic retries and failover for that attempt.

Registration is enabled by default. Set `asterisk.register: false` for trunks without REGISTER. The provider must be able to reach the advertised pod address on UDP 5060 and 10000–10019 for SIP and RTP. With port-preserving NAT, configure forwarding to the pod and set both `asterisk.externalAddress` and `asterisk.localNet`. The chart creates a ClusterIP Service for AMI only. With NetworkPolicy enabled, list every provider network in `providerCIDRs`. The chart supplies the AMI settings to the API and worker. Remove any previous numbered Asterisk instances and call proxy settings that would route around this gateway. Restart the gateway, API and worker after rotating credentials.

Set the user's phone to digits with an optional leading `+`, enable `call` and send the test notification below. Answer and listen for “Monitoring alert. Please check your incident notifications.” Also trigger a test escalation through the worker. AMI success means the PBX queued the call. For an existing external PBX use the instructions below.


## Before you begin

Prepare an AMI account allowed to originate calls and a dialplan context and extension that handle the alert. Permit TCP 5038 from the worker and API to the PBX. Agree a test number with the recipient. [Installation options](INSTALLATION.md). [Install and configure Asterisk](https://docs.asterisk.org/Getting-Started/).

## Connect and configure

1. Set the AMI secret in `NXS_ANOMALY_ASTERISK_SECRET` and configure the remaining fields below for your PBX.
2. CHANNEL is the prefix to which the user's phone is appended.
3. Save phone on the user and enable the call target.

```dotenv
NXS_ANOMALY_ASTERISK_HOST=pbx.example.org
NXS_ANOMALY_ASTERISK_USERNAME=oncall
NXS_ANOMALY_ASTERISK_CHANNEL=PJSIP/
NXS_ANOMALY_ASTERISK_CONTEXT=oncall
NXS_ANOMALY_ASTERISK_EXTEN=s
```

Pass these settings using the service environment. With Helm use extraConfig for non-secret values and the application Secret for credentials. Recreate the affected API and worker processes after changing their environment.

To exercise the real adapter as an administrator, set API, API_KEY and USER_ID in your shell and run:

```bash
curl --fail-with-body -X POST "$API/api/v1/users/$USER_ID/test-notification" \
  -H "X-API-Key: $API_KEY" -H 'Content-Type: application/json' \
  -d '{"channel":"call"}'
```

## Operational check

On the PBX inspect AMI availability and the configured dialplan before sending a real call. In the sample the extension is s and the context is oncall. A missing extension can leave a successfully originated call without the alert audio.

```bash
asterisk -rx 'manager show settings'
asterisk -rx 'dialplan show s@oncall'
```

## Verify and troubleshoot

Send a test to that number and listen to the call. An accepted Originate confirms the handoff to the PBX. For a silent or unanswered call inspect its channel, extension and dialplan in the Asterisk log. Test each fallback PBX after changing routing.

[Escalation and delivery configuration](CONFIGURATION.md) · [Integration catalogue](INTEGRATIONS.md)
