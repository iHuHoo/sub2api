-- A foreign key permits references to soft-deleted rows. Validate every new/changed
-- live reference under a target row lock, including writes outside repositories.
-- Historical references remain intact and unrelated edits do not revalidate them.
CREATE OR REPLACE FUNCTION validate_live_proxy_references() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    targets BIGINT[] := ARRAY[]::BIGINT[];
    target BIGINT;
    validate_all BOOLEAN;
BEGIN
    IF NEW.deleted_at IS NOT NULL THEN
        RETURN NEW;
    END IF;
    IF TG_OP = 'INSERT' THEN
        validate_all := TRUE;
    ELSE
        validate_all := OLD.deleted_at IS NOT NULL;
    END IF;
    IF TG_TABLE_NAME = 'accounts' THEN
        IF validate_all THEN
            targets := ARRAY[NEW.proxy_id, NEW.proxy_fallback_origin_id];
        ELSE
            IF NEW.proxy_id IS DISTINCT FROM OLD.proxy_id THEN
                targets := array_append(targets, NEW.proxy_id);
            END IF;
            IF NEW.proxy_fallback_origin_id IS DISTINCT FROM OLD.proxy_fallback_origin_id THEN
                targets := array_append(targets, NEW.proxy_fallback_origin_id);
            END IF;
        END IF;
    ELSE
        IF validate_all THEN
            targets := ARRAY[NEW.backup_proxy_id];
        ELSIF NEW.backup_proxy_id IS DISTINCT FROM OLD.backup_proxy_id THEN
            targets := ARRAY[NEW.backup_proxy_id];
        END IF;
    END IF;
    -- Lock targets in ID order, hold locks until commit. If a deletion already
    -- holds a target lock, PostgreSQL rechecks the live predicate after waiting.
    FOR target IN SELECT DISTINCT v FROM unnest(targets) AS v WHERE v IS NOT NULL ORDER BY v LOOP
        PERFORM id FROM proxies WHERE id = target AND deleted_at IS NULL FOR SHARE;
        IF NOT FOUND THEN
            RAISE EXCEPTION 'live_proxy_reference target unavailable'
                USING ERRCODE = '23503', CONSTRAINT = 'live_proxy_reference';
        END IF;
    END LOOP;
    RETURN NEW;
END;
$$;
DROP TRIGGER IF EXISTS accounts_live_proxy_references ON accounts;
CREATE TRIGGER accounts_live_proxy_references BEFORE INSERT OR UPDATE ON accounts
FOR EACH ROW EXECUTE FUNCTION validate_live_proxy_references();
DROP TRIGGER IF EXISTS proxies_live_proxy_references ON proxies;
CREATE TRIGGER proxies_live_proxy_references BEFORE INSERT OR UPDATE ON proxies
FOR EACH ROW EXECUTE FUNCTION validate_live_proxy_references();
