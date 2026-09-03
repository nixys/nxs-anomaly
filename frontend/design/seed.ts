import { adminApi, uniqueSuffix } from '../e2e/helpers';

// Demo content for the design export.
//
// A screenshot of an empty table tells a designer nothing about the product:
// the interesting design problems — how a long alert title wraps, how eight
// severities read next to each other in one column, what a row looks like when
// half its fields are absent — only appear once there is data. So the capture
// seeds a small but deliberately varied installation before it photographs it.
//
// Everything is created through the admin API with a unique suffix, the same
// way the e2e specs do it, so a repeat run adds a fresh set rather than
// colliding with the last one.

// Spread across the two colour maps in components/common.tsx so a single
// screenshot of the alert list exercises every badge colour the product has.
const ALERTS = [
  {
    title: 'PostgreSQL replica lag above 30s',
    severity: 'critical',
    message: 'Replica db-02 is 47s behind the primary and falling further behind.',
    labels: { alertname: 'PostgresReplicaLag', service: 'postgres', instance: 'db-02', team: 'platform' },
  },
  {
    title: 'Ingest queue depth growing',
    severity: 'high',
    message: 'Webhook ingest backlog crossed 10k events with no drain in sight.',
    labels: { alertname: 'IngestBacklog', service: 'anomaly-api', region: 'eu-central-1' },
  },
  {
    title: 'Delivery to Telegram failing',
    severity: 'error',
    message: 'Provider returned 429 for the last 12 attempts.',
    labels: { alertname: 'DeliveryFailure', channel: 'telegram', team: 'oncall' },
  },
  {
    title: 'Certificate expires in 6 days',
    severity: 'warning',
    message: 'alerts.example.com expires on the 7th; renewal has not started.',
    labels: { alertname: 'CertExpiry', domain: 'alerts.example.com' },
  },
  {
    title: 'Disk usage 81% on worker-03',
    severity: 'medium',
    message: 'Growth rate suggests the volume fills in about nine days.',
    labels: { alertname: 'DiskPressure', instance: 'worker-03', mount: '/var/lib' },
  },
  {
    title: 'Nightly backup finished',
    severity: 'info',
    message: 'Backup completed in 14m22s, 8.4 GiB written.',
    labels: { alertname: 'BackupComplete', job: 'pg-basebackup' },
  },
  {
    // No labels and a terse message: the sparse row a designer needs to see
    // next to the rich ones, because that is where a layout usually breaks.
    title: 'Scrape target down',
    severity: 'low',
    message: '',
    labels: {},
  },
] as const;

const RESPONDERS = [
  { name: 'Ada Okonkwo', username: 'ada' },
  { name: 'Ravi Mehta', username: 'ravi' },
  { name: 'Sofia Lindqvist', username: 'sofia' },
] as const;

/**
 * Create a small demo installation and page alerts through it.
 *
 * Returns nothing: the capture walks the UI by route, not by id, so it has no
 * use for the created objects. Failures are thrown — a silent half-seed would
 * produce a screenshot set that looks fine and shows the wrong product.
 */
export async function seedDemoData(): Promise<void> {
  const api = await adminApi();
  const suffix = uniqueSuffix();

  try {
    const users = [];
    for (const responder of RESPONDERS) {
      const res = await api.post('/api/v1/users', {
        data: {
          name: responder.name,
          username: `${responder.username}-${suffix}`,
          // The log channel delivers locally, so seeding needs no external provider.
          notification_targets: [{ type: 'log', target: '' }],
        },
      });
      if (!res.ok()) throw new Error(`seed user ${responder.username}: ${res.status()} ${await res.text()}`);
      users.push(await res.json());
    }

    const chainRes = await api.post('/api/v1/escalation-chains', {
      data: {
        name: `Platform on-call (${suffix})`,
        steps: [
          { kind: 'NOTIFY_USER', user_ids: [users[0].id] },
          { kind: 'NOTIFY_USER', user_ids: [users[1].id, users[2].id] },
        ],
      },
    });
    if (!chainRes.ok()) throw new Error(`seed chain: ${chainRes.status()} ${await chainRes.text()}`);
    const chain = await chainRes.json();

    const integrationRes = await api.post('/api/v1/integrations', {
      data: {
        name: `Prometheus (${suffix})`,
        routes: [
          { name: 'default', match_type: 'all', is_default: true, escalation_chain_id: chain.id },
        ],
      },
    });
    if (!integrationRes.ok())
      throw new Error(`seed integration: ${integrationRes.status()} ${await integrationRes.text()}`);
    const integration = await integrationRes.json();

    for (const alert of ALERTS) {
      const res = await api.post(`/integrations/v1/webhook/${integration.key}`, { data: alert });
      if (!res.ok()) throw new Error(`ingest "${alert.title}": ${res.status()} ${await res.text()}`);
    }
  } finally {
    await api.dispose();
  }
}
