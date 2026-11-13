package store

import "context"

// alertStatusChunk caps the rows one statement of SetAlertStatusForGroups
// rewrites. Each chunk is its own statement and commits on its own, so a call
// that runs out of time keeps the chunks it finished instead of rolling back
// all of them.
const alertStatusChunk = 2000

// SetAlertStatusForGroups rewrites the status of every alert belonging to the
// named groups.
//
// Alerts were write-once at ingest: their status said what the source last
// reported and nothing else ever touched it. That left an alert reading
// "firing" forever after an operator closed its group, which is what the
// alerts list and the group's alerts tab then showed. The group's lifecycle now
// carries to its members, and this is where that write happens.
//
// Direct UPDATEs rather than a load-mutate-save through the collection cycle:
// a busy group can hold thousands of alerts, and pulling them all through the
// mutator would put the cost of a resolve on the size of the group's history —
// on the ingest path, on every event. The predicate on status keeps the write
// to the rows that actually change.
//
// The write goes in chunks of alertStatusChunk rows. One statement for the
// whole set timed out on a bulk resolve of 200 groups of ~65 alerts each right
// after a storm (13 000 rows, 1.10.2 audit) and, being one statement, saved
// none of them.
//
// Nothing here is transactional with the group transition on purpose. The call
// runs after that transaction commits, so a failure leaves the group closed and
// some of its alerts still reading "firing"; the worker's repair sweep
// (RepairResolvedGroupAlerts) finishes the job. The reverse order would let a
// database blip roll back an operator's resolve.
func (s *pgStore) SetAlertStatusForGroups(ctx context.Context, groupIDs []string, status string) (int, error) {
	if len(groupIDs) == 0 || status == "" {
		return 0, nil
	}
	total := 0
	for {
		tag, err := s.pool.Exec(ctx, `
			UPDATE nxs_anomaly_alerts
			   SET data = jsonb_set(data, '{status}', to_jsonb($2::text), true),
			       status = $2,
			       updated_at = now()
			 WHERE id IN (
			       SELECT id FROM nxs_anomaly_alerts
			        WHERE alert_group_id = ANY($1::text[])
			          AND status IS DISTINCT FROM $2
			        LIMIT $3)`, groupIDs, status, alertStatusChunk)
		if err != nil {
			return total, err
		}
		n := int(tag.RowsAffected())
		total += n
		if n < alertStatusChunk {
			return total, nil
		}
	}
}

// RepairResolvedGroupAlerts resolves up to limit alerts that still read
// "firing" although their group is resolved, and returns how many it changed.
//
// It is the safety net under SetAlertStatusForGroups, whose post-commit write
// can fail or time out and has nobody to retry it. A firing alert never joins
// a resolved group (ingest only attaches to open ones), so a resolved group
// with a firing alert is always a missed write, never live state. The scan
// starts from the partial index of firing alerts, which is small next to the
// table.
func (s *pgStore) RepairResolvedGroupAlerts(ctx context.Context, limit int) (int, error) {
	if limit <= 0 {
		return 0, nil
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE nxs_anomaly_alerts
		   SET data = jsonb_set(data, '{status}', '"resolved"', true),
		       status = 'resolved',
		       updated_at = now()
		 WHERE id IN (
		       SELECT a.id FROM nxs_anomaly_alerts a
		         JOIN nxs_anomaly_alert_groups g ON g.id = a.alert_group_id
		        WHERE a.status = 'firing'
		          AND g.status = 'resolved'
		        LIMIT $1)
		   AND status = 'firing'`, limit)
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}
