-- 04-data-model.md: ALERT_DISPATCHES — record of each attempted
-- notification, for exactly-once verification and debugging.
CREATE TABLE alert_dispatches (
    id bigserial PRIMARY KEY,
    incident_id bigint NOT NULL REFERENCES incidents (id),
    alert_channel_id uuid NOT NULL REFERENCES alert_channels (id),
    kind text NOT NULL CHECK (kind IN ('opened', 'resolved')),
    dispatched_at timestamptz NOT NULL DEFAULT now(),
    delivery_confirmed boolean NOT NULL DEFAULT false
);

CREATE INDEX alert_dispatches_incident_id_idx ON alert_dispatches (incident_id);
