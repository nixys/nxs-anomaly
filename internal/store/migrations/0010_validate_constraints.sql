ALTER TABLE nxs_anomaly_teams
    VALIDATE CONSTRAINT nxs_anomaly_teams_name_not_empty;

ALTER TABLE nxs_anomaly_integrations
    VALIDATE CONSTRAINT nxs_anomaly_integrations_key_not_empty;

ALTER TABLE nxs_anomaly_integrations
    VALIDATE CONSTRAINT nxs_anomaly_integrations_type_not_empty;

ALTER TABLE nxs_anomaly_mobile_sessions
    VALIDATE CONSTRAINT nxs_anomaly_mobile_sessions_user_fk;

ALTER TABLE nxs_anomaly_mobile_sessions
    VALIDATE CONSTRAINT nxs_anomaly_mobile_sessions_device_fk;

ALTER TABLE nxs_anomaly_alerts
    VALIDATE CONSTRAINT nxs_anomaly_alerts_integration_fk;

ALTER TABLE nxs_anomaly_alert_groups
    VALIDATE CONSTRAINT nxs_anomaly_alert_groups_integration_fk;

ALTER TABLE nxs_anomaly_notifications
    VALIDATE CONSTRAINT nxs_anomaly_notifications_group_fk;

ALTER TABLE nxs_anomaly_notifications
    VALIDATE CONSTRAINT nxs_anomaly_notifications_user_fk;

ALTER TABLE nxs_anomaly_notification_delivery_attempts
    VALIDATE CONSTRAINT nxs_anomaly_delivery_attempts_notification_fk;
