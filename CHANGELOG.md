# Changelog

All notable changes to this project are documented here. The format is based on
[Keep a Changelog](https://keepachangelog.com/), and the project aims to follow
semantic versioning once it reaches 1.0.

## [Unreleased]

### Fixed
- **The web interface can change data in the Compose deployment.** The
  frontend's nginx forwarded `Host $host`, which drops the port, so every write
  after sign-in from `http://127.0.0.1:3100` failed the same-origin check with
  403 "cross-origin request rejected". It now forwards `$http_host`. Any access
  on a port other than 80/443 was affected, including `kubectl port-forward`.
- **Invalid input to the management API answers 400 instead of 500.** An unknown
  notification target type, priority, policy channel, escalation step or route
  `match_type`, an invalid route regex, and a route list without exactly one
  default returned "internal error".
- **A user's timezone is validated.** An unknown IANA name such as `Mars/Base`
  was stored as given; it is now rejected with 400, as schedules already were.
- **The community Compose file no longer publishes PostgreSQL on
  `127.0.0.1:5432`.** Nothing in the stack needs it, and `docker compose up -d`
  failed on any host already running PostgreSQL.
- **The community Quickstart works with older curl and explains the last Setup
  blocker.** The Alertmanager example used `--fail-with-body`, which curl before
  7.76 rejects; and a finished checklist still shows the backup blocker.
- **The Artifact Hub repository metadata carries the current repository ID.**
  The renamed `nxs-anomaly` repository was registered under a new ID, so the
  published `artifacthub-repo.yml` could never earn the Verified publisher mark.
- **The Artifact Hub badge in the community README points at the live package.**
  The Artifact Hub repository was renamed from `nxs-anomaly-community-edition` to
  `nxs-anomaly`; the badge link and its image now use
  `https://artifacthub.io/packages/helm/nxs-anomaly/nxs-anomaly`.
- **The GitHub release installs ORAS again.** `oras-project/setup-oras@v2`
  accepts only the versions listed in its bundled release table, which ends at
  1.3.3, so pinning 1.3.4 failed the chart job with "official ORAS CLI releases
  does not contain version 1.3.4" before the Artifact Hub metadata was pushed.
- **The community Helm chart's home and source links resolve.** The community
  cut rewrote the project URL to `github.com/nixys/nxs-anomaly/nxs-anomaly`,
  which Artifact Hub showed as the package home and which answers 404.
- **The release acceptance job creates the directory it verifies the chart
  provenance into.** `helm pull --destination` does not create it (unlike
  `--untardir`), so the check failed after a successful download with
  `open /tmp/chart-verify/…tgz…: no such file or directory`, although the
  published chart and its `.prov` were correct.
- **The GitHub release no longer fails importing the chart signing key.**
  `gpg --export-secret-keys` exports a passphrase-protected key through
  gpg-agent, which asks pinentry for the passphrase and, with no TTY on the
  runner, fails with "Inappropriate ioctl for device" and exports nothing. The
  `HELM_GPG_PRIVATE_KEY` secret already holds the binary keyring Helm needs,
  so the workflow now decodes it straight to the keyring file.

### Added
- **A Community installation guide with ready-to-use presets.**
  `docs/community/en/INSTALLATION.md` and its Russian counterpart walk through
  on-premise (systemd), Docker Compose and Kubernetes installs from one release
  tag. `deploy/quickstart/` carries the files they use: `prepare.sh` generates
  credentials into the ignored `.local/` once and refuses to overwrite them, plus
  Compose, systemd, Helm values, delivery env templates and a minimal Terraform
  example. The community README is reorganised around this quickstart, and the
  Compose file loads `.env` in the API and worker, restarts services and waits
  for health checks.
- **Artifact Hub shows nxs-anomaly as a verified publisher, with a logo.** The
  community release pushes `artifacthub-repo.yml` to the `artifacthub.io` tag of
  `ghcr.io/nixys/nxs-anomaly`, and the chart names its logo in `icon`. The
  community README carries a Terraform Registry badge.
- **The community Helm chart carries GPG provenance for Artifact Hub.** The
  existing keyless cosign signature proves *this workflow, from this repo,
  built it*, but Artifact Hub's "Signed" badge and `helm pull --verify` read
  Helm's own `.prov` format instead, which cosign doesn't produce. The GitHub
  release workflow now also signs the packaged chart with a GPG key named in
  `Chart.yaml`'s `artifacthub.io/signKey`, and the `.prov` rides the same OCI
  push as an extra layer — no separate publish step. Documented in
  `packaging/community/SECURITY.md`'s "Helm chart provenance" section,
  including the key generation/rotation runbook. **Not yet active**: needs the
  actual key generated (maintainer runbook, not done from CI), the
  `HELM_GPG_PRIVATE_KEY`/`HELM_GPG_PASSPHRASE` GitHub secrets set, the public
  key committed to `packaging/community/assets/`, and `signKey` filled in with
  the real fingerprint — until then the next tag's release job will fail at
  the "Import the chart signing key" step.

- **On-call quality report: a scheduled digest, not another dashboard.**
  `nxs-anomaly run-report` (weekly by default, Helm CronJob
  `templates/enterprise/oncall-report-cronjob.yaml`) builds a per-team — or
  installation-wide — summary over the trailing 7 days from the same
  ClickHouse views the analytics dashboards use: missed ACKs, channel/delivery
  problems, night load, repeat/exhausted escalations and noisy alert sources.
  Every named offender carries its `alert_group_id`/`episode_id`, so the
  digest names a case to look up rather than asking to be trusted. Delivered
  as an email through the normal notification/retry pipeline (no bypass — see
  `ProcessScheduleShiftNotifications` for the same synthetic-notification
  shape this reuses), retrievable via `GET /api/v1/reports[/{id}]` and the
  ChatOps `report` command. Idempotent by (team, period): a manual re-run or
  an overlapping CronJob execution returns the report already on file instead
  of sending a second copy. This is also the first path in the product that
  reads ClickHouse directly — everywhere else it is written by Vector and read
  only by Grafana. See `docs/enterprise/ru/API.md`'s "Отчёты о качестве
  дежурств" section.

- **Incident-response analytics: ten dashboards, and the schema that makes them
  honest.** The single 23-panel dashboard is split into one file per question in
  `docs/enterprise/grafana/` — overview, response latency, backlog, noise,
  delivery, escalation, on-call load, schedule coverage, an episode explorer, and
  data quality. Import `10-analytics-data-quality.json` first: an empty response
  chart is produced both by a quiet week and by a consumer that stopped three days
  ago, and only that one tells them apart. New views back them —
  `nxs_episode_fact` (the response with its service, severity and a single
  terminal outcome), `nxs_notification_fact` (per notification rather than per
  provider call), `nxs_group_state_latest` (what is unfinished now),
  `nxs_response_objectives` (targets as data, per service and severity, with a
  validity interval) and `nxs_analytics_health`. Rationale, metric dictionary and
  what the data cannot answer: `docs/enterprise/ru/INCIDENT_ANALYTICS_DASHBOARDS.md`.

- **A delivery attempt now says what it was for.** `purpose` on
  `notification.delivery_attempted` separates a page from a resolution notice, a
  shift handover, a test click in the UI and a line written to the group log.
  Without it every delivered message counted as a page, so pressing "test"
  improved time-to-first-page.

- **A group silenced by a maintenance window now emits the silence.** The
  suppression was applied at group creation without an event, so an episode
  suppressed on purpose was indistinguishable in ClickHouse from one nobody
  answered.

### Fixed
- **Target attainment counted only the episodes that were answered.** The share
  left out exactly the incidents nobody responded to, so it improved every time
  one dragged on. It is now computed over a matured cohort: an episode whose
  deadline has passed unanswered is late, one whose deadline has not passed is
  pending, and neither is a success. `nxs_response_attainment` reports the counts;
  shares are `sum(numerator)/sum(denominator)`, never an average of per-team
  shares — one team answering 1 of 1 and another 0 of 99 is 1%, and averaging the
  two says 50%.

- **Percentiles were being averaged.** Panels read `avg(p95_…)` across teams and
  days, which is not a p95. The daily views now carry mergeable
  `quantileState` columns beside the plain percentiles, and every panel that spans
  more than one team or day reads those through `quantileMerge`.

- **An episode replaced by the next pass at its group stayed unresolved forever.**
  It is now `superseded`, which is a terminal outcome rather than permanent
  backlog.

- **The first delivered attempt was treated as the first page**, so a resolution
  notice or a test message set the page time. `first_paged_at` now requires
  `purpose = 'page'`, and events published before that field existed report
  unknown rather than being promoted to pages.

- **A group opened and silenced in one transaction read as either state.** Both
  events share a millisecond, and ordering by time alone picked one arbitrarily;
  the state is now resolved by (time, position in the lifecycle).

- **A late resolution never rebuilt its rollup.** The response rollup is keyed by
  the day an episode opened, and the trailing window covers recent days only — an
  episode opened five weeks ago and resolved this morning kept a row saying it was
  never resolved. The rebuild now also names the opening days of episodes that
  moved since.

- **Stage intervals are guarded on their own ordering.** An acknowledgement
  timestamped before the opening produced a negative MTTA that quietly dragged the
  average down; such an episode now contributes NULL and is counted on the data
  quality dashboard instead.

- **Delivery rows carried no episode or team**, so a person paged twice about one
  group in two separate episodes collapsed into one interruption and a team filter
  silently showed global delivery numbers. Both ids are exposed, and the schedule
  coverage view gained `report_id` so the per-check totals — which repeat on every
  schedule row — are taken once.

- **Both READMEs open with the product lockup.** `frontend/logo/horizontal.png` is
  the mark beside the name — the shape a document header wants, and the one the app
  chrome must not have, since the header and the sign-in screen set the name as real
  text that can be selected and read aloud.

- **The product has its mark, and the browser tab has an icon.** The logo was a
  placeholder drawn in code — a blue rounded square with a white zigzag — and there
  was no favicon at all, so the tab a responder is meant to pick out among twenty
  others at three in the morning carried the browser's blank glyph. The mark is a
  bell whose frame draws an A; `frontend/logo/` holds the sources and
  `frontend/public/` what ships. Only the mark is rendered: the shell header and
  the sign-in screen already set the product name in text beside it, and a lockup
  with its own wordmark would print the name twice.

- **A responder can now silence an alert from the chat, and take a mis-tap back.**
  Acknowledge and resolve were the only two buttons, and neither is the answer to
  noise at three in the morning: one claims the incident is being worked, the other
  claims it is over. Every alert now also carries `Silence 1h / 4h / 8h`, and a
  settled message keeps exactly one button — the way back (`Undo acknowledge`,
  `Reopen`). `SilenceGroup`, `UnacknowledgeGroup` and `UnresolveGroup` already
  existed in the engine and in the web UI; only the chat had no way to reach them,
  so a mis-tapped Resolve from a phone could only be fixed by opening the browser.

- **The Mattermost bot answers commands.** It had one endpoint — the button
  callback — so `status`, `alerts`, `duty`, `oncall` and `priority` were reachable
  from Telegram and Slack and from nowhere else. `POST
  /integrations/v1/chatops/mattermost/command` accepts a slash command,
  authenticated by the token Mattermost issues
  (`NXS_ANOMALY_MATTERMOST_COMMAND_TOKEN`). One registration serves every command:
  the trigger word is dropped and the arguments are the command, so `/nxs ack grp_1`
  runs `ack grp_1`.

- **A Slack or Mattermost action is attributed to the person who took it.** Identity
  was resolved by `telegram_id` alone, so every tap on those two platforms ran as the
  service principal and the group's log named a bot. Users carry `slack_id` and
  `mattermost_id` next to `telegram_id` (both editable in the UI, both erased by a
  data-erasure request), and the commands that need a person — `duty`, `priority` —
  work in those chats instead of refusing with a message about Telegram.

- **`/alerts` separates opening a group from acting on it, and offers the storm case.**
  A row used to be one button labelled with the group's title whose action was
  acknowledge — a label promising navigation and a tap changing state, with no
  confirmation. Each row is now the label, which opens a card, and a narrow `✓`.
  Below the rows, `Acknowledge all open` and `Silence all 1h` act on every open group
  the caller can see, for the forty-groups-from-one-cluster-failure case that acting
  one at a time cannot serve.

- **`/start`, a link to the group's page, and shift buttons that know their schedule.**
  The bot answered `/start` with "unsupported chatops command"; it now greets, offers
  the three things a responder opens it for, and says plainly when the chat account is
  not linked to a user. Alerts carry an `Open in nxs-anomaly` link when
  `NXS_ANOMALY_PUBLIC_URL` is set, and a shift notice now offers `Take this shift` and
  `Who is on call` alongside the check-in — all three needed a schedule id nobody
  remembers at 09:00 on a Monday, and the notice knows it.

### Fixed
- **Acting on an alert from Slack or Mattermost no longer deletes it from the channel.**
  Both replaced the message with the verdict alone — "Acknowledged grp_a1b2c3d4e5f6" —
  so the incident a channel had been reading became an identifier, which nobody reads.
  They now do what Telegram already did: keep the text, append the verdict, drop the
  buttons. On Mattermost the update deliberately names no message at all, which is what
  leaves the post as it stands.

- **A Mattermost alert with buttons no longer shows its text twice.** The payload put
  the same text in the post and in its attachment, and Mattermost renders both.

- **A refused or unknown command in a chat gets an answer.** The webhook replied with a
  JSON error object and a 4xx. Telegram ignores any body that is not a method call, so
  the person saw nothing — and read the non-2xx as a delivery to retry, so the refused
  command was redelivered. Refusals are now answered with 200 in the platform's own
  shape, which is the invariant the button path already held.

- **The frontend test suite no longer exits non-zero with every test passing.** jsdom
  implements no scrolling, so `Element.scrollIntoView` does not exist; the alert-group
  list and Mantine's Combobox both call it, and Mantine's call fires on a timer after
  its test has finished, landing the throw outside any test. Stubbed alongside the
  `matchMedia` and `ResizeObserver` stubs already there — a gap in the test environment,
  not in the product.

- **An alert whose source called it `high` is no longer invisible to every filter and
  sorted below `debug`.** Three places disagreed about what a severity is: the filter
  offered five words, the badge coloured eight, and the server's ranking knew five, so a
  group stored as `high`, `medium`, `P1` or `sev2` was painted, could not be selected,
  and sorted to the bottom of a list ordered by importance — silently, in the one list a
  responder scans first. There is now a single ladder — critical, error, warning, info,
  debug — and a table of the spellings that mean each level, defined once in
  `internal/store` and mirrored for colour in the frontend. Filtering by a level matches
  every spelling of it, ordering ranks them together, and a word the service does not
  model keeps its own name, ranks last and is matched exactly rather than guessed into a
  level it may not belong to.

- **The timeline of a group reads as what happened, not as the rows that store it.**
  Every entry was titled "event" and followed by its own plumbing — `actor` as JSON, the
  log id, the wire type — with the one sentence that mattered buried among them. Entries
  are now named in the reader's language, newest first, with the person credited when a
  person is behind the action, the few data fields that change an entry's meaning shown
  as fields, and delivery or configuration failures marked. The raw rows did not go
  anywhere: they are in the Raw tab, which is where they were always meant to be read
  from. The `notified` entries also carry their recipients as data rather than only
  inside a pre-rendered English sentence, so a Russian UI can name them too.

- **A repeat firing no longer executes the escalation step a `WAIT` is still counting
  down to.** Every accepted alert ran the chain from the group's stored position, so an
  alert arriving on a group already parked behind a `WAIT` ran the step the wait was
  counting down to. With the chain `WAIT 10 minutes → RESOLVE`, a source repeating the
  same alert half a second after the first one closed the incident half a second after it
  opened, and the firing after that had to open a second group for an incident that had
  never stopped. The chain's position is a promise about time and the timer
  (`next_run_at`) belongs to the worker; ingest now advances escalation only for an alert
  that opens a group.

- **A repeat firing no longer clears an acknowledgement.** An acknowledged group returned
  to `open` on the next alert with the same dedupe key, which for a source that re-sends
  on a timer meant paging the person who had just answered, on every repeat. The default
  is now that the acknowledgement stands; `NXS_ANOMALY_REOPEN_ACKED_ON_NEW_ALERT=true`
  restores the previous behaviour for deployments whose sources fire only on genuinely
  new events. A group that does reopen now restarts its chain from step zero, the way an
  unresolve already did, instead of resuming at the position the acknowledgement stopped
  it at — resuming there was the same early-step bug wearing a different hat.

- **Concurrent ingests no longer overwrite each other's group state.** Each request built
  its update on a copy of the alert group read *before* the per-integration advisory lock,
  so two requests inside the lock in turn both started from the same pre-lock snapshot and
  the second wrote back a group that had never seen the first: twenty accepted alerts left
  a group reporting `alert_count` 10 and 12 across runs, with `alert_ids` short by the same
  amount, while all twenty alert rows were stored. The rows were never lost — the
  aggregate the counters, the group view and the epic threshold read was. The group is now
  resolved only from the state loaded under the lock, and the pre-lock lookup that fed the
  stale copy is gone (one query less per alert).

- **A group its own escalation chain resolved during ingest now closes its alerts.** Only
  the ingest result "resolved" — a resolving event from the source — carried the status
  down to the group's alerts, so a group closed by a `RESOLVE` step reached during the
  ingest kept alerts marked `firing` forever, on a group nobody would touch again. The
  group's final status is what decides now, not the per-alert result.

  Regressions for all four run over real HTTP against a real PostgreSQL
  (`tests/ingest_live_test.go`), because none of them is reachable otherwise: two need the
  group row to survive between requests, and one needs two requests inside the ingest at
  the same time.

### Changed
- **Community falls back to English, Enterprise to Russian.** The interface
  still follows a stored choice and then the browser; only when the browser asks
  for neither supported language does the edition decide. Community, published
  for everyone, now opens in English there instead of Russian.
- **The delivery log is about incidents again.** "Notifications" showed twenty-one rows
  differing only in the recipient, with the incident behind each one reduced to a link
  labelled "Open", a target column of dashes, a retries column of zeros and the same
  absolute timestamp repeated to the second. Rows now carry the incident's title and the
  reason it was sent, time is relative with the exact moment on hover, and a column whose
  every value on the page is empty is not drawn at all. Filters live in the address bar,
  so a filtered delivery log can be handed to somebody.

- **An escalation chain reads as a sentence, and can say who it would page right now.**
  The card printed the wire names of the step kinds — `1. NOTIFY_USER  2. NOTIFY_USER` —
  which is the configuration that decides whether anybody is woken, displayed as constants
  from the source, with the one fact that matters missing: who. It now reads "page Ada
  Okonkwo → wait 5 min → page the Platform team", marks any step that names nobody, says
  outright when a whole chain reaches nobody, and offers a dry-run that resolves the
  notifying steps against the current rotas without sending anything.

- **Insights answer whether things are getting better or worse.** The screen fired twelve
  list queries for their `total` — twelve round trips to draw six numbers — and could not
  show direction at all. One request now returns the counts and a daily trend, drawn as
  two charts (incidents opened and closed; deliveries delivered and failed) rather than one
  chart with two scales. The severity distribution counts by level, so it agrees with the
  badges below it.

- **A rota is a grid before it is a table.** Four weeks of shifts are drawn as bands per
  day — holes are gaps, an override is the same band with a different surface, a double
  shift is one colour across two rows — with the exact intervals still tabulated below,
  because that is what somebody quotes in a handover.

- **Movement where it explains something.** A row that arrived since the last refresh is
  highlighted for 2.4 s; a status badge acknowledges its own change; tiles count to their
  new value instead of swapping it; the bulk bar slides in with the selection; hover and
  focus colours take 120 ms instead of none. The severity stripe, the level badge and the
  numbers in tables never animate — those are what the eye compares. `prefers-reduced-motion`
  removes all of it rather than shortening it, and toasts moved to the bottom right, where
  somebody working a table is actually looking.

- **Waiting looks like the thing that is coming.** Lists wait behind a skeleton shaped like
  their rows rather than a centred spinner, and the skeleton only appears after 200 ms so a
  fast answer does not flash. Polling stops while a tab is hidden.

- **Destructive row actions moved behind the overflow menu**, so a red trash icon no longer
  sits a few pixels from "edit" with no label, and the notification-priority labels no
  longer share the word "medium" with a severity spelling.

- **Every route object the chart renders takes an explicit name: `ingress.name`,
  `istio.virtualService.name`, `gatewayAPI.httpRoute.name`.** Their names were always the
  release's full name, which is fine until two releases publish through one controller or
  put routes in one namespace — a community and an enterprise install side by side, or one
  release per team. Both then render the same object, and a GitOps controller reports it
  as belonging to two applications and reconciles it back and forth
  (`VirtualService/nxs-anomaly is part of applications argocd/nxs-anomaly-ce-team-x and
  nxs-anomaly-ee-team-x`). The cross-namespace `ReferenceGrant` that an HTTPRoute needs
  follows the route's name, so the pair stays together. The defaults are unchanged,
  because renaming a route is not cosmetic: the old object is deleted and the new one
  created, and traffic to the host stops in between.

- **The sidebar collapses to a rail of icons and remembers that it did.** On a 13" laptop
  the navigation was 240 px of permanent furniture next to a nine-column table; it now
  narrows to 64 px — icons in the same left-hand position they occupy when expanded, so
  the labels slide out from behind them rather than the whole column moving, section
  headings cross-fading into the rules that keep the grouping visible, and a tooltip on
  each icon. The toggle sits at the foot of the sidebar, `⌘B` does it from the keyboard,
  and the choice is remembered per browser and read before the first paint, so a reload
  opens at the width it was left at instead of animating into place. Width and content
  edge move on one 260 ms curve that decelerates into its stop; labels leave in 110 ms
  and arrive over the last 160 ms, so text is never squeezed while still readable, and
  `prefers-reduced-motion` turns the movement off entirely. Below the navbar breakpoint
  nothing changes: there the sidebar is a drawer the burger opens, and a 64 px drawer is
  not a navigation.

- **The alert-group list is a queue you can work from the keyboard, hand over by link,
  and read on a phone.** Filters, sorting and the page number now live in the address
  bar, so a filtered list is a link somebody can paste into a handover instead of a state
  that dies with the tab; four named views (Firing, Critical, Being worked, All) replace
  three empty dropdowns as the first thing on the page, and a bare `/alert-groups` opens
  on the firing queue rather than on everything ever recorded. Each row carries a
  severity stripe and how long the incident has been burning, which is the question a
  queue is scanned to answer and which "last alert 42 seconds ago" never answered. The
  bulk actions appear when something is selected instead of standing permanently
  disabled, `j`/`k`/`x`/`a`/`r`/`Enter` work on the row under the cursor, `⌘K` opens a
  palette over every page, and below the navbar breakpoint the nine-column table becomes
  a list of cards rather than a horizontal scroll. Auto-refresh says when it last ran and
  can be paused while somebody reads.

- **The group page leads with what a responder decides on.** Twelve equally weighted
  fields became a header — severity, status, title, how long, which integration, which
  chain, when the next escalation fires — with the rest one click away; the integration
  and the escalation chain are named and linked instead of shown as `int_8be95880d83d`.

- **The sidebar is grouped into Respond, On call, Routing, Review and Installation.**
  Seventeen equal-weight links were a list nobody read to the end. The pages did not
  change, only the claim that they are all the same kind of thing.

- **An empty list says which emptiness it is and what to do about it.** "No alert groups
  match these filters" was shown to installations that had no integrations at all; the
  page now separates a filter that matched nothing (offering to clear it) from a product
  nobody has connected a source to yet (offering to connect one).

- **The Kafka analytics relay writes a batch of outbox events in one request instead of
  one request per event.** The publish was synchronous per message and `kafka-go` waits
  out its 10 ms batch timeout on each one, so a drain cost roughly a second per hundred
  events and a cycle hit its 2-second budget after about 200 — the queue-drain fix of
  0.1.81 could only go as fast as the transport under it. A batch of 100 now costs one
  write (~10 ms). What changes with it: a failed write says nothing about which of its
  messages the broker accepted, so none of that batch is deleted from the outbox and all
  of it is published again on the next cycle. Consumers may therefore see duplicates
  where they previously would not have — analytics delivery has always been
  at-least-once, and nothing is lost. Batches already published earlier in the same cycle
  stay published. The message type is defined by the engine, next to the interface it
  hands to a producer, so that the edition split keeps working: `internal/kafka` is
  removed wholesale from the community tree, and a type living there could not be named
  by code that stays.

- **The Kafka analytics relay drains the outbox every cycle instead of one batch of
  100.** The queue could only shrink by a hundred events per worker cycle however far
  behind it was, so a burst outran it: measured on a live installation, ingest reached
  ~600 alerts/s across four integrations while the relay moved ~80 events/s, and 3000
  events peaked at a backlog of 2348 that took another ~30 s to reach ClickHouse. Nothing
  was ever lost — the outbox is durable and the consumer caught up — but the analytics
  tables answered questions about a state of the world minutes old, and the lag grew with
  the burst. One cycle now keeps taking batches until the queue is empty, bounded at 2
  seconds or 5000 events so that escalation and delivery, which share the same worker
  cycle, are not held behind the relay; whatever is left goes out on the next cycle.

- **One naming policy for every release artefact: the edition is the last name
  segment.** The enterprise edition publishes `nxs-anomaly`,
  `nxs-anomaly-frontend` and the chart `nxs-anomaly`; the community
  edition publishes the same three names without the suffix. The frontend is built and
  published per edition even though its sources are identical in both — it carries no
  edition-specific code, it asks the server what it may show — because an operator reading
  a manifest should not have to remember that one of the three artefacts follows a
  different rule. Charts had to stop sharing coordinates for a harder reason: the two
  editions render different contents, and one name and version resolving to two different
  charts makes mirroring, caching and signature provenance ambiguous.

  The chart name does **not** reach the objects the chart creates. `values.yaml` now pins
  `nameOverride`, so resource names and `app.kubernetes.io/name` — the immutable
  Deployment selector — are identical in both editions. Without that pin, renaming the
  chart would have renamed every object and changed a selector helm is not allowed to
  change: moving a release from one edition to the other would have failed as an upgrade
  and orphaned every PersistentVolumeClaim. With it, the move stays what it was designed
  to be — swap the image repository, add the pull secret, point at the other chart, and
  the schema underneath is the same.

### Added
- **OpenSearch Alerting, Kibana Rules and Elasticsearch Watcher as alert sources.**
  `POST /integrations/v1/opensearch/{key}` and `POST /integrations/v1/elasticsearch/{key}`,
  with `normalizeOpenSearchAlert` and `normalizeElasticsearchAlert` behind them. None of
  the three products has a webhook format of its own — every one of them posts a Mustache
  template the operator wrote, and OpenSearch's default template is not even JSON — so the
  shape is ours and ALERT_PROCESSING.md §2.6–2.7 carries the template to paste into each.
  What a template cannot express is what these endpoints are for: the plugin's inverted
  `1`…`5` trigger severity translated onto the five levels the on-call queue sorts by, a
  dedupe key built from monitor and trigger ids (plus bucket keys) rather than from the
  tail of whatever field was handy, recovery recognised from OpenSearch's `COMPLETED` and
  Kibana's `recovered` action group, and a bucket-level monitor's several buckets ingested
  as one envelope in one transaction under one advisory lock, the way an Alertmanager
  envelope is. The Elastic side defaults to severity `warning` rather than `unknown`
  because neither Kibana rules nor Watcher have a severity of their own, and `unknown`
  ranks below every known level — the default would have put these alerts at the bottom of
  the on-call queue.
- **Two editions, cut from one tree.** The service now builds as a community edition
  (everything but single sign-on, team boundaries and the Kafka analytics stream) or as
  the full one, from the same sources: `go build -tags community ./...` produces the
  first, a plain build the second. What belongs to which edition is written in the files
  themselves — Go carries build constraints, everything else carries
  `nxs:enterprise:begin`/`:end` marker comments — rather than in a list somewhere that
  would drift the first time a file was added. `scripts/make-community.sh` generates the
  public tree from those two mechanisms and `scripts/check-community-cut.sh` verifies it;
  two CI jobs build and test the generated tree on every commit, so a change to shared
  code that quietly depends on the closed half fails on the commit that made it rather
  than on release day. The seams the split needed were mostly already there: the Kafka
  producer sat behind an interface and every analytics call site already went through one
  helper, so the community build turns the whole stream off by returning its argument
  unchanged. Two things the cut had to move first, and both were bugs waiting: the shared
  sign-in helper `startSession` lived in the OIDC handlers, and the shared test actor
  `adminCtx` lived in an analytics test — either would have taken the other edition down
  with it.
- **The edition is now something the product reports rather than something you infer.**
  `/health` and `/api/v1/auth/methods` carry an `edition` field, and a new authenticated
  `GET /api/v1/capabilities` reports each gated feature as `available`,
  `unavailable_in_edition` or `not_configured`. Three states, not two: an operator who has
  not set an issuer can fix that, and an operator on an edition without the provider
  cannot, and telling both the same thing wastes the time of one of them. The sign-in
  screen uses it to show single sign-on **disabled with an explanation** instead of
  silently drawing one fewer button — somebody arriving from an installation that had SSO
  would otherwise read the gap as a broken deployment. An enterprise build with SSO merely
  unconfigured stays silent there on purpose: that is the operator's problem, not the
  problem of the person trying to sign in. The OIDC endpoints answer `501` in the
  community build and keep their `404 not configured` in the enterprise one, so a client
  can tell the two apart; the operations are marked `x-edition: enterprise` in the spec.
  The readiness report no longer advises setting `NXS_ANOMALY_TEAM_SCOPING=true` in a
  build that reads no such flag — advice that does nothing costs the reader a trip to find
  that out.
- **Ordering on the list endpoints, and a sort control on the alert pages.** Every
  paginated list took `limit` and `offset` and nothing else: it was `ORDER BY id`, and ids are random
  hex, so an "alert list" arrived in an order that meant nothing and changed nothing when
  new alerts came in — the group that had just fired could be on page four. Lists now
  order by time where time is what a person reads them by (alert groups by
  `last_received_at`, alerts by `received_at`, notifications by `created_at`, newest
  first) and accept `?sort=<column>&order=asc|desc` to change it. The column is validated
  against the collection's own columns, so an unknown name is a `400` rather than a page
  that looks sorted and is not; id is always the final sort key, because without a
  tie-break an `OFFSET`-paged listing can show one row twice and hide another. The alert
  and alert-group pages carry the picker and a direction toggle, and sorting happens in
  the query — sorting the 25 rows that arrived would only reorder the slice the server
  picked. `severity` is ranked rather than spelled: alphabetically "critical" sorts
  between "alert" and "debug", which would bury the alerts somebody has to answer now in
  the middle of the page.
- **A database restart made senders drop alerts instead of retrying them.** Any store
  failure on the ingest path became `500 internal error`, including "PostgreSQL is not
  accepting connections". The status code is what decides whether an alert survives:
  Alertmanager, Grafana and webhook senders back off and retry a 503, while a 500 reads
  as "this request is broken". Measured by restarting PostgreSQL under live ingest — 11
  of 60 alerts came back 500, each an alert the sender had no reason to send again.
  Ingest now answers `503` with `Retry-After` when the database is unavailable, judged by
  the two classes actually observed: a dial that never completed, and a SQLSTATE from
  class 57 (operator intervention, e.g. 57P01 "terminating connection due to
  administrator command" — what a rolling restart produces) or class 08. Constraint
  violations, syntax errors and permission denials stay 500: retrying those returns the
  same answer.
- **Ingest cost grew with the integration's open-group backlog.** Every ingest loaded
  every unresolved group of its integration inside the advisory lock, so the in-mutator
  dedupe re-scan could find one by key. Measured on a live contour: 16.6ms per alert at
  50 open groups, 148.1ms at 800 — a straight ~0.174ms per group, making a burst of n
  alerts cost O(n²), and degrading exactly during an incident, when open groups are many
  and alerts arrive fastest. Resolving those 800 groups and re-measuring the same
  integration returned it to 17.0ms, which is what identified the load as the cause
  rather than a correlate. The two collections are now loaded by the keys the envelope
  actually carries — the groups by `dedupe_key`, the batches by `batch_key`. It is the
  same question asked more cheaply: nothing else reads those collections on this path,
  groups created earlier in the same envelope are in state regardless of what was
  loaded, and a concurrent ingest cannot be inside the lock we hold. No migration —
  `nxs_anomaly_alert_groups_active_lookup_idx` has served this shape since 0002 and this
  path had simply been ignoring it.
- **A database still starting up could fail the connect retry it was written for.**
  `isRetryableConnectError` matched `connect: connection reset`, the reset seen while
  dialling, but a PostgreSQL that has begun listening and is not yet serving resets
  during the startup handshake instead — `read: connection reset by peer`, which did not
  match. So the retry budget the docs recommend for `serve` and `run-worker` did nothing
  in the one case it exists for. Found when four load-test profiles in a row died at
  store init with no report at all, looking like a product failure rather than a database
  that needed another second.
- **NetworkPolicy left the frontend unreachable and unable to reach the API.** The
  default-deny selects every pod in the release, and the only allowances were for the
  API, the worker, the consumer and the datastores — the frontend had neither an ingress
  rule nor an egress one, so turning `networkPolicy.enabled` on cut the browser off from
  nginx and nginx off from the API it proxies `/api/` to. The file's own header comment
  promised "ingress to the frontend/API HTTP ports" and only the API half existed; the
  kind NetworkPolicy e2e never loads the UI, so nothing caught it. The frontend now gets
  its own policy: egress to the API service port, and ingress on its HTTP port from
  `networkPolicy.extraIngress` — emitted only when that list has entries, because an
  ingress rule with an empty `from` matches every source rather than none, the same trap
  the worker rule already documents.
- **An ingest pipeline that parses and enriches an alert before anything is decided about
  it.** `pipeline` on an integration is a list of stages, each one action with an optional
  `if`: `extract` (named regex groups into labels — grok, roughly), `set`, `rename`,
  `remove`, `gsub`, `truncate` and `drop`, addressing `title`, `message`, `severity` and
  `label:<name>`.
  It runs after route selection and before everything else. Routing deliberately stays
  upstream: enrichment is edited constantly and by many hands, and if it fed route
  selection then every such edit would change which rota gets woken, made by someone who
  was not thinking about paging. The route follows from the alert as the source sent it,
  so an integration's routes tell the whole story of where its alerts go. Everything
  downstream sees the enriched alert — the dedupe key and therefore grouping, the stored
  labels, the notification text — which is where the useful work is: cutting a build id
  out of a title so repeated failures collapse into one incident, adding a pod and
  namespace the source never stated. What it gives up is repointing an alert at a
  different escalation chain, which is the point.
  Deliberate limits, because this sits on the hottest path in the service: 32 stages, a
  512-character pattern, no regex stage over a 16KB field, 32 named groups per extract,
  and compiled patterns cached across alerts. A stage that cannot run is skipped and
  logged rather than failing the alert — an alert delivered unenriched is cosmetic, an
  alert not delivered is a missed incident. `extract` does not overwrite what the source
  stated, an unconditional `drop` is refused because it silences an integration that
  keeps answering 202, and two actions in one stage are refused because their order would
  not be visible in the config. A pipeline that does not compile is a 400 when the
  integration is saved. `POST /api/v1/routes/debug/{key}` previews it — labels before and
  after, what was added, changed and removed, and the title on both sides.
- **Liveness that detects a stalled analytics consumer.** Vector's disk buffer can
  stop noticing events it holds: they sit in the buffer, the sink never pulls them and
  never even dials ClickHouse, and nothing is logged. It is an open upstream bug
  (vectordotdev/vector#22946, #10806) whose only workarounds are "restart" or "send
  another event", so a quiet stream is the worst case — and the old probe, an httpGet on
  /metrics, answers 200 throughout. That combination gave two and a half days of an
  analytics leg reporting Ready on a dev cluster while nothing reached ClickHouse, and a
  recurring CI flake where exactly two events were ingested and then nothing.
  Liveness now asks about the buffer instead of the port: a sink holding buffered events
  that has never taken a single one fails the check, and the pod restarts, which is the
  documented recovery. `component_received_events_total` counts events pulled out of the
  buffer before the insert is attempted — verified against a live consumer with
  ClickHouse scaled to zero, where it tracked buffer depth rather than staying at 0 — so
  this cannot fire on a sink merely retrying a failing insert, nor on an idle deployment
  whose buffer is empty. Being a counter it latches, so there is no restart loop. Turn it
  off with `kafkaConsumer.stallProbe.enabled=false`.
- **The chart gives `serve` and `run-worker` a budget to wait for the database.** The
  application defaults to fail-fast so one-shot CLI commands and tests do not hang on a
  database that will never come up; in a Deployment, where pod start order is arbitrary,
  that means the API and worker exit(1) on any install or rollout that outruns PostgreSQL
  and recover only through CrashLoopBackOff — one API restart and two worker restarts on
  an observed rollout. `NXS_ANOMALY_DB_CONNECT_MAX_WAIT_SECONDS` now defaults to 60 in
  the chart; set it to "0" for the old behaviour.
- **ServiceMonitor and PrometheusRule are guarded on the Prometheus Operator CRDs.**
  Without the guard, enabling either flag on a cluster that has no operator emits a
  manifest of an unknown kind, and a GitOps controller then fails to sync the *entire*
  release — one observability toggle takes the whole application out of reconciliation.
  That is why the flags had to stay off on a dev contour that would otherwise want them.
  They now render nothing where the CRDs are absent and start working by themselves once
  the operator is installed, with no values change.
- **The load suite gained the axis it lacked and a per-profile drain budget.**
  `-integrations` spreads the alerts across several integrations; every profile until now
  used one, so the suite only ever measured a single hot path even though ingest
  serialises per integration. The drain timeout moved from one constant to a property of
  each profile: `provider_timeout` holds ~20% of 4500 deliveries behind a 2s provider and
  fit inside 60s only because ingest used to be slow enough to spread the work out — once
  ingest got faster the same run delivered 1335/1500 in 60s and 1500/1500 in 180s, losing
  nothing. An acceptance criterion that depends on the producer being slow is a
  coincidence, not a criterion.
- **Alerts for the analytics leg (Kafka → Vector → ClickHouse).** Every rule shipped so
  far watched the Go service, so a consumer that stopped consuming was invisible: ingest,
  escalation and delivery all stay green while nothing reaches ClickHouse. Observed on a
  dev cluster where the consumer sat unable to read its topic for two and a half days
  with the pod reporting Ready throughout — its probes hit `/metrics`, which answers 200
  regardless of what the Kafka source is doing. `AnalyticsConsumerDown`,
  `AnalyticsConsumerErrors` and `AnalyticsConsumerStalled` cover it; the last compares
  alerts arriving against events consumed, because silence on either side alone is a
  quiet period rather than a fault, and it stays quiet where Vector is not deployed at
  all.
- **A guard that refuses to run the integration suite against a database somebody else is
  using.** The suite seeds notifications and runs worker cycles in-process, expecting its
  own stand-ins to receive the deliveries; a deployed worker on the same database claims
  them first and delivers them from wherever it runs. What that looked like was seven
  unrelated delivery tests failing with nothing tying them to the cause. The suite now
  reads the worker heartbeat it already writes for readiness and stops in under a second
  with an explanation.
- **`NXS_ANOMALY_TEST_TIMEOUT_SCALE`** multiplies the suite's wall-clock deadlines. They
  are tuned for a co-located PostgreSQL, which is what CI provides and what production
  never is: measured against a cluster PostgreSQL over a forwarded port, one query costs
  ~6.6ms instead of ~0.6ms and the suite takes 81s instead of 22s, so tests fail on the
  clock and the failure reads like a product bug.
- **Objects created by Terraform are marked and cannot be edited in the UI.** Users,
  teams, schedules, escalation chains, integrations, ChatOps channels and maintenance
  windows created by a request that identifies itself as an infrastructure-as-code tool
  carry `provisioned_by`, and updates and deletes from anyone else are refused with
  `403`. Terraform's contract is that the file is the truth: an escalation chain edited
  in the web UI at 02:00 works until the next apply and then quietly goes back to paging
  the wrong team, with nothing on screen ever having said the edit would not last. The
  web UI shows a badge next to the object's name and disables its own edit and delete
  controls, so the refusal is visible before it is hit. Detection is the `User-Agent`
  every terraform-plugin-sdk request carries, or an explicit
  `X-Nxs-Anomaly-Provisioner` header — which is also the escape hatch for an object whose
  definition no longer exists and which somebody now has to clean up by hand. It is not a
  permission and never widens what a caller may do. Deliberately untouched: reads,
  alert-group actions (acknowledge / resolve / silence), schedule overrides — covering a
  shift tonight is what the rota is for — and routing-key rotation, which is the answer
  to a leaked key and which Terraform has no action for.
- **Outbound delivery through a proxy.** A deployment whose network cannot reach a
  provider directly — a blocked messenger, or egress permitted only through one audited
  hop — previously had no way to page anyone: the notification failed at dial time,
  retried, and dead-lettered. `NXS_ANOMALY_DELIVERY_PROXY_URL` now sends every delivery
  channel through a proxy, `NXS_ANOMALY_DELIVERY_PROXY_<CHANNEL>_URL` overrides it per
  channel (or opts one out with `direct`), and `NXS_ANOMALY_DELIVERY_NO_PROXY` keeps the
  internal receivers direct. `http`, `https`, `socks5`, `socks5h` and `tcp` are supported;
  SOCKS always resolves the destination at the proxy, because a network that blocks a
  provider usually poisons its name too. Chart section `deliveryProxy`; a URL with a
  password is refused there rather than rendered into a ConfigMap.

  `tcp` is a transparent relay rather than a proxy — the shape an operator gets from an
  HAProxy `mode tcp` frontend forwarding to the provider. Such a hop speaks no protocol
  and does not terminate TLS, so only the dial address changes: the request keeps the
  provider's name, and the `Host` header, the TLS SNI and the certificate check still
  validate against the provider, with no verification disabled. It needs an explicit
  port, and it is blind to the destination — a channel whose address comes from the
  escalation chain (`webhook`, `issue`, `chatops`) gets a startup warning, since every
  destination on it lands on the one backend. `socks5` and `tcp` are the two schemes
  that carry the non-HTTP channels `email` (SMTP) and `call` (Asterisk AMI); an HTTP
  proxy has no requests to proxy for them and is ignored with a warning.

  A misconfigured proxy URL does **not** degrade to sending directly: that would turn a
  typo into an alert nobody receives, reported as a provider problem. The channel fails
  every attempt with the reason, and `/api/v1/readiness` counts it as a channel with no
  transport. The SSRF guard changes shape for a proxied channel and says so in
  [docs/community/en/SECURITY_PROFILE.md](docs/community/en/SECURITY_PROFILE.md): the proxy's own address is always
  permitted (an egress proxy on `10.x` is the normal deployment), the destination is no
  longer judged by IP because this process neither resolves nor dials it, and
  `NXS_ANOMALY_EGRESS_ALLOWLIST` continues to judge it — on the configured URL and on
  every redirect hop. Channels that go direct, including everything on the `NO_PROXY`
  list, are guarded exactly as before. Full reference: [docs/community/en/PROXY.md](docs/community/en/PROXY.md).

- **Russian and English interface (`ru-RU`, `en-US`).** Visible UI copy now lives in a
  typed catalog, including onboarding, validation/error summaries, alert severities and
  delivery states. Dates, relative times, durations and numbers use `Intl` in the person's
  saved language and timezone; schedule previews stay in the schedule's own timezone.
  The language switch applies immediately, is remembered in the browser, and is also saved
  on the signed-in user through the self-service `/api/v1/auth/preferences` endpoint.

- **Personal data: an inventory, a retention profile, export and verifiable erasure.**
  The product stores names, logins, email addresses, phone numbers, Telegram ids, OIDC
  subjects, client IPs, alert payloads nobody classified, and an audit trail of who did
  what. What it had no answer for was how long any of that is kept, what exactly is held
  about one person, or how to remove it. [docs/community/en/DATA_INVENTORY.md](docs/community/en/DATA_INVENTORY.md)
  is now the map, and three mechanisms act on it.

  **Retention** is per category (`NXS_ANOMALY_NOTIFICATION_RETENTION_DAYS`,
  `_DELIVERY_ATTEMPT_`, `_WEB_SESSION_`, alongside the existing audit and alert-group
  horizons), swept every worker cycle in bounded batches, and reported as
  `nxs_anomaly_retention_deleted_total{category}` plus an info log line — a horizon whose
  sweep silently stopped is otherwise indistinguishable from one that finds nothing.
  Every new horizon defaults to "keep", because an upgrade must not start deleting a
  customer's history on its own initiative; the readiness report says so out loud rather
  than choosing a number nobody agreed to. Only terminal notifications are eligible: one
  still in flight is work, and deleting it would drop a page somebody is waiting for.

  **Export** (`GET /api/v1/users/{id}/export`, admin) assembles the records themselves
  rather than a summary. The password hash and session token hashes never appear in it —
  they are credentials, not information about the person, and an export would only be one
  more place for them to leak.

  **Erasure** (`POST /api/v1/users/{id}/erase`, admin) deletes credentials, sessions,
  devices and ChatOps bindings, and pseudonymises the user record, the delivery addresses
  on their notifications, and the actor name and IP on their audit events. The events
  themselves — what happened, when, in what order — are untouched, including the record of
  what this person did to *other* people's data, which is what an audit trail exists to
  keep. Deleting the row instead would orphan the alert history and destroy that record.
  The response is a verification, not a claim: the data is read back and searched for the
  original identifiers, and the copies this operation cannot reach (backups, logs,
  analytics, traces) are named in `out_of_scope` rather than left implied. Idempotent.

- **Outbound channel policy.** `NXS_ANOMALY_BLOCKED_CHANNELS` refuses named channels
  installation-wide and `NXS_ANOMALY_EGRESS_ALLOWLIST` restricts where webhook-shaped
  deliveries may go (hostnames, a leading dot matching subdomains, and CIDRs). Enforced
  at configuration time, at delivery time and on redirects — three places, deliberately:
  configuration-time refusal means an operator learns about the rule when they save,
  not the night nobody was paged; delivery-time refusal covers objects configured before
  the policy was tightened; and a 302 goes somewhere this installation never agreed to.
  A refusal is a terminal `skipped` with a stated reason, never a silent success. The
  readiness report blocks while anything still points at a refused channel — not because
  something might leak (it will not), but because the roster shows it as a way to reach
  a person and it is not one.

  The allowlist judges the destination as written; the SSRF guard follows DNS and judges
  what the name resolves to. They are complementary — an allowlisted hostname pointing
  into the cluster is still refused.

  `NXS_ANOMALY_ANALYTICS_PSEUDONYMISE=true` additionally digests `user_id`/`team_id`
  before events leave for Kafka/ClickHouse. Genuinely personal fields never entered those
  events in the first place; this is the layer on top, for an analytics store read more
  widely than the service.

- **Helm: an RF closed-beta preset, a values schema, and post-install acceptance.**
  `values-beta-rf.yaml` (an enterprise-edition values overlay) layers the
  compliance decisions on the production profile: explicit retention per category, the
  channel policy above, external PostgreSQL at `sslmode: verify-full`, two replicas of
  API/worker/frontend with PDBs and topology spread, NetworkPolicy + ServiceMonitor +
  PrometheusRule, JSON logs, Kafka and ClickHouse off, and tracing either off or pointed
  inside the same perimeter. Three fields are left blank on purpose and the chart refuses
  to render without them.

  `values.schema.json` rejects values of the wrong shape before rendering; the preflight
  rejects right values in wrong combinations — incompatible secret modes, production with
  a bundled database, an empty external DB host where the chart composes the DSN, replica
  counts (including a PDB that permits no disruption at all), tracing without an endpoint,
  unstated team scoping, an unsafe `sslmode`, and NetworkPolicy plus ServiceMonitor with
  no route for Prometheus.

  `tests.acceptance.enabled` adds a `helm test` pod that drives a real alert through a
  temporary canary integration — ingest, delivery attempt, acknowledge, resolve, audit —
  and removes everything it created from a `trap`, so a failed assertion cannot leave a
  live ingest endpoint on a production installation. The canary is paged on the `log`
  channel, which needs no egress: an acceptance failure should report the installation's
  health, not the network's opinion.

- **Analytics: what actually happened to an alert, in ClickHouse.** The Kafka outbox
  carried one event, `alert.ingested`, so the store could answer how many alerts arrived
  and nothing about whether anybody was woken, how long they took, or whether the page
  reached them at all. It now carries the group lifecycle, every delivery attempt, each
  executed escalation step, and a periodic schedule-coverage sample — through the same
  transaction as the change they describe, so an event exists exactly when the change was
  committed. No CDC, and PostgreSQL stays the operational source of truth.

  The unit of measurement is the **episode**, not the group: a group can be answered more
  than once, and folding the passes together lets a reopen overwrite the MTTR of the first
  response. Publishing is at-least-once, so everything downstream deduplicates on an
  `event_id` generated once and stored inside the outbox payload — a republished event is
  byte-identical, and a duplicate counted as a second incident is the failure the whole
  design is arranged around. For the same reason there is no incremental materialized
  view: it would add the duplicate on arrival and never subtract it.

  What the numbers refuse to do is as deliberate as what they do. An episode nobody
  resolved has no MTTR rather than a large one, and appears as its own count. The delivery
  rate excludes skips, because a channel with no transport was never tried and averaging
  it in hides a misconfiguration behind a good number. Detection time uses the timestamp
  the source itself reported and is absent — never zero — when the source reports none.
  Nothing that identifies a recipient reaches the store: no target, no provider body, no
  credential, no schedule name.

  Ships as `nxs_events_raw` plus five semantic views, a `docs/grafana-analytics-dashboard.json`
  with 23 panels, and Vector routing that keeps the existing `nxs_alerts_log` and its
  queries exactly as they were. Emission is behind `KAFKA_ANALYTICS_LIFECYCLE_ENABLED`,
  off by default: the previous consumer rejects a lifecycle message *after* committing its
  offset, so the schema and the routing must be deployed and confirmed first. Optional
  pre-aggregated rollups are off until a dashboard is measured to need them, and are
  rebuilt from the deduplicated source rather than maintained incrementally.

- **The team boundary has to be stated.** `NXS_ANOMALY_TEAM_SCOPING` defaults to off, and
  unset reads as off — which in a multi-team installation means every operator can see and
  page every service, decided by nobody. The production profile now refuses to render until
  the flag is set explicitly to `"true"` or `"false"`; a single trust domain is a
  legitimate deployment, so the gate asks for the answer rather than a particular one, and
  the production preset answers `"true"`. The readiness report gains "Team boundaries are
  decided", which blocks on integrations and schedules that belong to no team: with scoping
  on, those are visible to everyone, and it is silent — the UI shows the object and the
  audit shows nothing wrong. API keys remain global, which the setup guide now says.

- **Heartbeat: notice a source that has gone quiet** (`heartbeat.interval_seconds` per
  integration, off by default). Every other signal in this service starts with an alert
  arriving, which leaves the one failure nobody is told about — the exporter that died, the
  cron that stopped, the segment that took the Alertmanager with it. Silence looks exactly
  like health, and the longer it lasts the more reassuring it gets.

  "Last seen" is read from the alerts table rather than stamped on the integration:
  `nxs_anomaly_alerts_integration_received_idx` is already `(integration_id, received_at
  desc)`, so it is an index-only lookup, where a stamped column would mean writing the
  integration row on the hottest path in the service. The alert is raised **through the
  ordinary ingest path**, so it is routed, grouped, escalated and delivered by exactly the
  machinery a real alert uses — a dead-man switch taking its own private route to the
  responder would be the one alert nobody had ever tested end to end. It fires once per
  silence rather than once per worker cycle, and resolves itself when the source returns.

  Opt-in per integration because plenty of sources are legitimately quiet for weeks;
  reporting those would train everyone to ignore it. The interval is floored at a minute
  (the worker cycle is five seconds, and a late tick is not an outage) and `grace_seconds`
  defaults to a third of it. Plus `nxs_anomaly_sources_silent` and a `SourceSilent` rule as
  the backstop for when nxs-anomaly's own delivery is what broke.

- **Buttons on Slack and Mattermost, not just Telegram.** A responder could acknowledge with
  one tap or by typing a command depending on which chat they happened to read the alert in;
  now the three platforms offer the same thing. Slack alerts carry `blocks` with buttons and
  the taps arrive on `/integrations/v1/chatops/slack/interactive`, verified with the same v0
  signature as slash commands — the credential check is the one already proven, only the
  body's shape differs. Mattermost alerts carry `attachments` with `actions` and post back to
  `/integrations/v1/chatops/mattermost`. Every action resolves through the same table as the
  Telegram ones, so one added on a single platform cannot quietly mean something else on
  another; a test asserts the three keyboards stay identical.

  Slack replaces the message on success and leaves it on refusal; Mattermost updates the post
  and empties its attachments, or answers the person alone. Either way a settled alert stops
  offering a button for work already done, and a refused tap leaves the buttons for whoever
  looks next.

  Mattermost buttons need `NXS_ANOMALY_PUBLIC_URL` and `NXS_ANOMALY_MATTERMOST_ACTION_SECRET`:
  its callback goes to an absolute address and carries no signature, so the secret in the
  button's context is the only thing separating a real tap from anyone who learned the URL.
  Without either, the alert still goes out — plain. A button pointing where nobody answers is
  worse than no button. Both platforms run as the service principal, since neither's user ids
  are stored here; that is the same limitation the typed Slack command already had.
- **The alert message settles once its buttons are used.** After an acknowledge the message
  kept offering "Acknowledge": the toast confirming it is gone within seconds, and what
  stays on screen is a button implying the work is still waiting. The verdict is now
  appended to the message and the keyboard removed, in place, using the message the tap
  already identifies — no id had to be stored anywhere. A refused tap changes nothing and
  leaves the buttons, since the next person to look should still be able to act.
- **A "I am on duty" button on the shift notice.** The handover message already reached the
  person; asking them to then remember a command is a step nobody takes at 09:00 on a
  Monday. Confirming a shift is now a tap.
- **`DutyShiftWithoutCheckin` alerting rule** over `nxs_anomaly_duty_without_checkin`. The
  metric existed with nothing watching it, so a shift nobody confirmed was visible only to
  whoever happened to open the dashboard. Thirty minutes rather than the fifteen used for
  coverage: a shift boundary legitimately leaves a few minutes before the incoming person
  confirms, and paging on that would teach everyone to ignore the alert.
- **A takeover tells the person it relieved.** `duty take` removed somebody from call
  without telling them, which is the same failure as putting them on it without telling
  them: they keep behaving as though the pager is theirs, or stop watching without knowing
  anyone else started.
- **Rate limits from a provider are honoured** (`Retry-After`, and Telegram's
  `parameters.retry_after`, on HTTP 429). A 429 was treated as an ordinary delivery failure
  and retried on the configured backoff — during a burst, which is exactly when the limit
  arrives and when this service is generating the most traffic, that turns a rate limit into
  a queue of requests all refused again. The wait is capped at an hour, applies only to 429
  (a 500 with a stray header is not a rate limit and must not dictate this service's
  schedule), and does not extend the retry budget: a provider that keeps saying "later"
  still exhausts its retries and dead-letters.
- **Take over on-call during an incident (`duty take`), and reorder who is reached first
  (`priority`).** A takeover writes a *schedule override*, never the check-in flag: the
  schedule engine does not read that flag, so switching duty with it would leave
  `NOTIFY_SCHEDULE`, the coverage report and the preview all still naming the person being
  replaced — the two answers would disagree precisely when it matters. Two hours by default
  and a day at most; beyond that it is a schedule change, which belongs where the whole team
  can see it rather than in a chat message nobody scrolls back to. The schedule is chosen
  when only one is reachable — hunting for an id is not what somebody woken at 4am should be
  doing — and when several are, the candidates are listed rather than one being picked by
  coin flip over whose phone stops ringing. The check-in is set to the same window so it
  lapses with the override instead of outliving it. `priority` changes your own place in the
  queue as a responder; changing a colleague's decides when *their* phone rings and takes
  the editor role. Both obey the same two boundaries as the alert commands. The override
  itself is built by the same code as the REST path, so one taken in an incident is
  indistinguishable from one entered in the web interface — which it must be, since the
  schedule engine gives overrides top priority and cannot tell them apart.
- **Shift check-in (`duty on` / `duty off`) that expires by itself**, plus
  `nxs_anomaly_duty_on_call` and `nxs_anomaly_duty_without_checkin`. The schedule decides
  who *should* be on call; the flag now records who confirmed it, and the gap between the
  two is reported. Coverage only ever answered "is somebody assigned", so a rota could be
  green while nobody had actually taken the shift, and nothing said so. A check-in lapses at
  the end of the shift the person is covering — or after twelve hours when no schedule names
  them, which is what a stand-in during an incident looks like. A fixed timeout would either
  outlive the shift, leaving the escalation step paging whoever last remembered to type
  "duty on" days ago (the defect the old nxs-alert had with its manual duty on/off), or cut
  short a long shift and drop the flag halfway through. Checking in requires a sender this
  deployment recognises: the service principal a shared chat falls back to is not a person,
  and letting it set the flag would put the whole chat on call. An `on_duty` set through the
  API or the web interface is a standing assignment, not a shift confirmation, and this
  mechanism leaves it alone.
- **`alerts` — the open alert groups, listed and actionable from the chat.** `status`
  answers how many there are; this answers which ones, newest first, five to a page, with
  one button per group that acknowledges it. Paging state travels in `callback_data`
  (`alerts:2`) rather than in a stored session: a listing is read by one person for a few
  seconds, and a session would outlive the reason it existed while adding a table, a
  migration and an expiry policy to maintain. A tap redraws the same message through
  `editMessageText` instead of posting another near-identical list, and a page past the end
  is clamped to the last one — pages shrink while they are being read as colleagues
  acknowledge, and a stale tap should land somewhere useful rather than on an error about a
  page that existed a minute ago. The listing obeys the same team boundary as the actions.
- **Tell the people a group woke that it is over** (`NXS_ANOMALY_NOTIFY_ON_RESOLVE`, off by
  default). A group closed in silence: whoever had been dragged out of bed for it found out
  by going and looking, and the escalation they were answering simply stopped. Recipients are
  the users the group actually paged, recorded on the group itself as it notifies them —
  recomputing the set from the schedule would reach whoever is on call *now*, which at 4am is
  a different and wrong list, and reading it back from the notifications table would cost a
  load on every resolve path including ingest, which does not know the group's id until it is
  already inside the mutator. It fires once, marked on the group: resolve is idempotent and
  allowed from any state, so a duplicate source event would otherwise page everyone again,
  and the in-state idempotency set cannot prevent that because the paths that resolve save
  notifications without loading them. No personal policy run is started — a policy escalates
  until someone acknowledges, and there is nothing left to acknowledge. Off by default
  because it is a new class of message and an upgrade does not get to decide that a responder
  now receives twice as much at night.
- **Acknowledge and resolve from Telegram with one tap.** Alert notifications sent to
  Telegram now carry inline `Acknowledge` and `Resolve` buttons, and the resulting
  `callback_query` is accepted on the existing signed webhook. A tap is translated into the
  ChatOps command it stands for and runs through exactly the same path as the typed word, so
  identity, team scope and role are decided in one place rather than twice — a shortcut that
  reached its own verdict would be a second, weaker way in. Buttons are attached only to
  notifications about an alert group, and are dropped rather than truncated when
  `callback_data` would exceed Telegram's 64-byte cap: over the limit Telegram rejects the
  whole `sendMessage`, so an unusually long group id would cost the alert itself, not just
  its shortcut. The webhook always answers `200` — a refusal is a final answer, and a non-2xx
  would have Telegram redeliver the update and re-run the command — with the verdict reaching
  the responder through `answerCallbackQuery`, without which the button spins forever and a
  completed acknowledge reads as nothing having happened. Slack's interactive messages are
  still out of scope: they need a full Slack app.
- **Distributed tracing** ([docs/community/en/TRACING.md](docs/community/en/TRACING.md)): OpenTelemetry spans over
  ingest → worker cycle → provider call, exported over OTLP/HTTP, off unless
  `OTEL_EXPORTER_OTLP_ENDPOINT` is set. Metrics answer "how many deliveries are slow"; the
  question after an incident is "why did *this* notification take four minutes", and that
  is a causal chain. The chain here crosses processes and time — a webhook lands on an API
  pod and the delivery happens in a worker cycle minutes later — so `X-Request-ID` cannot
  join it. Trace id is returned as `X-Trace-ID`, logged on the access line and the delivery
  attempt, and stored on audit rows (migration `0025`, filterable via
  `GET /api/v1/audit?trace_id=`) so the join outlives the spans themselves. Probes are not
  traced and span names are route categories, not paths.
- **Error-budget burn-rate alerts** (`nxs-anomaly.slo` group in
  [docs/prometheus-rules.yaml](docs/prometheus-rules.yaml)): multi-window multi-burn-rate on
  ingest availability (99.9%) and per-provider delivery success (99%), fast 14.4× over 5m+1h
  and slow 6× over 30m+6h, on top of recording rules for each window. Every existing rule
  compares an instant rate to a fixed threshold, which cannot tell a two-minute spike from a
  week of slow bleeding — the spike pages and recovers, the bleed never crosses the line and
  eats the SLO. `helm:test` now diffs the chart's rule file against the docs one, which the
  templates asked for in a comment and nothing enforced.
- **Cluster-wide sign-in rate limit** (migration `0024`): the sign-in limiter's token buckets
  moved from a per-process map into PostgreSQL, so the limit is what it says regardless of
  replica count. It was per pod, meaning an attacker spreading password guesses across pods
  got N times the attempts — the limit that most needs to be global was the one scaling with
  the deployment. The ingest and API limiters stay in memory on purpose (hot path); the chart
  now divides `rateLimits.*PerCluster` by `api.replicaCount` so operators state the figure
  they actually mean. Fails open if the database is unreachable, deliberately: that path
  cannot verify a password without the database anyway. See
  [docs/community/en/SECURITY_PROFILE.md](docs/community/en/SECURITY_PROFILE.md).
- **Fuzz tests for ingest normalisation** (`internal/engine/ingest_fuzz_test.go`): seven targets
  over the Alertmanager, PagerDuty, VictorOps, Grafana and legacy nxs-alert payload paths,
  asserting the result stays JSON-serialisable, valid UTF-8, and carries a non-empty title.
- **Nightly capacity gate** (`test:loadtest`): the BETA-022 harness and its acceptance criteria
  have existed since the capacity work and nothing ran them, so the figures in
  [docs/community/en/CAPACITY.md](docs/community/en/CAPACITY.md) documented a state of the world nobody was checking.
- **CLI tests** (`cmd/nxs-anomaly/main_test.go`): dispatch, usage, table rendering and the value
  helpers. The command list is now one table instead of a switch plus a hand-written usage
  string that could drift.
- **One source for the release version** ([`VERSION`](VERSION)): `scripts/set-version.sh` stamps it
  into `Chart.yaml` (version + appVersion), `docs/openapi.json` and the chart README, and
  `scripts/check-version.sh` refuses a mismatch from the `pre-commit` hook, the `pre-push` hook
  (a `vX.Y.Z` tag is validated against the commit it points at, not the working tree) and the
  `test:version` CI job. The published image tag keeps its `v` and the chart version cannot,
  so the two forms are now stated once instead of drifting apart.
- **Chart release pipeline restructured after nxs-universal-chart**: package → publish → sign →
  metadata → verify, one stage per step (`release:sbom`, `release:chart:package`,
  `release:chart:publish`, `release:sign`, `release:verify`).
- **`release:verify`**: a tagged pipeline re-runs the chart README's own install and
  `cosign verify` commands **with no registry credentials**, against the artifacts it just
  published, deriving the image refs from the published chart — so a tag the chart resolves to
  but nobody pushed fails the release. The transcript ships as `release-verification.txt`.
- **Point-in-time recovery drill** (`tests/pitr_drill.sh`, CI `test:pitr-drill`): WAL archiving →
  base backup → destroy the cluster → replay to a recovery target, asserting the pre-target write
  is back **and the post-target write is gone**. Without that second assertion a recovery that
  replays everything looks identical to a working PITR and could not undo an accidental delete.
  It waits for `pg_is_in_recovery() = false`, since a cluster still replaying answers `pg_isready`
  and fails every write.
- **Rollback compatibility is tested, not asserted**: the restore drill now builds the previous
  release tag and requires it to serve and ingest against the restored, already-migrated schema —
  the expand/contract promise in [docs/community/en/BACKUP_RESTORE.md](docs/community/en/BACKUP_RESTORE.md).
- **Setup wizard and readiness report** (BETA-051): `/setup` walks a new team from people to a
  dry-run alert, and `/readiness` answers the only question that matters — if an alert arrived
  now, would anybody be paged? Seven checks (database, worker, integrations, routing,
  notification targets, schedule coverage, backup age) graded `ok`/`warning`/`blocker`, exposed
  at `GET /api/v1/readiness`. Every step is **derived** from that report rather than stored, so
  the wizard cannot claim a step is done after someone deletes what satisfied it. Production
  activation stays blocked until an admin acknowledges the blockers with a reason
  (`POST /api/v1/readiness/acknowledge`); the acknowledgement is bound to a fingerprint of the
  exact blocker set and lapses when that set changes. The backup check is fed by the backup job
  itself (`POST /api/v1/backups/report`), since snapshots, WAL archives and dumps all happen
  outside the application — see [docs/community/en/BACKUP_RESTORE.md](docs/community/en/BACKUP_RESTORE.md).
- **OpenAPI 3.1 contract** for the native `/api/v1` surface ([docs/openapi.json](docs/openapi.json)):
  security schemes, the shared `{"error"}` model, the pagination envelope, and field-by-field
  schemas for the core payloads (identity, user, team, schedule, escalation chain, integration,
  alert group, alert, notification, delivery attempt, readiness), where `required` lists exactly
  the keys the engine writes unconditionally. Drift is gated in both directions by
  `internal/server/openapi_contract_test.go`: every documented route must be dispatched (a
  panicking handler is a failure, not a pass), every route literal must match a documented path
  segment by segment, every suffix-routed sub-resource must end a documented path, and every
  authorization prefix in `auth.go` must still lead somewhere documented. The frontend compiles
  against types generated from it — `src/api/types.ts` and `src/api/client.ts` alias the
  generated schemas instead of re-declaring them, so a spec change is a TypeScript error. The
  Grafana-compat surface is explicitly **not** part of the contract.
- **Backup/restore/upgrade/rollback runbook** ([docs/community/en/BACKUP_RESTORE.md](docs/community/en/BACKUP_RESTORE.md))
  with RPO ≤5 min / RTO ≤30 min targets, a backward-compatible (expand/contract) migration
  rule, and an **automated restore drill** (`tests/restore_drill.sh` + CI `test:restore-drill`):
  seed → backup → drop DB → restore → verify the data survived and the alert flow still works,
  asserting RTO within budget.
- **Frontend supply-chain gate**: `npm run audit:prod` (zero unaccepted high/critical in
  production deps — beta criterion) and a controlled `audit:dev` gate, via a small allowlist
  script with dated, self-expiring exceptions (`frontend/scripts/audit-gate.mjs`).
- **Helm production preset** (`values-production.yaml`) and a render-time **preflight** that
  refuses the production profile with inline secrets, bundled databases or a weakened
  SSRF/secure-cookie flag; image + OCI chart signature verification documented.
- **Production security profile** (`NXS_ANOMALY_PROFILE=production`): one switch enables
  the SSRF guard, request rate limits and the delivery circuit breaker, and refuses new
  inline secrets. Any single default is still overridable. See
  [docs/community/en/SECURITY_PROFILE.md](docs/community/en/SECURITY_PROFILE.md).
- **`env:VAR` secret references** for per-object secrets (integration `webhook_secret`,
  chatops `webhook_url`, mobile `push_token`) so the plaintext never lands in PostgreSQL;
  legacy inline values keep working.
- **Security regression suite** and a `test:security` CI job; **SBOM** (CycloneDX) and
  **cosign** image signing on tagged releases.
- Root `LICENSE` (Apache-2.0), `SECURITY.md`, this changelog.
- **Official Helm chart** (`deploy/helm/nxs-anomaly`): API/worker/frontend; bundled or
  external PostgreSQL/Kafka/ClickHouse; secrets via existingSecret / External Secrets /
  Vault Secrets Operator; PDB, NetworkPolicy, topology spread, ServiceMonitor,
  PrometheusRule. Verified by helm-unittest and a kind install/upgrade smoke.
- **Personal notification policies** (default/important): notify → wait → fallback state
  machine, stopping on acknowledgement; test-notification endpoint.
- **Observable standalone worker**: `/live`, `/ready`, `/metrics` on the worker; readiness
  reflects DB reachability and cycle completion. Full SLI/alert-rule set, Grafana
  dashboard, and a capacity/chaos load harness.
- **On-call correctness**: the dashboard shows who is actually on call now (Schedule v2),
  not the manual on-duty flag; `GET /api/v1/on-call`.
- Browser e2e release gate (Playwright) and a deterministic frontend build gate.

### Changed
- **`test:coverage` is a gate, not a dashboard.** It was `allow_failure: true`, so coverage could
  fall a point per release with nothing ever saying so. `COVERAGE_FLOOR` is now enforced (71.0,
  against a measured 73.0).
- **Cosign signing moved from keyless to the project's release key** (`COSIGN_PRIVATE_KEY` /
  `COSIGN_PUBLIC_KEY`), matching nxs-universal-chart. Keyless mints a Fulcio certificate from the
  CI's OIDC issuer and public Sigstore trusts `gitlab.com`, not a self-hosted `github.com`: the
  documented `cosign verify` was a command nobody could have run. Images and the chart are signed
  by digest, and the public key ships as a `release:verify` job artifact.
- **Restore RTO is measured to service readiness, not to the end of the database import.** The
  drill now starts the real API and worker on the restored database and waits until
  `GET /api/v1/readiness` reports database *and* worker ok. A restored database nobody can be
  paged from is not a recovered service, and the gap is not academic: a local run shows ~5s of
  import followed by ~13s before readiness is green.
- **The rest of the OpenAPI payloads are modelled** (BETA-042 tail): ChatOps channels and
  messages, Grafana plugin registrations, the history, on-call, coverage, preview, bulk-action
  and route-debug envelopes are now field-by-field schemas instead of the open `Entity`, and the
  frontend compiles against them.
- **Frontend dependencies upgraded** to close all `npm audit` high/critical findings: React
  Router 7, Vite 8, Vitest 4, Playwright 1.61.1; toolchain moved to **Node 22** (CI + Dockerfile),
  removing the jest-dom 7 / Node 20 mismatch.
- **Helm chart is now a release artifact**: real image registry defaults
  (`ghcr.io/nixys/nxs-anomaly[-frontend]`), chart version == appVersion == image
  tag, and a `release:helm-chart` job that publishes the signed OCI chart on tagged releases.
- **Audit is now atomic with the operation it records**: the state change and its audit
  event commit in one transaction, so a committed change can never be missing its record.
- Insights: the integration filter now scopes every KPI, and the time range is scoped to
  the incident history it actually filters.

- **Kafka → ClickHouse consumer in the chart** (`kafkaConsumer.enabled`). The outbox has
  published to Kafka for a while and the chart provisioned ClickHouse as its sink, but
  wiring the two was left to the operator. It is now a Vector deployment plus a
  `post-install` hook Job that creates the table. Vector rather than ClickHouse's own Kafka
  table engine because the engine reports a stalled consumer and skipped malformed messages
  only in `system.kafka_consumers` and the server log — nothing an alert rule can watch;
  Vector exports Prometheus metrics the existing ServiceMonitor scrapes, buffers to disk
  across a ClickHouse restart, and prints events it could not parse instead of dropping
  them. The table is `ReplacingMergeTree(published_at)` ordered by
  `(received_at, integration_id, alert_id)`, which is what makes the documented "ClickHouse
  deduplicates by alert.id" true — the sample DDL that claim referred to was a plain
  `MergeTree` and double-counted every republished alert. Rendering is refused unless both a
  Kafka source and a ClickHouse destination are configured. See
  the Kafka reference and the chart README.

### Removed
- **The Grafana OnCall plugin compatibility layer** (`/api/internal/v1/*`, `internal/grafana`),
  the `grafana_plugins` entity with its API and settings page, and the QR mobile-verification
  tokens that only it issued. The layer existed because the service began without an
  interface of its own: the plugin *was* the UI. The standalone frontend replaced it, and
  what remained was 3 500 lines at 37% coverage carrying a second management surface in
  someone else's shape — including `NXS_ANOMALY_GRAFANA_COMPAT_ALLOW_ANONYMOUS`, a switch
  that granted the admin role to every request on that surface. Removing it deletes an
  attack surface as well as the code.

  Unaffected, despite sharing the word: the **Grafana Alerting webhook**
  (`POST /integrations/v1/grafana-alerting/{key}`) is an ingest source and stays; the Grafana
  dashboard and Prometheus rules for observing this service are unrelated. Mobile sessions
  are unaffected — `/api/v1/mobile/*` has its own session endpoint; only the QR handshake,
  which lived inside the compat layer, is gone.

  Five tables are left in place on purpose (`nxs_anomaly_grafana_plugins`, the three from
  `0011`, and `nxs_anomaly_mobile_verification_tokens`). Dropping them in the same release
  would break the rollback the DR drill asserts: the previous binary still reads them. They
  are removed by a migration in the next release — see
  [docs/community/en/MIGRATIONS.md](docs/community/en/MIGRATIONS.md), "Снятые таблицы".

### Fixed
- **`nxs_anomaly_db_up` read 0 forever on every API replica, and the alert built on it
  fired on healthy deployments.** The gauge was refreshed only inside the worker cycle,
  and an API process runs with `--no-scheduler`, so it never ran one: the gauge sat at
  its zero value for the life of the process while `/health` on the same pod reported
  `db_ok: true`. `DatabaseUnavailable` (`docs/prometheus-rules.yaml`) reads that gauge
  from every process — its own comment says "worker or API" — so scraping the API meant a
  permanent critical page naming the database, which was fine. The pool gauges
  (`nxs_anomaly_db_pool_*`) were dead the same way. The process-local gauges now refresh
  on a ticker wherever no worker cycle runs; the cluster-wide backlog gauges stay the
  worker's job, so API replicas do not double-report them.
- **A failed sign-in left nothing in the audit trail.** Rejections were recorded with a
  `slog.Info` and nowhere else, while every *successful* login was written to
  `nxs_anomaly_audit_events` — so the events an operator actually reviews were the ones
  missing. A brute-force run against the login endpoint was invisible to the trail, and
  the service log is not a substitute: it rotates and it is not append-only. Rejections
  now write `auth.login_failed` with the attempted login and the request IP; the password
  never appears. Where the login names a real account the event is attributed to that
  account — not a claim about who was at the keyboard, but what keeps the record within
  reach of `PseudonymiseAuditActor`, which keys on `actor_id`, so an erasure request still
  covers it. A login matching nobody stays unattributed.
- **Neither the API nor the frontend sent any security header.** The frontend's nginx now
  sets `X-Content-Type-Options`, `X-Frame-Options`, `Referrer-Policy` and a
  Content-Security-Policy matching what the build emits (hashed same-origin assets, no
  inline script), repeated in the two locations that declare an `add_header` of their own
  — nginx does not merge them, so `index.html` would otherwise have been the one response
  without any. `server_tokens off` stops it advertising its exact version. The CSP is an
  env var (`NXS_ANOMALY_CSP`) because widening it is a legitimate deployment need. The Go
  API sets `X-Content-Type-Options: nosniff` itself, since it is also reachable without
  nginx in front of it. HSTS is deliberately left to whatever terminates TLS.
- **A notification template edited through the API did not reach the worker until the
  worker was restarted.** The per-integration template cache had no expiry: an entry was
  read once on the first delivery and then served from memory forever. Its only eviction
  path is `invalidateTemplateCache`, called in-process after an integration update or
  delete — so the API pod that served the `PUT` cleared its own copy and every worker
  replica kept rendering the old text, indefinitely and with nothing in the logs to say
  so. The cache now carries a load timestamp and expires on the same clock as the
  reference cache next to it (the worker poll interval, 5s by default), which is the
  mechanism that already bounds cross-replica staleness for users, teams and schedules.
  In-process invalidation stays, so an edit is still visible immediately on the pod that
  served it. Because expiry now means a re-read, a failed read falls back to the expired
  copy rather than to the built-in default text — a database blip must not quietly strip
  a configured template off a page.
- **The Kafka→ClickHouse consumer could go silently deaf on one of its two tables.**
  Both ClickHouse sinks ran a startup healthcheck, and a Vector sink whose healthcheck
  fails is never started: its events go into the disk buffer and stay there, with no log
  line after the single "Healthcheck failed". Pod start order is arbitrary, so ClickHouse
  still coming up when the consumer boots is the ordinary case — and because the two
  sinks healthcheck milliseconds apart, whichever lost that race was the one that went
  dark. The symptom was an alert that reached the legacy table but never the event log,
  or the reverse, with everything else in the pipeline reporting healthy. Confirmed on a
  live cluster: two events queued in the sink's buffer, `component_received_events_total`
  = 0 for that sink, and immediate delivery of exactly those events once the process was
  restarted with ClickHouse up. Both sinks now run without the startup healthcheck — they
  retry on their own and the buffer holds the backlog, so what is worth alerting on is a
  growing `vector_buffer_events`, not a boot-time probe. The kind e2e job prints the
  buffered-vs-received counters when a table stays empty, so the next occurrence of
  anything like this names the component instead of leaving "the table is empty".
- **Buttons on a personal Telegram notification did nothing.** Alerts are delivered to a
  person's own chat with the bot, with Acknowledge and Resolve attached; a tap comes back
  naming that private chat, which is nobody's ChatOps channel, so every one of them was
  refused with "no telegram chatops channel is bound to <chat id>" — the alert stayed
  open while the responder was sure they had acknowledged it. A command from a chat with
  no channel bound now runs when the sender is somebody this deployment recognises (their
  `telegram_id` is on a user), under that person's own role and team scope. An
  unrecognised sender in an unbound chat is still refused: that account falls back to the
  platform service principal, and running its commands from any chat would hand the right
  to acknowledge alerts to whoever adds the bot somewhere.
- **Deleting a maintenance window answered "Internal Error".** The window collection was
  missing from the engine's delete allow-list, so the documented `DELETE
  /api/v1/maintenance-windows/{id}` — and the delete button the UI has always shown —
  reached a bare error and came back as a `500`. Windows can be deleted; an unsupported
  collection is now a validation error rather than a server fault.
- **Closing an alert group left its alerts reading "firing" for ever.** An alert's status
  was written once, at ingest, from what the source reported, and no group transition
  ever touched it again — so an operator who resolved a group watched its alerts stay
  active on the alerts list and on the group's own alerts tab, in a state nothing would
  ever leave. A group's lifecycle now carries to its members: resolving closes them,
  reopening a group reopens them, and this applies wherever the resolve comes from — the
  API, a bulk action, a mobile session, a ChatOps command or button, a `RESOLVE`
  escalation step, or a resolving event from the source (which closes the alerts that
  arrived earlier in the group, not only the event carrying the resolution).

  The write is one statement per transition rather than part of the group's save cycle:
  a busy group can hold thousands of alerts, and pulling them through the mutator would
  put the cost of a resolve — and of every ingest, because a source resolve happens
  there — on the length of the group's history. It runs after the group's transaction
  commits, so a failure leaves the group closed with its alerts unchanged, which is the
  behaviour that shipped until now, rather than rolling back an operator's action.

- **The chart's `helm test` hooks were never packaged.** `.helmignore` carried an
  unanchored `tests/`, which also matched `templates/tests/` — so `helm test` on an
  installed release ran nothing at all and reported success: the hook Pod was not in the
  chart to run. Anchored to `/tests/`.

- **The pipeline started twenty-six jobs at once and starved itself.** Every test job sat in
  a single `test` stage, so three kind clusters, two BuildKit builds, half a dozen PostgreSQL
  and Kafka service containers and the browser suite all began at the same instant and
  competed for the same runners — and a busy afternoon meant several pipelines doing it
  together. The jobs are now grouped by what they cost the cluster rather than by what they
  check (`static` → `quality` → `unit` → `integration` → `cluster`), which spreads the load
  in time by construction: a job without `needs:` waits for the whole previous stage. The
  cheap checks now answer in under a minute and fail the pipeline before anything expensive
  has started. The three kind jobs share a `resource_group`, as do the three database drills,
  so they no longer pile up across concurrent pipelines either.

  Two `needs:` edges were removed rather than kept: `build:binary` and `build:image` named
  the test jobs they depend on, which let a compile and an image build start in the middle of
  the cluster stage on a runner already carrying a kind cluster. The one that remains —
  `build:image` waiting on `build:image:frontend` — is ordering, and keeps two BuildKit
  builds off the same runner.

  While checking the graph: `release:chart:package` could package and publish a chart before
  the kind smoke had run on it, because its `needs:` named only the template checks and
  `needs` skips the stages in between. The cluster jobs are now named there too, `optional`
  so a pipeline where their rules do not fire still builds.
- **History ignored team scoping, and it is the widest read in the service.**
  `GET /api/v1/history` returns alert groups with their notifications, delivery attempts,
  batches and timeline inlined — and a notification carries its *target*: somebody's Telegram
  id, phone number or address. Every other read had been narrowed to the caller's teams;
  this one had not, so any authenticated reader, including a viewer, could enumerate every
  incident in the deployment together with the contact details of everyone who had been
  paged. It is now scoped at the group query, which is what bounds everything it inlines —
  scoping later would mean the rows had already been read. An explicit `integration` filter
  is intersected with the scope rather than replacing it, and a caller whose teams reach no
  integration gets an empty answer rather than an unrestricted one: the SQL renders `FALSE`
  instead of omitting the clause.

  The comment in `scope.go` describing notifications and delivery attempts as an accepted gap
  was stale — both had been closed earlier (migration `0021` denormalised `integration_id`
  onto notifications; attempts require a named, authorised notification). It has been
  rewritten to say what is actually true, since a stale note of this kind hides the real gap
  next to it.
- **A ChatOps command typed into Telegram answered nothing.** Telegram ignores a webhook
  response that is not a method call, so the reply was written, returned and discarded: an
  acknowledge went through and the person who typed it saw no confirmation at all. This is
  the same defect that was found and fixed for Slack — which renders its reply from a
  top-level `text` — and left standing on the other platform. The response is now the
  `sendMessage` call itself.
- **An inbound ChatOps command could act on any alert group in the deployment.** `ack` and
  `resolve` took the group by the id in the message and never asked who owned it, and
  `status` listed every unresolved group there was — so a chat bound to one team could
  acknowledge, resolve and enumerate another team's incidents, and the only thing standing
  between an outsider and that was knowing a group id. Two boundaries now apply and both
  must hold: the actor's own team scope, the same one every other entry point enforces, and
  the team the channel is bound to, because the chat a command arrives in is the context it
  acts in — belonging to two teams does not make one team's chat a way into the other's.
  Ownership is read from the group's integration inside the lock; a group whose integration
  is gone is refused rather than treated as unowned. Integrations with no team stay reachable
  by everyone, so this changes nothing for a deployment that never assigned teams.
- **A Telegram acknowledge was attributed to the bot, not to the engineer who sent it.** The
  signed inbound path authenticated as one service principal for everyone, with the sender's
  handle riding along as unverified data, so the audit trail said `chatops:telegram` and the
  responder role was granted to whoever could reach the chat. The sender's Telegram account
  id is now matched against `users.telegram_id`: a known sender acts as themselves, with
  their own role and team scope, and the audit record names them. An unknown sender still
  falls back to the service principal — a shared team chat is a legitimate way to run these
  commands — but a sender who *is* known and whose role was cleared is refused rather than
  downgraded to the fallback, since falling back there would hand back rights that were
  deliberately taken away. Slack is unchanged: its user ids are not what this service stores.
- **The Kafka outbox published nothing unless the topic already existed.** The producer
  asked the broker for topic metadata with `AllowAutoTopicCreation` unset, so a topic that
  did not exist came back as `UNKNOWN_TOPIC_OR_PARTITION` — regardless of the broker's own
  `auto.create.topics.enable`. Every deployment that did not pre-create its topic therefore
  published nothing at all: the events stayed in `nxs_anomaly_kafka_outbox`, the worker
  retried them every cycle logging `kafka_publish_failed`, and the analytics pipeline stayed
  empty. The producer no longer opts out; the broker's own setting decides, which is where
  that policy belongs, and a broker with auto-creation disabled still refuses and still says
  so. The gap survived this long because the only test that reaches a real broker creates
  its topic first — deliberately, to test the producer rather than a broker setting — so the
  missing-topic path had no coverage. It has one now, and the kind smoke that found this
  asserts the rows arrive end to end.
- **The SSRF guard was bypassable by redirect and by DNS rebinding.** `guardWebhookURL`
  vetted the URL as configured and nothing further: a permitted host answering `302` to
  `169.254.169.254` was followed, because the guard never saw redirect targets, and the
  address it checked was a separate lookup from the address the client went on to dial.
  Anyone who could set a webhook URL — an editor, via a `TRIGGER_WEBHOOK` step or a
  notification target — could reach cloud metadata or the pod network from the server. The
  check now lives in the transport (`newDeliveryHTTPClient`): each hop's address is
  validated inside the dial and the vetted IP is the one connected to, so there is no
  second lookup and no unchecked redirect. A name answering with both a public and a
  private address is refused outright rather than narrowed to the public one. Redirects
  between permitted hosts are still followed, up to the usual limit of 10. Covered in
  [docs/community/en/SECURITY_PROFILE.md](docs/community/en/SECURITY_PROFILE.md); no configuration change — the guard
  is still off by default and on under `NXS_ANOMALY_PROFILE=production`.
- **A database blip could be recorded as "nobody was on call".** Ingest, the escalation
  cycle and the batch flush read their reference collections as `x, _ := refCollection(…)`.
  A failed read yields a nil map, which is indistinguishable downstream from an empty one,
  so a `NOTIFY_SCHEDULE` step found no schedule, wrote *No active user found in current
  schedule window* into the group timeline, advanced `current_step` and cleared
  `next_run_at` — consuming the step permanently. Nobody was paged, no error was returned,
  no metric moved, and the incident timeline carried a plausible-looking explanation. These
  reads now fail the operation (`refSet`), leaving the group untouched so the next cycle
  retries.
- **The Grafana plugin's "who am I" call could return somebody else's identity.**
  `findCurrentUser` fell back to the first user in the roster when the incoming
  `X-Grafana-Context` matched nobody by login or email, so an unrecognised Grafana user was
  answered with another person's record — on a page that shows on-call status and lets the
  viewer act as themselves. It now returns the same synthetic `id: "unknown"` record an
  empty roster already produced, so "we do not know you" has one shape instead of two
  depending on how far along setup happens to be. **Behaviour change** on
  `GET /api/internal/v1/user`: a plugin request whose context matches no user now gets
  `id: "unknown"` and `is_currently_oncall: false` instead of an arbitrary real user. The
  normal path is unaffected — the plugin syncs the caller into the roster on
  `/plugin/v2/status` before asking.
- **UTF-8 truncation on Cyrillic text.** Five places capped a string with `s[:n]`, which counts
  bytes: a legacy alert title (160), a Telegram message (4096), a provider-response excerpt, a
  stored `User-Agent` (512) and the CLI table columns. On Cyrillic — two bytes per letter here —
  that both halves the visible allowance and cuts the last letter in half, and PostgreSQL rejects
  the result outright ("invalid byte sequence for encoding UTF8") while Telegram's API rejects the
  message. A long Russian `triggerMessage` was therefore a failed insert, not a truncated alert.
  All five now go through `utils.TruncateRunes`; the CLI table also measured column widths in
  bytes, so Cyrillic rows did not line up. Found by the new fuzz tests.
- **Ingest availability SLI queried a label value that never existed.** The SLI in
  [docs/community/en/ALERTING_RULES.md](docs/community/en/ALERTING_RULES.md) used `handler="ingest"`; the middleware writes
  `handler="webhook"`. The documented query returned no data.
- **A fresh installation showed a blank page.** Every list endpoint answered `{"items": null}` on
  an empty collection (`scanRows` returns a nil slice, which JSON renders as `null`), and the
  shared `QueryState` component does `data.items.length` — one TypeError, and React unmounted the
  whole SPA: no navigation, no buttons, nothing. Published list envelopes now always carry an
  array, gated by `internal/server/empty_list_test.go`, which walks the documented paths so a new
  endpoint is covered without anyone remembering to add it. The in-memory test double was also
  taught to return nil on an empty result, like the real store — it had been hiding this class of
  bug from every unit test.
- **The /integrations page was unreachable in the dev server**: the Vite proxy forwarded the whole
  `/integrations` prefix to the API, so a hard load of the SPA's own /integrations route returned
  the API's "404 page not found". The proxy now mirrors nginx (`/integrations/v1`), which was
  always correct in production.
- **Sign-in throttling counted successful logins.** With one attempt per ten seconds and a burst
  of five *per IP*, five legitimate sign-ins from a shared egress address — an office NAT, a CI
  runner, the e2e suite — locked everyone else out, and the sustained rate never let them back in
  during a busy morning. Only failed attempts are charged now (the password-change handler already
  worked this way); the budget is still checked before the password hash, so a flood of wrong
  passwords is still rejected cheaply.
- **ChatOps message list crashed the Settings page**: a message `response` is an object
  (`{text, …}`), not a string, and rendering it directly is a React "Objects are not valid as a
  child" throw. Typing the payload surfaced it; the list now renders `response.text`.
- **Registering a Grafana plugin silently discarded the URL**: the form sent `url`, a field the
  engine never reads (it stores `grafana_url`), so every registration was saved with an empty URL
  and the table's URL column always showed a dash.
- **Insights crashed on an empty history range**: `items` arrives as `null`, not `[]`, when the
  engine's var-declared slice stays empty.
- The chart README pointed at `gitlab.com` for the cosign identity and issuer while CI runs on
  `github.com`, and installed values from a `raw.githubusercontent.com` URL that 404s (there is
  no public mirror); the preset is now taken from the packaged chart itself.
- The committed chart resolved images to a tag that was never published: `appVersion` had no `v`
  while the release pipeline pushes `${CI_COMMIT_TAG}` (`v0.1.28`), so a `helm install` from a
  checkout asked the registry for `nxs-anomaly:0.1.27`.
- The restore drill's throwaway postgres could be declared ready while `initdb`'s temporary
  socket-only server was still up, dropping the next connection; readiness is now checked over TCP.
- `WorkerCycleStuck` alert compared a duration to a timestamp and fired constantly; it now
  uses a dedicated last-cycle timestamp metric.
