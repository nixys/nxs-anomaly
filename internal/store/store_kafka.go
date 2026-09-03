package store

import (
	"context"
	"fmt"
	"strings"
)

// ListKafkaOutboxEvents returns the oldest pending outbox events, up to limit.
func (s *pgStore) ListKafkaOutboxEvents(ctx context.Context, limit int) ([]map[string]any, error) {
	rows, err := s.pool.Query(ctx,
		"SELECT data FROM nxs_anomaly_kafka_outbox ORDER BY created_at LIMIT $1",
		limit)
	if err != nil {
		return nil, err
	}
	return scanRows(rows)
}

// CountKafkaOutboxByTopic returns the number of pending outbox events per topic.
// Rows with a null/empty topic are grouped under "unknown".
func (s *pgStore) CountKafkaOutboxByTopic(ctx context.Context) (map[string]int, error) {
	rows, err := s.pool.Query(ctx,
		"SELECT COALESCE(NULLIF(topic, ''), 'unknown') AS t, COUNT(*) FROM nxs_anomaly_kafka_outbox GROUP BY t")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var topic string
		var n int
		if err := rows.Scan(&topic, &n); err != nil {
			return nil, err
		}
		out[topic] = n
	}
	return out, rows.Err()
}

// DeleteKafkaOutboxEvents removes outbox events by their IDs.
func (s *pgStore) DeleteKafkaOutboxEvents(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	placeholders := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		placeholders[i] = fmt.Sprintf("$%d", i+1)
		args[i] = id
	}
	_, err := s.pool.Exec(ctx,
		fmt.Sprintf("DELETE FROM nxs_anomaly_kafka_outbox WHERE id IN (%s)",
			strings.Join(placeholders, ",")),
		args...)
	return err
}
