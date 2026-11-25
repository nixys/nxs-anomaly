# Voice calls through Asterisk AMI

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
