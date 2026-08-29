-- 04-data-model.md: ALERT_CHANNELS — operator-configured webhook/email
-- destinations (FR-013/FR-014). destination_encrypted is Secret (FR-023):
-- application-level encrypted at rest; the read API never selects this
-- column after the initial creation response (docs/architecture/openapi.yaml).
CREATE TABLE alert_channels (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    type text NOT NULL CHECK (type IN ('webhook', 'email')),
    destination_encrypted text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);
