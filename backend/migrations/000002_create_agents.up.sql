-- 04-data-model.md: AGENTS — registered agent instances and their liveness
-- (ADR-0003, NFR-008).
CREATE TABLE agents (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name text NOT NULL,
    credential_hash text NOT NULL,
    report_interval_seconds integer NOT NULL,
    last_heartbeat_at timestamptz,
    registered_at timestamptz NOT NULL DEFAULT now()
);

-- Indexing strategy: "unique index on credential identifier; lookups are
-- always by agent_id, no additional index needed for staleness checks."
CREATE UNIQUE INDEX agents_credential_hash_key ON agents (credential_hash);
