CREATE TABLE IF NOT EXISTS nxs_anomaly_kafka_outbox (
    id         TEXT        PRIMARY KEY,
    data       JSONB       NOT NULL,
    topic      TEXT        NOT NULL,
    created_at TIMESTAMPTZ NOT NULL
);

-- FIFO ordering for the worker step that reads and publishes outbox events.
CREATE INDEX IF NOT EXISTS nxs_anomaly_kafka_outbox_created_idx
    ON nxs_anomaly_kafka_outbox (created_at);
