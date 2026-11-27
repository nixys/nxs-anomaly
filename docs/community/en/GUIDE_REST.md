# Automating the management API

## Before you begin

Install an HTTP client such as curl. Obtain the HTTPS address and an API key with the minimum role for your operation. For a self-hosted client install the issuing CA in its trust store. [Installation options](INSTALLATION.md).

## Connect and configure

1. Export API as the base URL and API_KEY as the credential.
2. Read the first page of teams with the request below.
3. For larger lists advance offset by limit until the returned total is covered.

```bash
curl --fail-with-body -H "X-API-Key: $API_KEY" "$API/api/v1/teams?limit=10&offset=0"
```

## Verify and troubleshoot

Check items and total against the UI with the same access. For 401 check authentication, for 403 check role and team scope. Read the endpoint schema before writes and record the returned object ID for subsequent updates.

[Management API and permissions](API.md) · [Integration catalogue](INTEGRATIONS.md)
