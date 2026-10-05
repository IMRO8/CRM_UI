CREATE EXTENSION IF NOT EXISTS pgcrypto;
CREATE TABLE users(id text PRIMARY KEY, name text NOT NULL, title text NOT NULL, superuser boolean NOT NULL DEFAULT false, password_hash text NOT NULL);
CREATE TABLE tenants(id text PRIMARY KEY, code text NOT NULL UNIQUE, body jsonb NOT NULL);
CREATE TABLE roles(id text PRIMARY KEY, body jsonb NOT NULL);
CREATE TABLE memberships(tenant_id text NOT NULL REFERENCES tenants, user_id text NOT NULL REFERENCES users, PRIMARY KEY(tenant_id,user_id));
CREATE TABLE membership_roles(tenant_id text NOT NULL,user_id text NOT NULL,role_id text NOT NULL REFERENCES roles,PRIMARY KEY(tenant_id,user_id,role_id),FOREIGN KEY(tenant_id,user_id) REFERENCES memberships);
CREATE TABLE form_versions(id text NOT NULL, version integer NOT NULL CHECK(version>0),body jsonb NOT NULL,PRIMARY KEY(id,version));
CREATE TABLE rera_accounts(id text PRIMARY KEY,tenant_id text NOT NULL REFERENCES tenants,number text NOT NULL UNIQUE CHECK(number=upper(regexp_replace(number,'[[:space:]]','','g')) AND number ~ '^[0-9]{6,34}$'),body jsonb NOT NULL);
CREATE TABLE work_orders(id text PRIMARY KEY,tenant_id text NOT NULL REFERENCES tenants,number text NOT NULL,base bigint NOT NULL CHECK(base>=0 AND base<=100000000000000),form_id text NOT NULL,form_version integer NOT NULL,created_by text NOT NULL REFERENCES users,body jsonb NOT NULL,UNIQUE(tenant_id,number),FOREIGN KEY(form_id,form_version) REFERENCES form_versions);
CREATE TABLE uploads(id text PRIMARY KEY,tenant_id text NOT NULL REFERENCES tenants,created_by text NOT NULL REFERENCES users,checksum text NOT NULL,body jsonb NOT NULL);
CREATE TABLE amendments(id text PRIMARY KEY,order_id text NOT NULL REFERENCES work_orders,delta bigint NOT NULL CHECK(delta<>0 AND abs(delta)<=100000000000000),created_by text NOT NULL REFERENCES users,upload_id text REFERENCES uploads,body jsonb NOT NULL);
CREATE TABLE amendment_events(id text PRIMARY KEY,amendment_id text NOT NULL REFERENCES amendments,action text NOT NULL CHECK(action IN('submit','review','approve','reject')),stage text NOT NULL DEFAULT '' CHECK(stage IN('','manager','finance')),actor_id text NOT NULL REFERENCES users,body jsonb NOT NULL,CHECK((action='approve')=(stage<>'')));
CREATE UNIQUE INDEX event_action_once ON amendment_events(amendment_id,action,stage);
CREATE UNIQUE INDEX distinct_approvers ON amendment_events(amendment_id,actor_id) WHERE action='approve';
CREATE TABLE audit_log(id text PRIMARY KEY,tenant_id text NOT NULL REFERENCES tenants,actor_id text NOT NULL REFERENCES users,body jsonb NOT NULL);
CREATE TABLE sessions(token_hash text PRIMARY KEY,user_id text NOT NULL REFERENCES users,expires_at timestamptz NOT NULL);
CREATE INDEX work_order_tenant_idx ON work_orders(tenant_id);
CREATE INDEX amendment_order_idx ON amendments(order_id);
CREATE INDEX audit_tenant_idx ON audit_log(tenant_id);
CREATE INDEX membership_user_idx ON memberships(user_id);
CREATE FUNCTION current_actor() RETURNS text LANGUAGE sql STABLE AS $$ SELECT nullif(current_setting('app.user_id',true),'') $$;
CREATE FUNCTION is_platform_admin() RETURNS boolean LANGUAGE sql STABLE SECURITY DEFINER SET search_path=public,pg_temp AS $$ SELECT coalesce((SELECT superuser FROM users WHERE id=current_actor()),false) $$;
CREATE FUNCTION has_tenant(t text) RETURNS boolean LANGUAGE sql STABLE SECURITY DEFINER SET search_path=public,pg_temp AS $$ SELECT is_platform_admin() OR EXISTS(SELECT 1 FROM membership_roles WHERE tenant_id=t AND user_id=current_actor()) $$;
CREATE FUNCTION freeze_record() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'Financial and audit history is immutable; create a new amendment or version'; END $$;
DO $$ DECLARE tbl text; BEGIN FOREACH tbl IN ARRAY ARRAY['work_orders','amendments','amendment_events','audit_log','form_versions','uploads','rera_accounts'] LOOP EXECUTE format('CREATE TRIGGER immutable_history BEFORE UPDATE OR DELETE ON %I FOR EACH ROW EXECUTE FUNCTION freeze_record()',tbl);END LOOP;END $$;
CREATE FUNCTION effective_total(wo text) RETURNS bigint LANGUAGE sql STABLE AS $$ SELECT w.base+coalesce((SELECT sum(a.delta) FROM amendments a WHERE a.order_id=wo AND EXISTS(SELECT 1 FROM amendment_events e WHERE e.amendment_id=a.id AND e.action='approve' AND e.stage='finance')),0)::bigint FROM work_orders w WHERE w.id=wo $$;
CREATE FUNCTION enforce_workflow() RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER SET search_path=public,pg_temp AS $$
DECLARE a amendments%ROWTYPE; st text; total bigint;
BEGIN
 SELECT * INTO a FROM amendments WHERE id=NEW.amendment_id;
 PERFORM 1 FROM work_orders WHERE id=a.order_id FOR UPDATE;
 IF EXISTS(SELECT 1 FROM amendment_events WHERE amendment_id=a.id AND (action='reject' OR (action='approve' AND stage='finance'))) THEN RAISE EXCEPTION 'Amendment is already finalized'; END IF;
 IF NEW.action='submit' THEN
   IF EXISTS(SELECT 1 FROM amendment_events WHERE amendment_id=a.id) THEN RAISE EXCEPTION 'Only drafts can be submitted'; END IF;
   IF NEW.actor_id<>a.created_by AND NOT is_platform_admin() THEN RAISE EXCEPTION 'Only the creator can submit'; END IF;
 ELSE
   IF NEW.actor_id=a.created_by THEN RAISE EXCEPTION 'Creator cannot review or approve own amendment'; END IF;
   IF NOT EXISTS(SELECT 1 FROM amendment_events WHERE amendment_id=a.id AND action='submit') THEN RAISE EXCEPTION 'Submission is required'; END IF;
   IF NEW.action='review' AND EXISTS(SELECT 1 FROM amendment_events WHERE amendment_id=a.id AND action='review') THEN RAISE EXCEPTION 'Review is already complete'; END IF;
   IF NEW.action='approve' THEN
     IF NOT EXISTS(SELECT 1 FROM amendment_events WHERE amendment_id=a.id AND action='review') THEN RAISE EXCEPTION 'Review is required'; END IF;
     IF NEW.stage='finance' AND NOT EXISTS(SELECT 1 FROM amendment_events WHERE amendment_id=a.id AND action='approve' AND stage='manager') THEN RAISE EXCEPTION 'Manager approval is required'; END IF;
     SELECT effective_total(a.order_id)+a.delta INTO total;
     IF total<0 OR total>100000000000000 THEN RAISE EXCEPTION 'Approval blocked: resulting total is outside the allowed range'; END IF;
   END IF;
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER workflow_guard BEFORE INSERT ON amendment_events FOR EACH ROW EXECUTE FUNCTION enforce_workflow();
ALTER TABLE tenants ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_select ON tenants FOR SELECT USING(has_tenant(id));
CREATE POLICY tenant_insert ON tenants FOR INSERT WITH CHECK(is_platform_admin());
ALTER TABLE rera_accounts ENABLE ROW LEVEL SECURITY;
CREATE POLICY rera_select ON rera_accounts FOR SELECT USING(has_tenant(tenant_id));
CREATE POLICY rera_insert ON rera_accounts FOR INSERT WITH CHECK(is_platform_admin());
ALTER TABLE roles ENABLE ROW LEVEL SECURITY;
CREATE POLICY role_select ON roles FOR SELECT USING(true);
CREATE POLICY role_insert ON roles FOR INSERT WITH CHECK(is_platform_admin());
CREATE POLICY role_update ON roles FOR UPDATE USING(is_platform_admin()) WITH CHECK(is_platform_admin());
ALTER TABLE form_versions ENABLE ROW LEVEL SECURITY;
CREATE POLICY form_select ON form_versions FOR SELECT USING(true);
CREATE POLICY form_insert ON form_versions FOR INSERT WITH CHECK(is_platform_admin());
DO $$ DECLARE tbl text; BEGIN FOREACH tbl IN ARRAY ARRAY['memberships','membership_roles'] LOOP
 EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY',tbl);
 EXECUTE format('CREATE POLICY membership_select ON %I FOR SELECT USING(has_tenant(tenant_id))',tbl);
 EXECUTE format('CREATE POLICY membership_manage ON %I FOR ALL USING(is_platform_admin()) WITH CHECK(is_platform_admin())',tbl);
END LOOP;END $$;
DO $$ DECLARE tbl text; BEGIN FOREACH tbl IN ARRAY ARRAY['work_orders','uploads','audit_log'] LOOP
 EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY',tbl);
 EXECUTE format('CREATE POLICY project_select ON %I FOR SELECT USING(has_tenant(tenant_id))',tbl);
 EXECUTE format('CREATE POLICY project_insert ON %I FOR INSERT WITH CHECK(has_tenant(tenant_id))',tbl);
END LOOP;END $$;
ALTER TABLE amendments ENABLE ROW LEVEL SECURITY;
CREATE POLICY amendment_select ON amendments FOR SELECT USING(EXISTS(SELECT 1 FROM work_orders w WHERE w.id=order_id));
CREATE POLICY amendment_insert ON amendments FOR INSERT WITH CHECK(created_by=current_actor() AND EXISTS(SELECT 1 FROM work_orders w WHERE w.id=order_id));
ALTER TABLE amendment_events ENABLE ROW LEVEL SECURITY;
CREATE POLICY event_select ON amendment_events FOR SELECT USING(EXISTS(SELECT 1 FROM amendments a WHERE a.id=amendment_id));
CREATE POLICY event_insert ON amendment_events FOR INSERT WITH CHECK(actor_id=current_actor() AND EXISTS(SELECT 1 FROM amendments a WHERE a.id=amendment_id));
