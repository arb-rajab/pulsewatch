-- Reverses 000012. Schema-only, so nothing here loses data: token itself was
-- never touched by the up migration.
DROP INDEX device_tokens_provider_token_hash_uidx;
ALTER TABLE device_tokens DROP COLUMN token_hash;
ALTER TABLE device_tokens DROP COLUMN token_encrypted;
