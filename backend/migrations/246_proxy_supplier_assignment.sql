ALTER TABLE proxies ADD COLUMN IF NOT EXISTS assigned_supplier_user_id BIGINT REFERENCES users(id);
CREATE INDEX IF NOT EXISTS proxies_assigned_supplier_user_id ON proxies(assigned_supplier_user_id);
ALTER TABLE proxies ADD CONSTRAINT proxy_distinct_supplier_origin CHECK (supplier_user_id IS NULL OR assigned_supplier_user_id IS NULL);
ALTER TABLE proxies ADD CONSTRAINT assigned_proxy_no_direct CHECK (assigned_supplier_user_id IS NULL OR fallback_mode <> 'direct');

-- Validate changed references under proxy row locks, including direct SQL/imports.
CREATE OR REPLACE FUNCTION validate_live_proxy_references() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE targets BIGINT[] := ARRAY[]::BIGINT[]; target BIGINT; p RECORD; owner BIGINT; validate_all BOOLEAN;
BEGIN
 IF NEW.deleted_at IS NOT NULL THEN RETURN NEW; END IF;
 IF TG_OP='INSERT' THEN validate_all:=TRUE; ELSE validate_all:=OLD.deleted_at IS NOT NULL; END IF;
 IF TG_TABLE_NAME='accounts' THEN
  owner:=NEW.supplier_user_id;
  IF validate_all OR NEW.supplier_user_id IS DISTINCT FROM OLD.supplier_user_id THEN
   targets:=ARRAY[NEW.proxy_id,NEW.proxy_fallback_origin_id];
  ELSE
   IF NEW.proxy_id IS DISTINCT FROM OLD.proxy_id THEN targets:=array_append(targets,NEW.proxy_id); END IF;
   IF NEW.proxy_fallback_origin_id IS DISTINCT FROM OLD.proxy_fallback_origin_id THEN targets:=array_append(targets,NEW.proxy_fallback_origin_id); END IF;
  END IF;
  IF NEW.parent_account_id IS NOT NULL AND (validate_all OR NEW.parent_account_id IS DISTINCT FROM OLD.parent_account_id OR NEW.supplier_user_id IS DISTINCT FROM OLD.supplier_user_id) THEN
   -- Serialize child attachment with the parent's proxy binding change.
   PERFORM id FROM accounts WHERE id=NEW.parent_account_id AND deleted_at IS NULL FOR SHARE;
   targets := targets || COALESCE((SELECT ARRAY[proxy_id,proxy_fallback_origin_id] FROM accounts WHERE id=NEW.parent_account_id AND deleted_at IS NULL), ARRAY[]::BIGINT[]);
  END IF;
 ELSE
  owner:=COALESCE(NEW.supplier_user_id,NEW.assigned_supplier_user_id);
  IF validate_all OR NEW.backup_proxy_id IS DISTINCT FROM OLD.backup_proxy_id OR COALESCE(NEW.supplier_user_id,NEW.assigned_supplier_user_id) IS DISTINCT FROM COALESCE(OLD.supplier_user_id,OLD.assigned_supplier_user_id) THEN targets:=ARRAY[NEW.backup_proxy_id]; END IF;
 END IF;
 FOR target IN SELECT DISTINCT v FROM unnest(targets) AS v WHERE v IS NOT NULL ORDER BY v LOOP
  SELECT * INTO p FROM proxies WHERE id=target AND deleted_at IS NULL FOR SHARE;
  IF NOT FOUND THEN RAISE EXCEPTION 'live_proxy_reference target unavailable' USING ERRCODE='23503',CONSTRAINT='live_proxy_reference'; END IF;
  IF p.assigned_supplier_user_id IS NOT NULL THEN
   PERFORM id FROM users WHERE id=p.assigned_supplier_user_id AND role='supplier' AND status='active' AND deleted_at IS NULL FOR SHARE;
   IF NOT FOUND THEN RAISE EXCEPTION 'proxy reference unavailable' USING ERRCODE='23514',CONSTRAINT='proxy_supplier_ownership'; END IF;
  END IF;
  IF TG_TABLE_NAME='proxies' THEN
   IF COALESCE(p.supplier_user_id,p.assigned_supplier_user_id) IS DISTINCT FROM owner THEN RAISE EXCEPTION 'proxy ownership mismatch' USING ERRCODE='23514',CONSTRAINT='proxy_supplier_ownership'; END IF;
  ELSIF p.assigned_supplier_user_id IS NOT NULL THEN
   IF p.assigned_supplier_user_id IS DISTINCT FROM owner OR EXISTS(SELECT 1 FROM accounts WHERE parent_account_id=NEW.id AND deleted_at IS NULL AND supplier_user_id IS DISTINCT FROM p.assigned_supplier_user_id) OR NOT EXISTS(SELECT 1 FROM users WHERE id=p.assigned_supplier_user_id AND role='supplier' AND status='active' AND deleted_at IS NULL) THEN RAISE EXCEPTION 'proxy reference unavailable' USING ERRCODE='23514',CONSTRAINT='proxy_supplier_ownership'; END IF;
  END IF;
 END LOOP;
 RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION validate_proxy_assignment() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='UPDATE' THEN
  IF NEW.supplier_user_id IS DISTINCT FROM OLD.supplier_user_id THEN RAISE EXCEPTION 'proxy creator is immutable' USING ERRCODE='23514'; END IF;
  IF NEW.assigned_supplier_user_id IS NOT DISTINCT FROM OLD.assigned_supplier_user_id THEN RETURN NEW; END IF;
 END IF;
 IF NEW.assigned_supplier_user_id IS NOT NULL THEN
  PERFORM id FROM users WHERE id=NEW.assigned_supplier_user_id AND role='supplier' AND status='active' AND deleted_at IS NULL FOR SHARE;
  IF NOT FOUND THEN RAISE EXCEPTION 'assignment target unavailable' USING ERRCODE='23514',CONSTRAINT='proxy_assignment_target'; END IF;
 END IF;
 IF TG_OP='UPDATE' THEN
  IF EXISTS(SELECT 1 FROM proxies WHERE backup_proxy_id=NEW.id AND deleted_at IS NULL) OR
   EXISTS(SELECT 1 FROM accounts WHERE (proxy_id=NEW.id OR proxy_fallback_origin_id=NEW.id OR parent_account_id IN (SELECT id FROM accounts WHERE (proxy_id=NEW.id OR proxy_fallback_origin_id=NEW.id) AND deleted_at IS NULL)) AND deleted_at IS NULL AND
    (OLD.assigned_supplier_user_id IS NOT NULL OR NEW.assigned_supplier_user_id IS NULL OR supplier_user_id IS DISTINCT FROM NEW.assigned_supplier_user_id)) THEN
   RAISE EXCEPTION 'proxy assignment in use' USING ERRCODE='23514',CONSTRAINT='proxy_assignment_in_use';
  END IF;
 END IF;
 RETURN NEW;
END;
$$;
CREATE TRIGGER proxies_assignment BEFORE INSERT OR UPDATE ON proxies FOR EACH ROW EXECUTE FUNCTION validate_proxy_assignment();
