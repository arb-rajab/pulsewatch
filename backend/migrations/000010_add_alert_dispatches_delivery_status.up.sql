-- ADR-0006: real webhook delivery replaces the log-only dispatch stub.
-- attempts/last_error give an operator visibility into retry behavior
-- without changing alert_dispatches' one-row-per-attempted-notification
-- shape (04-data-model.md) — a retried delivery still ends in exactly one
-- row, since retries happen synchronously inside a single Dispatch call
-- before this row is ever inserted.
ALTER TABLE alert_dispatches
    ADD COLUMN attempts smallint NOT NULL DEFAULT 1,
    ADD COLUMN last_error text;
