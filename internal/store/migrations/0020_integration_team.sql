-- Team ownership for integrations.
--
-- Integrations are where a team boundary can be drawn without denormalising:
-- alerts and alert groups already carry integration_id, so "the alert groups my
-- team owns" is a filter over that column rather than a copy of team_id that
-- would go stale the moment an integration is reassigned. Schedules and ChatOps
-- channels already had team_id of their own.
--
-- NULL means unassigned, and unassigned objects are visible to everyone. That
-- is what makes enabling NXS_ANOMALY_TEAM_SCOPING a no-op on a deployment that
-- has never used teams: every existing row is NULL, so nothing changes until
-- somebody assigns a team.

ALTER TABLE nxs_anomaly_integrations ADD COLUMN IF NOT EXISTS team_id TEXT;

-- Scoped listing filters on this column on every request, and the set of teams
-- is small, so a plain index over the assigned rows is enough.
CREATE INDEX IF NOT EXISTS nxs_anomaly_integrations_team_idx
    ON nxs_anomaly_integrations (team_id)
    WHERE team_id IS NOT NULL;

-- Membership lookup ("which teams is this user in") runs once per scoped
-- request and is answered by containment over the member_ids array.
CREATE INDEX IF NOT EXISTS nxs_anomaly_teams_members_idx
    ON nxs_anomaly_teams USING GIN ((data -> 'member_ids'));
