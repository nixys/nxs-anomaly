-- Expression index so DeleteOldChatopsMessages can skip a sequential scan.
CREATE INDEX IF NOT EXISTS nxs_anomaly_chatops_messages_created_at_idx
    ON nxs_anomaly_chatops_messages ((data->>'created_at'));
