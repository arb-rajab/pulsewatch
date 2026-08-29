-- 04-data-model.md: TARGETS — operator-registered check configuration
-- (US-001/US-002). agent_id nullable: null means server-executed,
-- non-null means agent-executed (ADR-0003).
CREATE TABLE targets (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    type text NOT NULL CHECK (type IN ('http', 'tcp')),
    url_or_host text NOT NULL,
    port integer,
    body_match_pattern text,
    interval_seconds integer NOT NULL CHECK (interval_seconds BETWEEN 10 AND 86400),
    failure_threshold integer NOT NULL DEFAULT 3,
    timeout_seconds integer NOT NULL,
    agent_id uuid REFERENCES agents (id),
    created_at timestamptz NOT NULL DEFAULT now(),
    deleted_at timestamptz
);

CREATE INDEX targets_agent_id_idx ON targets (agent_id) WHERE agent_id IS NOT NULL;
