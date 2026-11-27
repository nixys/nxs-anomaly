# Outbound delivery proxy

## Before you begin

Prepare a reachable proxy supporting the transport of the chosen channel. Determine which private destinations must be dialled directly. [Proxy transports and channel matrix](PROXY.md). [Installation options](INSTALLATION.md).

## Connect and configure

1. Set the general or channel-specific proxy variables on both API and worker.
2. The example routes Telegram through an HTTP proxy.
3. Add internal hosts to `NXS_ANOMALY_DELIVERY_NO_PROXY` where required and restart the processes.

```dotenv
NXS_ANOMALY_DELIVERY_PROXY_TELEGRAM_URL=http://proxy.example.org:3128
```

Pass these settings using the service environment. With Helm use extraConfig for non-secret values and the application Secret for credentials. Recreate the affected API and worker processes after changing their environment.

## Verify and troubleshoot

Send a Telegram test from the UI and a worker-driven notification, then inspect proxy access logs and delivery attempts. On timeouts check proxy ACLs, CONNECT support and DNS. Re-test direct destinations after editing exceptions.

[Deployment reference](DEPLOY.md) · [Integration catalogue](INTEGRATIONS.md)
