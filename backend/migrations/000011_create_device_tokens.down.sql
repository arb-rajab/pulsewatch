-- Reverses 000011. Restoring alert_channels' original CHECK requires that no
-- 'push' row survives it, so the rows this migration made possible (and the
-- alert_dispatches rows referencing them, which hold a plain REFERENCES with
-- no ON DELETE CASCADE) are deleted first. That is a real data loss and is
-- stated here rather than hidden: down-migrating past 000011 removes the
-- push channels themselves, exactly as down-migrating past 000008 would.
DELETE FROM alert_dispatches
WHERE alert_channel_id IN (SELECT id FROM alert_channels WHERE type = 'push');

DELETE FROM alert_channels WHERE type = 'push';

DROP TABLE device_tokens;

ALTER TABLE alert_channels
    DROP CONSTRAINT alert_channels_type_check;

ALTER TABLE alert_channels
    ADD CONSTRAINT alert_channels_type_check CHECK (type IN ('webhook', 'email'));
