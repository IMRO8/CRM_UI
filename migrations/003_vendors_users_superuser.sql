ALTER TABLE users ADD COLUMN username text;
UPDATE users SET username=lower(id);
ALTER TABLE users ALTER COLUMN username SET NOT NULL;
CREATE UNIQUE INDEX unique_username ON users(lower(username));
ALTER TABLE users ENABLE ROW LEVEL SECURITY;
CREATE POLICY user_select ON users FOR SELECT USING(true);
CREATE POLICY user_insert ON users FOR INSERT WITH CHECK(is_platform_admin() AND NOT superuser);
CREATE POLICY user_password_update ON users FOR UPDATE USING(is_platform_admin()) WITH CHECK(is_platform_admin());
CREATE TABLE vendors(id text PRIMARY KEY,tenant_id text NOT NULL REFERENCES tenants,deleted boolean NOT NULL DEFAULT false,body jsonb NOT NULL,UNIQUE(id,tenant_id));
ALTER TABLE vendors ENABLE ROW LEVEL SECURITY;
CREATE POLICY vendor_select ON vendors FOR SELECT USING(has_tenant(tenant_id));
CREATE POLICY vendor_insert ON vendors FOR INSERT WITH CHECK(is_platform_admin());
CREATE POLICY vendor_update ON vendors FOR UPDATE USING(is_platform_admin()) WITH CHECK(is_platform_admin());
ALTER TABLE work_orders ADD CONSTRAINT work_order_project_pair UNIQUE(id,tenant_id);
CREATE TABLE work_order_vendors(order_id text PRIMARY KEY,tenant_id text NOT NULL,vendor_id text NOT NULL,snapshot jsonb NOT NULL,FOREIGN KEY(order_id,tenant_id) REFERENCES work_orders(id,tenant_id),FOREIGN KEY(vendor_id,tenant_id) REFERENCES vendors(id,tenant_id));
ALTER TABLE work_order_vendors ENABLE ROW LEVEL SECURITY;
CREATE POLICY order_vendor_select ON work_order_vendors FOR SELECT USING(has_tenant(tenant_id));
CREATE POLICY order_vendor_insert ON work_order_vendors FOR INSERT WITH CHECK(has_tenant(tenant_id));
CREATE TRIGGER immutable_vendor_snapshot BEFORE UPDATE OR DELETE ON work_order_vendors FOR EACH ROW EXECUTE FUNCTION freeze_record();
CREATE OR REPLACE FUNCTION enforce_workflow() RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER SET search_path=public,pg_temp AS $$
DECLARE a amendments%ROWTYPE; st text; total bigint;
BEGIN
 SELECT * INTO a FROM amendments WHERE id=NEW.amendment_id;
 PERFORM 1 FROM work_orders WHERE id=a.order_id FOR UPDATE;
 IF EXISTS(SELECT 1 FROM amendment_events WHERE amendment_id=a.id AND (action='reject' OR (action='approve' AND stage='finance'))) THEN RAISE EXCEPTION 'Amendment is already finalized'; END IF;
 IF NEW.action='submit' THEN
   IF EXISTS(SELECT 1 FROM amendment_events WHERE amendment_id=a.id) THEN RAISE EXCEPTION 'Only drafts can be submitted'; END IF;
   IF NEW.actor_id<>a.created_by AND NOT is_platform_admin() THEN RAISE EXCEPTION 'Only the creator can submit'; END IF;
 ELSE
   IF NEW.actor_id=a.created_by AND NOT is_platform_admin() THEN RAISE EXCEPTION 'Creator cannot review or approve own amendment'; END IF;
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
INSERT INTO vendors VALUES('v1','t1',false,'{"id":"v1","tenantId":"t1","name":"Sri Sai Constructions","trade":"Civil works","contactName":"Srinivas Reddy","phone":"9000000010","email":"vendor1@example.com","gstin":"","address":"Hyderabad, Telangana","deleted":false}'::jsonb);
INSERT INTO vendors VALUES('v2','t1',false,'{"id":"v2","tenantId":"t1","name":"Vertex Electricals","trade":"Electrical","contactName":"Deepak Nair","phone":"9000000011","email":"vendor2@example.com","gstin":"","address":"Hyderabad, Telangana","deleted":false}'::jsonb);
INSERT INTO vendors VALUES('v3','t1',false,'{"id":"v3","tenantId":"t1","name":"AquaFlow Systems","trade":"MEP","contactName":"Meera Patel","phone":"9000000012","email":"vendor3@example.com","gstin":"","address":"Hyderabad, Telangana","deleted":false}'::jsonb);
INSERT INTO vendors VALUES('v4','t1',false,'{"id":"v4","tenantId":"t1","name":"Stonecraft Interiors","trade":"Interiors","contactName":"Arjun Mehta","phone":"9000000013","email":"vendor4@example.com","gstin":"","address":"Hyderabad, Telangana","deleted":false}'::jsonb);
INSERT INTO vendors VALUES('v5','t2',false,'{"id":"v5","tenantId":"t2","name":"Metro Build Contractors","trade":"Civil works","contactName":"Vikram Rao","phone":"9000000014","email":"vendor5@example.com","gstin":"","address":"Bengaluru, Karnataka","deleted":false}'::jsonb);
INSERT INTO vendors VALUES('v6','t2',false,'{"id":"v6","tenantId":"t2","name":"Sri Sai Constructions","trade":"Civil works","contactName":"Srinivas Reddy","phone":"9000000010","email":"vendor1@example.com","gstin":"","address":"Bengaluru project office","deleted":false}'::jsonb);
INSERT INTO work_order_vendors VALUES('w1','t1','v1','{"id":"v1","tenantId":"t1","name":"Sri Sai Constructions","trade":"Civil works","contactName":"Srinivas Reddy","phone":"9000000010","email":"vendor1@example.com","gstin":"","address":"Hyderabad, Telangana","deleted":false}'::jsonb);
INSERT INTO work_order_vendors VALUES('w2','t1','v2','{"id":"v2","tenantId":"t1","name":"Vertex Electricals","trade":"Electrical","contactName":"Deepak Nair","phone":"9000000011","email":"vendor2@example.com","gstin":"","address":"Hyderabad, Telangana","deleted":false}'::jsonb);
INSERT INTO work_order_vendors VALUES('w3','t1','v3','{"id":"v3","tenantId":"t1","name":"AquaFlow Systems","trade":"MEP","contactName":"Meera Patel","phone":"9000000012","email":"vendor3@example.com","gstin":"","address":"Hyderabad, Telangana","deleted":false}'::jsonb);
INSERT INTO work_order_vendors VALUES('w4','t1','v4','{"id":"v4","tenantId":"t1","name":"Stonecraft Interiors","trade":"Interiors","contactName":"Arjun Mehta","phone":"9000000013","email":"vendor4@example.com","gstin":"","address":"Hyderabad, Telangana","deleted":false}'::jsonb);
INSERT INTO work_order_vendors VALUES('w5','t2','v5','{"id":"v5","tenantId":"t2","name":"Metro Build Contractors","trade":"Civil works","contactName":"Vikram Rao","phone":"9000000014","email":"vendor5@example.com","gstin":"","address":"Bengaluru, Karnataka","deleted":false}'::jsonb);
INSERT INTO form_versions VALUES('vendor',1,'{"id":"vendor","name":"Vendor Details","version":1,"fields":[],"createdBy":"u1","createdAt":"2026-09-01T08:00:00Z"}'::jsonb);
UPDATE roles SET body=jsonb_set(body,'{permissions,vendor}','{"actions":["view"],"fields":{"name":"view","trade":"view","contactName":"view","phone":"view","email":"view","gstin":"view","address":"view"}}'::jsonb,true) WHERE id='manager';
UPDATE roles SET body=jsonb_set(body,'{permissions,vendor}','{"actions":["view"],"fields":{"name":"view","trade":"view","contactName":"view","phone":"view","email":"view","gstin":"view","address":"view"}}'::jsonb,true) WHERE id='finance';
UPDATE roles SET body=jsonb_set(body,'{permissions,vendor}','{"actions":["view"],"fields":{"name":"view","trade":"view","contactName":"view","phone":"view","email":"view","gstin":"hidden","address":"view"}}'::jsonb,true) WHERE id='engineer';
