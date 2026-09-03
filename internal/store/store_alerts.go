package store

import "context"

// SetAlertStatusForGroups rewrites the status of every alert belonging to the
// named groups, in one statement per call.
//
// Alerts were write-once at ingest: their status said what the source last
// reported and nothing else ever touched it. That left an alert reading
// "firing" forever after an operator closed its group, which is what the
// alerts list and the group's alerts tab then showed. The group's lifecycle now
// carries to its members, and this is where that write happens.
//
// A single UPDATE rather than a load-mutate-save through the collection cycle:
// a busy group can hold thousands of alerts, and pulling them all through the
// mutator would put the cost of a resolve on the size of the group's history —
// on the ingest path, on every event. The predicate on status keeps the write
// to the rows that actually change.
//
// Nothing here is transactional with the group transition on purpose. The call
// runs after that transaction commits, so a failure leaves the group closed and
// its alerts still reading "firing" — exactly the state the product was in
// before this existed, and a state the next transition corrects. The reverse
// order would let a database blip roll back an operator's resolve.
func (s *pgStore) SetAlertStatusForGroups(ctx context.Context, groupIDs []string, status string) (int, error) {
	if len(groupIDs) == 0 || status == "" {
		return 0, nil
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE nxs_anomaly_alerts
		   SET data = jsonb_set(data, '{status}', to_jsonb($2::text), true),
		       status = $2,
		       updated_at = now()
		 WHERE alert_group_id = ANY($1::text[])
		   AND status IS DISTINCT FROM $2`, groupIDs, status)
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}
