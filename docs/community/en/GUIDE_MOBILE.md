# Mobile pairing and push relay

## Before you begin

Install the project mobile client on the test phone. Give it HTTPS access to the API. For push, provide a relay that accepts the device ID, platform, `push_token`, title, severity and `group_id` and forwards the notification to your mobile provider. [Installation options](INSTALLATION.md).

## Connect and configure

1. Sign in on the web, issue a one-time mobile pairing code and redeem it on the phone.
2. Register a device push token for relay delivery.
3. Configure the relay URL below and its optional bearer credential in NXS_ANOMALY_MOBILE_PUSH_TOKEN.

```dotenv
NXS_ANOMALY_MOBILE_PUSH_URL=https://push.example.org/notifications
```

Pass these settings using the service environment. With Helm use extraConfig for non-secret values and the application Secret for credentials. Recreate the affected API and worker processes after changing their environment.

## Operational check

Run the second check with an admin API key and the paired user's ID. A successful provider response confirms acceptance by the relay. Check arrival on the device separately.

```bash
curl --fail-with-body -X POST -H "X-API-Key: $API_KEY" "$API/api/v1/users/$USER_ID/test-push"
```

## Verify and troubleshoot

Trigger a group addressed to the user and watch it appear in the open app over SSE. Separately, as admin, POST /api/v1/users/{id}/test-push and verify receipt on the phone. For skipped push inspect the relay URL and device token. Revoke the session of a lost phone.

[Pairing and session API](API.md#chatops-and-mobile-clients) · [Integration catalogue](INTEGRATIONS.md)
