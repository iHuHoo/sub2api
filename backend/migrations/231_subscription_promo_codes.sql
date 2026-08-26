ALTER TABLE promo_codes
    ADD COLUMN IF NOT EXISTS purpose VARCHAR(32) NOT NULL DEFAULT 'registration_bonus',
    ADD COLUMN IF NOT EXISTS discount_rate DECIMAL(10,4);

ALTER TABLE payment_orders
    ADD COLUMN IF NOT EXISTS promo_code_id BIGINT,
    ADD COLUMN IF NOT EXISTS promo_code VARCHAR(32),
    ADD COLUMN IF NOT EXISTS original_amount DECIMAL(20,2),
    ADD COLUMN IF NOT EXISTS discount_rate DECIMAL(10,4),
    ADD COLUMN IF NOT EXISTS discount_amount DECIMAL(20,2) NOT NULL DEFAULT 0;

ALTER TABLE promo_code_usages
    ADD COLUMN IF NOT EXISTS payment_order_id BIGINT,
    ADD COLUMN IF NOT EXISTS usage_type VARCHAR(32) NOT NULL DEFAULT 'registration_bonus',
    ADD COLUMN IF NOT EXISTS status VARCHAR(20) NOT NULL DEFAULT 'consumed',
    ADD COLUMN IF NOT EXISTS discount_amount DECIMAL(20,2) NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS reserved_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS consumed_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS released_at TIMESTAMPTZ;

UPDATE promo_code_usages SET consumed_at = used_at
WHERE usage_type = 'registration_bonus' AND status = 'consumed' AND consumed_at IS NULL;

ALTER TABLE promo_code_usages
    DROP CONSTRAINT IF EXISTS promo_code_usages_promo_code_id_user_id_key;
DROP INDEX IF EXISTS promo_code_usages_promo_code_id_user_id;
DROP INDEX IF EXISTS promocodeusage_promo_code_id_user_id;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'payment_orders_promo_code_id_fkey'
          AND conrelid = 'payment_orders'::regclass
    ) THEN
        ALTER TABLE payment_orders
            ADD CONSTRAINT payment_orders_promo_code_id_fkey
            FOREIGN KEY (promo_code_id) REFERENCES promo_codes(id) ON DELETE SET NULL;
    END IF;

    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'promo_code_usages_payment_order_id_fkey'
          AND conrelid = 'promo_code_usages'::regclass
    ) THEN
        ALTER TABLE promo_code_usages
            ADD CONSTRAINT promo_code_usages_payment_order_id_fkey
            FOREIGN KEY (payment_order_id) REFERENCES payment_orders(id) ON DELETE SET NULL;
    END IF;

    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'promo_codes_purpose_check'
          AND conrelid = 'promo_codes'::regclass
    ) THEN
        ALTER TABLE promo_codes ADD CONSTRAINT promo_codes_purpose_check CHECK (
            (purpose = 'registration_bonus' AND discount_rate IS NULL)
            OR
            (purpose = 'subscription_discount' AND discount_rate > 0 AND discount_rate < 1 AND max_uses = 1)
        );
    END IF;

    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'promo_code_usages_type_status_check'
          AND conrelid = 'promo_code_usages'::regclass
    ) THEN
        ALTER TABLE promo_code_usages ADD CONSTRAINT promo_code_usages_type_status_check CHECK (
            (usage_type = 'registration_bonus' AND status = 'consumed')
            OR
            (usage_type = 'subscription_discount' AND status IN ('reserved', 'consumed', 'released'))
        );
    END IF;
END $$;

CREATE UNIQUE INDEX IF NOT EXISTS uq_promo_registration_user
    ON promo_code_usages(promo_code_id, user_id)
    WHERE usage_type = 'registration_bonus';
CREATE UNIQUE INDEX IF NOT EXISTS uq_promo_subscription_reserved
    ON promo_code_usages(promo_code_id)
    WHERE usage_type = 'subscription_discount' AND status = 'reserved';
CREATE UNIQUE INDEX IF NOT EXISTS uq_promo_subscription_consumed
    ON promo_code_usages(promo_code_id)
    WHERE usage_type = 'subscription_discount' AND status = 'consumed';
CREATE UNIQUE INDEX IF NOT EXISTS uq_promo_usage_payment_order
    ON promo_code_usages(payment_order_id)
    WHERE payment_order_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_promo_codes_purpose ON promo_codes(purpose);
CREATE INDEX IF NOT EXISTS idx_payment_orders_promo_code_id ON payment_orders(promo_code_id);
CREATE INDEX IF NOT EXISTS idx_promo_code_usages_status ON promo_code_usages(status);
