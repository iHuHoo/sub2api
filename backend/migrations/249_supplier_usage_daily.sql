CREATE TABLE supplier_usage_daily (
 day DATE NOT NULL,
 account_id BIGINT NOT NULL,
 supplier_user_id BIGINT NOT NULL,
 account_name TEXT NOT NULL,
 requests BIGINT NOT NULL DEFAULT 0,
 input_tokens BIGINT NOT NULL DEFAULT 0,
 output_tokens BIGINT NOT NULL DEFAULT 0,
 cache_creation_tokens BIGINT NOT NULL DEFAULT 0,
 cache_read_tokens BIGINT NOT NULL DEFAULT 0,
 total_cost NUMERIC(20,10) NOT NULL DEFAULT 0,
 models JSONB NOT NULL DEFAULT '{}',
 enabled BOOLEAN,
 sampled_at TIMESTAMPTZ,
 updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 PRIMARY KEY(day,account_id)
);
CREATE INDEX supplier_usage_daily_supplier_day_idx ON supplier_usage_daily(supplier_user_id,day);
CREATE TABLE supplier_usage_day_closes(day DATE PRIMARY KEY,closed_at TIMESTAMPTZ NOT NULL DEFAULT now());
