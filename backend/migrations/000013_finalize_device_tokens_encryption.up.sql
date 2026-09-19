-- Contract half of 000012's expand/contract pair. Requires
-- backend/cmd/encrypt-device-tokens to have already backfilled
-- token_encrypted/token_hash for every row 000012 left with only a
-- plaintext token — this migration fails loudly (a NOT NULL violation)
-- rather than silently dropping an unencrypted row's only value if that
-- backfill step was skipped. A fresh database (every column always written
-- through the encrypted path, e.g. this project's own CI) has no rows and
-- nothing to fail on.
ALTER TABLE device_tokens ALTER COLUMN token_encrypted SET NOT NULL;
ALTER TABLE device_tokens ALTER COLUMN token_hash SET NOT NULL;

ALTER TABLE device_tokens DROP CONSTRAINT device_tokens_provider_token_key;
DROP INDEX device_tokens_provider_token_hash_uidx;
ALTER TABLE device_tokens
    ADD CONSTRAINT device_tokens_provider_token_hash_key UNIQUE (provider, token_hash);

-- The plaintext column's job is now done by token_encrypted/token_hash.
ALTER TABLE device_tokens DROP COLUMN token;
