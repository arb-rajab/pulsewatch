-- B-012 security audit (06-security-threat-model.md T-09): device_tokens.token
-- was stored in plaintext. ADR-0007's "What is and isn't a secret here"
-- argued that was a deliberate, defensible choice (a device token grants
-- nothing without the provider credential that already is encrypted, and
-- RegisterDeviceToken's upsert must match the column by exact value, which
-- AES-256-GCM's randomised-nonce ciphertext cannot support) — this session
-- revisits that call, since the accepted-risk row itself names "any session
-- that treats device_tokens as equally sensitive to alert_channels" as its
-- own revisit trigger.
--
-- The exact-match-upsert problem ADR-0007 identified is real and still
-- applies; this migration solves it instead of re-accepting it: token_hash
-- is a deterministic HMAC-SHA256(token) blind index (RegisterDeviceToken's
-- ON CONFLICT target moves to it), and token_encrypted carries the real
-- AES-256-GCM ciphertext (alerting.EncryptDestination, the exact helper
-- alert_channels.destination_encrypted already uses) that PushDispatcher
-- decrypts at send time.
--
-- This is an expand/contract pair (000012 expands, 000013 contracts) rather
-- than a single rewriting migration, so a real deployment is never mid-
-- migration with a row this session's own tooling can't read: 000012 only
-- adds columns, backend/cmd/encrypt-device-tokens backfills every existing
-- plaintext row's token_encrypted/token_hash from token, and only then does
-- 000013 enforce NOT NULL, move the uniqueness constraint onto token_hash,
-- and drop the plaintext token column for good.
ALTER TABLE device_tokens ADD COLUMN token_encrypted text;
ALTER TABLE device_tokens ADD COLUMN token_hash text;

-- Enforced from the moment these columns exist (not deferred to 000013) so
-- a registration written after this migration, before the backfill/contract
-- migration runs, still can't collide with another provider/token pair.
-- Partial (WHERE token_hash IS NOT NULL) because pre-existing rows have no
-- token_hash yet until the backfill runs.
CREATE UNIQUE INDEX device_tokens_provider_token_hash_uidx
    ON device_tokens (provider, token_hash)
    WHERE token_hash IS NOT NULL;
