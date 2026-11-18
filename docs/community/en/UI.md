# Web panel and mobile client

The web panel calls the management API under `/api/v1` through the frontend
reverse proxy. Configure the upstream API and authentication before exposing it;
the frontend container does not replace server-side authorisation.

## Home overview

Home polls `/api/v1/overview` every 15 seconds. Open and acknowledged incident
counts, failed and retrying deliveries, integrations and on-call entries are
loaded together. A refresh error keeps the last successful snapshot visible and
shows a retry action. The snapshot may be stale until that retry succeeds.
It is a single HTTP result, not a database transaction across all sections.

## Reference lists and forms

Users, teams, schedules, integrations, escalation chains and maintenance windows
request 50 rows per page. Search runs on the server and covers the collection,
not only the visible page. User search includes name, username and email;
other reference collections search their name. An approximate count is marked
with `≈`; continue to the next page while it is available.

Reference selects search remotely, debounce typing by 250 ms and show the first
50 matches. Refine the search to find a value outside that result. Selected
values are fetched by their IDs independently of the search, so changing a
query does not remove an existing selection. Failed loads are shown in the form.

Tables scroll horizontally inside their container on small screens. Page
headers and setup cards stack their controls rather than compress their labels.
Keyboard focus remains visible; both application themes are supported.

## Sessions

The web and mobile query caches are cleared on sign-out, revoked credentials
(401) and entry into a new session. A temporary server failure (503) preserves
cached data. If the API is unreachable during identity checking, retry the
connection from the displayed recovery state.

## Mobile behaviour

Pair the phone with the server using its pairing flow. Tokens are kept in the
native keychain. A delayed 401 from an old client cannot revoke a newly paired
session. Do not share pairing codes or tokens in issue reports.

The Mine screen marks groups seen only when its tab is focused and the app is
active. Polling stops on an unfocused Mine tab. Background notification delivery
also depends on Android permissions, battery restrictions and transport setup.

List refresh and next-page errors keep existing rows visible and offer Retry.
An error loading the next page does not silently retry on every scroll event.
Group details show a loading indicator on first load. If a later refresh fails,
the cached group remains visible and its actions are disabled until recovery;
this avoids acting on a status the client could not verify.

Read queries carry cancellation signals. The mobile HTTP client uses native XHR,
keeps its native timeout and detaches abort listeners after completion.

## Validation boundaries

The local Docker acceptance suite covers real PostgreSQL/API/frontend flows,
roles, routing, incident actions, references and browser viewport widths.
Jest covers mobile state transitions and XHR cancellation. Native TalkBack,
large system fonts, notifications and background/foreground transitions still
need acceptance on an Android device or accelerated emulator before release.

See [API](API.md), [configuration](CONFIGURATION.md) and [testing](TESTING.md).
