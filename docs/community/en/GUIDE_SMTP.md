# Email delivery over SMTP

## Before you begin

Obtain the relay host, port, TLS mode, permitted sender address and credentials. Allow connections from both API and worker. Install the relay CA if it uses a private certificate. [Installation options](INSTALLATION.md).

## Connect and configure

1. Provide the settings below through the deployment environment and the password through its secret.
2. For STARTTLS use the relay's submission port and set USE_TLS to false.
3. Set email on the user and include email in notification_targets.

```dotenv
NXS_ANOMALY_SMTP_HOST=smtp.example.org
NXS_ANOMALY_SMTP_PORT=465
NXS_ANOMALY_SMTP_USE_TLS=true
NXS_ANOMALY_SMTP_USERNAME=oncall
NXS_ANOMALY_SMTP_SENDER=oncall@example.org
```

Pass these settings using the service environment. With Helm use extraConfig for non-secret values and the application Secret for credentials. Recreate the affected API and worker processes after changing their environment.

To exercise the real adapter as an administrator, set API, API_KEY and USER_ID in your shell and run:

```bash
curl --fail-with-body -X POST "$API/api/v1/users/$USER_ID/test-notification" \
  -H "X-API-Key: $API_KEY" -H 'Content-Type: application/json' \
  -d '{"channel":"email"}'
```

## Operational check

For an implicit TLS relay, check the handshake from the delivery network before testing with real mail. On a STARTTLS submission port add -starttls smtp and set the correct port. Verify the certificate chain and hostname.

```bash
openssl s_client -connect smtp.example.org:465 -servername smtp.example.org \
  -verify_hostname smtp.example.org -verify_return_error </dev/null
```

## Verify and troubleshoot

Send a test from the user profile and check both the delivery attempt and the mailbox. A relay acceptance followed by an empty inbox requires checking the relay queue and spam filter. For authentication failures verify `NXS_ANOMALY_SMTP_PASSWORD` and the allowed From address.

[SMTP ports, credentials and sender](CONFIGURATION.md#email-smtp) · [Integration catalogue](INTEGRATIONS.md)
