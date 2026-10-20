CREATE TABLE proxy_subscriptions (
    id BIGSERIAL PRIMARY KEY,
    user_id BIGINT NOT NULL UNIQUE REFERENCES users(id) ON DELETE CASCADE,
    owner_role TEXT NOT NULL CHECK (owner_role IN ('admin', 'supplier')),
    token_hash TEXT NOT NULL UNIQUE,
    version BIGINT NOT NULL DEFAULT 1 CHECK (version > 0),
    expires_at TIMESTAMPTZ NOT NULL,
    revoked_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Application mutations lock users first, then proxy_subscriptions, matching
-- this trigger. Re-enabling a user never restores an old bearer credential.
CREATE FUNCTION revoke_proxy_subscription_on_user_change() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF OLD.role IS DISTINCT FROM NEW.role
       OR (OLD.status = 'active' AND NEW.status IS DISTINCT FROM 'active')
       OR (OLD.deleted_at IS NULL AND NEW.deleted_at IS NOT NULL) THEN
        UPDATE proxy_subscriptions
           SET revoked_at = COALESCE(revoked_at, now()),
               version = version + 1, updated_at = now()
         WHERE user_id = NEW.id;
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER users_revoke_proxy_subscription
AFTER UPDATE OF role, status, deleted_at ON users
FOR EACH ROW EXECUTE FUNCTION revoke_proxy_subscription_on_user_change();
