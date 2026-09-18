-- Reverses 000013's schema shape. This is a SQL migration, not the
-- application — it holds no decryption key — so it cannot recover a real
-- plaintext token from token_encrypted; every row's token comes back NULL.
-- That is a real, stated data loss on downgrade, the same honest treatment
-- 000011's own down migration gives its 'push' channel rows, not a silent
-- gap.
ALTER TABLE device_tokens ADD COLUMN token text;

ALTER TABLE device_tokens DROP CONSTRAINT device_tokens_provider_token_hash_key;
CREATE UNIQUE INDEX device_tokens_provider_token_hash_uidx
    ON device_tokens (provider, token_hash)
    WHERE token_hash IS NOT NULL;

ALTER TABLE device_tokens ALTER COLUMN token_encrypted DROP NOT NULL;
ALTER TABLE device_tokens ALTER COLUMN token_hash DROP NOT NULL;
