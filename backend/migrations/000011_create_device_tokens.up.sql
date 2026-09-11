-- ADR-0007: mobile push as a third alert channel type.
--
-- alert_channels gains 'push'. A push channel's destination_encrypted holds
-- the *provider credential* (an FCM service-account JSON, or an APNs .p8
-- signing key plus its key/team/topic identifiers), not a single delivery
-- address — the addresses are the device tokens below, and one push channel
-- fans out to every live token registered for its provider. Same FR-023
-- encryption-at-rest treatment as a webhook URL, for a strictly more
-- sensitive secret.
ALTER TABLE alert_channels
    DROP CONSTRAINT alert_channels_type_check;

ALTER TABLE alert_channels
    ADD CONSTRAINT alert_channels_type_check CHECK (type IN ('webhook', 'email', 'push'));

-- DEVICE_TOKENS — one row per (provider, device token) an operator's mobile
-- app has registered (FR-013's notification surface extended to push).
--
-- token is the provider-issued device token. It is NOT encrypted at rest,
-- unlike alert_channels.destination_encrypted, and that difference is
-- deliberate: a device token is not a credential — holding one grants
-- nothing without the provider credential that is encrypted, it is rotated
-- by the device itself, and it must be matched by exact value on every
-- re-registration (an upsert on a deterministic column), which
-- randomised-nonce AES-GCM ciphertext structurally cannot support. See
-- ADR-0007's "What is and isn't a secret here" section.
--
-- dead_at/dead_reason are the push-specific delivery outcome ADR-0007 adds:
-- a provider telling us a token is unregistered or invalid is permanent,
-- never retried, and must stop future fan-out from paying for it.
CREATE TABLE device_tokens (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    operator_id uuid NOT NULL REFERENCES operators (id) ON DELETE CASCADE,
    provider text NOT NULL CHECK (provider IN ('fcm', 'apns')),
    platform text NOT NULL CHECK (platform IN ('ios', 'android')),
    token text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    last_registered_at timestamptz NOT NULL DEFAULT now(),
    last_delivered_at timestamptz,
    revoked_at timestamptz,
    dead_at timestamptz,
    dead_reason text,
    UNIQUE (provider, token)
);

-- The one query the dispatch hot path runs: every live token for one
-- provider. Partial, because dead/revoked rows are kept for operator
-- visibility but must never be scanned during fan-out.
CREATE INDEX device_tokens_live_idx
    ON device_tokens (provider)
    WHERE revoked_at IS NULL AND dead_at IS NULL;

CREATE INDEX device_tokens_operator_id_idx ON device_tokens (operator_id);
