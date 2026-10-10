-- Ownership is historical provenance: no cascading relationship or guessed backfill.
ALTER TABLE accounts ADD COLUMN IF NOT EXISTS supplier_user_id BIGINT;
ALTER TABLE accounts ADD COLUMN IF NOT EXISTS supplier_paused BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE accounts ADD COLUMN IF NOT EXISTS supplier_notes TEXT;
CREATE INDEX IF NOT EXISTS accounts_supplier_user_id_idx ON accounts (supplier_user_id);
ALTER TABLE proxies ADD COLUMN IF NOT EXISTS supplier_user_id BIGINT;
CREATE INDEX IF NOT EXISTS proxies_supplier_user_id_idx ON proxies (supplier_user_id);
ALTER TABLE users ADD COLUMN IF NOT EXISTS auth_version BIGINT NOT NULL DEFAULT 0;
