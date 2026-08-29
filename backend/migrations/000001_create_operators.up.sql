-- 04-data-model.md: OPERATORS — single-operator account (v1: exactly one
-- row in practice, modeled as a table so the schema doesn't change if that
-- ever isn't true).
CREATE TABLE operators (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    email text NOT NULL UNIQUE,
    password_hash text NOT NULL
);
