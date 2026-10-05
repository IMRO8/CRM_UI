ALTER TABLE tenants ADD COLUMN deleted boolean NOT NULL DEFAULT false;
CREATE POLICY tenant_update ON tenants FOR UPDATE USING(is_platform_admin()) WITH CHECK(is_platform_admin());
CREATE OR REPLACE FUNCTION has_tenant(t text) RETURNS boolean LANGUAGE sql STABLE SECURITY DEFINER SET search_path=public,pg_temp AS $$ SELECT is_platform_admin() OR EXISTS(SELECT 1 FROM membership_roles m JOIN tenants p ON p.id=m.tenant_id WHERE m.tenant_id=t AND m.user_id=current_actor() AND NOT p.deleted) $$;
CREATE FUNCTION project_form_action(t text,f text,a text) RETURNS boolean LANGUAGE sql STABLE SECURITY DEFINER SET search_path=public,pg_temp AS $$ SELECT is_platform_admin() OR EXISTS(SELECT 1 FROM membership_roles m JOIN roles r ON r.id=m.role_id WHERE m.tenant_id=t AND m.user_id=current_actor() AND (r.body->'permissions'->f->'actions') ? a) $$;
CREATE FUNCTION project_stage(t text,st text) RETURNS boolean LANGUAGE sql STABLE SECURITY DEFINER SET search_path=public,pg_temp AS $$ SELECT is_platform_admin() OR EXISTS(SELECT 1 FROM membership_roles m JOIN roles r ON r.id=m.role_id WHERE m.tenant_id=t AND m.user_id=current_actor() AND r.body->>'stage'=st) $$;
CREATE TABLE purchase_orders(id text PRIMARY KEY,tenant_id text NOT NULL REFERENCES tenants,number text NOT NULL,total bigint NOT NULL CHECK(total>=0 AND total<=100000000000000),vendor_id text NOT NULL,form_id text NOT NULL DEFAULT 'purchase_order' CHECK(form_id='purchase_order'),form_version integer NOT NULL,created_by text NOT NULL REFERENCES users,body jsonb NOT NULL,UNIQUE(tenant_id,number),UNIQUE(id,tenant_id),FOREIGN KEY(vendor_id,tenant_id) REFERENCES vendors(id,tenant_id),FOREIGN KEY(form_id,form_version) REFERENCES form_versions);
CREATE TABLE purchase_events(id text PRIMARY KEY,purchase_order_id text NOT NULL REFERENCES purchase_orders,action text NOT NULL CHECK(action IN('submit','review','approve','reject')),stage text NOT NULL DEFAULT '' CHECK(stage IN('','manager','finance')),actor_id text NOT NULL REFERENCES users,body jsonb NOT NULL,CHECK((action='approve')=(stage<>'')));
CREATE UNIQUE INDEX po_action_once ON purchase_events(purchase_order_id,action,stage);
CREATE UNIQUE INDEX po_distinct_approvers ON purchase_events(purchase_order_id,actor_id) WHERE action='approve';
CREATE TABLE stock_lots(id text PRIMARY KEY,purchase_order_id text NOT NULL,origin_tenant_id text NOT NULL,line_id text NOT NULL UNIQUE,body jsonb NOT NULL,FOREIGN KEY(purchase_order_id,origin_tenant_id) REFERENCES purchase_orders(id,tenant_id));
CREATE TABLE stock_movements(id text PRIMARY KEY,lot_id text NOT NULL REFERENCES stock_lots,tenant_id text NOT NULL REFERENCES tenants,kind text NOT NULL CHECK(kind IN('receipt','transfer-in','transfer-out','issue')),quantity_milli bigint NOT NULL CHECK(quantity_milli<>0 AND abs(quantity_milli)<=1000000000),transfer_id text,actor_id text NOT NULL REFERENCES users,body jsonb NOT NULL,CHECK((kind IN('receipt','transfer-in'))=(quantity_milli>0)));
CREATE INDEX stock_project_lot ON stock_movements(tenant_id,lot_id);
CREATE INDEX stock_transfer ON stock_movements(transfer_id);
CREATE FUNCTION purchase_approved(p text) RETURNS boolean LANGUAGE sql STABLE AS $$ SELECT EXISTS(SELECT 1 FROM purchase_events WHERE purchase_order_id=p AND action='approve' AND stage='finance') $$;
CREATE FUNCTION purchase_workflow_guard() RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER SET search_path=public,pg_temp AS $$
DECLARE p purchase_orders%ROWTYPE;
BEGIN
 SELECT * INTO p FROM purchase_orders WHERE id=NEW.purchase_order_id FOR UPDATE;
 IF NEW.actor_id IS DISTINCT FROM current_actor() OR NOT has_tenant(p.tenant_id) OR NOT project_form_action(p.tenant_id,'purchase_order',NEW.action) THEN RAISE EXCEPTION 'Purchase order action permission is required'; END IF;
 IF EXISTS(SELECT 1 FROM tenants WHERE id=p.tenant_id AND deleted) THEN RAISE EXCEPTION 'Project is deleted'; END IF;
 IF purchase_approved(p.id) OR EXISTS(SELECT 1 FROM purchase_events WHERE purchase_order_id=p.id AND action='reject') THEN RAISE EXCEPTION 'Purchase order is already finalized'; END IF;
 IF NEW.action='submit' THEN
  IF EXISTS(SELECT 1 FROM purchase_events WHERE purchase_order_id=p.id) THEN RAISE EXCEPTION 'Only drafts can be submitted'; END IF;
  IF p.created_by<>NEW.actor_id AND NOT is_platform_admin() THEN RAISE EXCEPTION 'Only the creator can submit'; END IF;
 ELSE
  IF p.created_by=NEW.actor_id AND NOT is_platform_admin() THEN RAISE EXCEPTION 'Creator cannot review or approve own purchase order'; END IF;
  IF NOT EXISTS(SELECT 1 FROM purchase_events WHERE purchase_order_id=p.id AND action='submit') THEN RAISE EXCEPTION 'Submission is required'; END IF;
  IF NEW.action='approve' THEN
   IF NOT EXISTS(SELECT 1 FROM purchase_events WHERE purchase_order_id=p.id AND action='review') THEN RAISE EXCEPTION 'Review is required'; END IF;
   IF NOT project_stage(p.tenant_id,NEW.stage) THEN RAISE EXCEPTION 'Approval stage permission is required'; END IF;
   IF NEW.stage='finance' AND NOT EXISTS(SELECT 1 FROM purchase_events WHERE purchase_order_id=p.id AND action='approve' AND stage='manager') THEN RAISE EXCEPTION 'Manager approval is required'; END IF;
  END IF;
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER po_workflow BEFORE INSERT ON purchase_events FOR EACH ROW EXECUTE FUNCTION purchase_workflow_guard();
CREATE FUNCTION purchase_draft_guard() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE l jsonb; n numeric=0;
BEGIN
 IF TG_OP='DELETE' THEN RAISE EXCEPTION 'Purchase order history is retained'; END IF;
 IF TG_OP='UPDATE' THEN
  IF NEW.id<>OLD.id OR NEW.tenant_id<>OLD.tenant_id OR NEW.created_by<>OLD.created_by OR NEW.form_version<>OLD.form_version OR EXISTS(SELECT 1 FROM purchase_events WHERE purchase_order_id=OLD.id) THEN RAISE EXCEPTION 'Only a draft purchase order can be edited'; END IF;
  IF OLD.created_by<>current_actor() AND NOT is_platform_admin() THEN RAISE EXCEPTION 'Only the creator can edit a draft'; END IF;
 END IF;
 IF jsonb_typeof(NEW.body->'lines') IS DISTINCT FROM 'array' OR jsonb_array_length(NEW.body->'lines') NOT BETWEEN 1 AND 100 THEN RAISE EXCEPTION 'Add material lines'; END IF;
 FOR l IN SELECT * FROM jsonb_array_elements(NEW.body->'lines') LOOP
  IF coalesce(l->>'kind','') NOT IN('purchase','rental') OR coalesce(l->>'material','')='' OR coalesce(l->>'unit','')='' OR coalesce(l->>'id','')='' OR NOT coalesce(l->>'quantityMilli','') ~ '^\d+$' OR NOT coalesce(l->>'rate','') ~ '^\d+$' OR (l->>'quantityMilli')::numeric NOT BETWEEN 1 AND 1000000000 OR (l->>'rate')::numeric NOT BETWEEN 0 AND 100000000000 THEN RAISE EXCEPTION 'Invalid material line'; END IF;
  n=n+round((l->>'quantityMilli')::numeric*(l->>'rate')::numeric/1000);
 END LOOP;
 IF n<>NEW.total OR (SELECT count(*) FROM jsonb_array_elements(NEW.body->'lines'))<>(SELECT count(DISTINCT item->>'id') FROM jsonb_array_elements(NEW.body->'lines') item) THEN RAISE EXCEPTION 'Line totals or IDs are invalid'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER po_draft BEFORE INSERT OR UPDATE OR DELETE ON purchase_orders FOR EACH ROW EXECUTE FUNCTION purchase_draft_guard();
CREATE FUNCTION stock_lot_guard() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF NOT purchase_approved(NEW.purchase_order_id) OR NOT EXISTS(SELECT 1 FROM purchase_orders p,jsonb_array_elements(p.body->'lines') l WHERE p.id=NEW.purchase_order_id AND l->>'id'=NEW.line_id AND l->>'kind'='purchase') THEN RAISE EXCEPTION 'Only approved purchased materials can enter owned inventory'; END IF;RETURN NEW;END $$;
CREATE TRIGGER lot_purchase_only BEFORE INSERT ON stock_lots FOR EACH ROW EXECUTE FUNCTION stock_lot_guard();
CREATE FUNCTION stock_guard() RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER SET search_path=public,pg_temp AS $$
DECLARE lot stock_lots%ROWTYPE; ordered bigint; received bigint; stock bigint;
BEGIN
 SELECT * INTO lot FROM stock_lots WHERE id=NEW.lot_id FOR UPDATE;
 IF NEW.actor_id IS DISTINCT FROM current_actor() OR NOT has_tenant(NEW.tenant_id) OR NOT project_form_action(NEW.tenant_id,'inventory',CASE WHEN NEW.kind IN('receipt','transfer-in') THEN 'create' ELSE 'edit' END) THEN RAISE EXCEPTION 'Inventory movement permission is required'; END IF;
 IF EXISTS(SELECT 1 FROM tenants WHERE id=NEW.tenant_id AND deleted) THEN RAISE EXCEPTION 'Project is deleted'; END IF;
 IF NEW.kind='receipt' THEN
  IF NEW.tenant_id<>lot.origin_tenant_id THEN RAISE EXCEPTION 'Receipt must enter purchasing project'; END IF;
  SELECT (l->>'quantityMilli')::bigint INTO ordered FROM purchase_orders p,jsonb_array_elements(p.body->'lines') l WHERE p.id=lot.purchase_order_id AND l->>'id'=lot.line_id;
  SELECT coalesce(sum(quantity_milli),0) INTO received FROM stock_movements WHERE lot_id=lot.id AND kind='receipt';
  IF received+NEW.quantity_milli>ordered THEN RAISE EXCEPTION 'Receipt exceeds remaining ordered quantity'; END IF;
 ELSIF NEW.quantity_milli<0 THEN
  SELECT coalesce(sum(quantity_milli),0) INTO stock FROM stock_movements WHERE lot_id=lot.id AND tenant_id=NEW.tenant_id;
  IF stock+NEW.quantity_milli<0 THEN RAISE EXCEPTION 'Quantity exceeds available project stock'; END IF;
 END IF;
 IF NEW.kind IN('transfer-in','transfer-out') AND NEW.transfer_id IS NULL THEN RAISE EXCEPTION 'Transfer reference is required'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER stock_validation BEFORE INSERT ON stock_movements FOR EACH ROW EXECUTE FUNCTION stock_guard();
CREATE FUNCTION balanced_transfer() RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER SET search_path=public,pg_temp AS $$ BEGIN
 IF NEW.kind IN('transfer-in','transfer-out') AND NOT EXISTS(SELECT 1 FROM stock_movements a JOIN stock_movements b ON b.transfer_id=a.transfer_id AND b.lot_id=a.lot_id AND b.quantity_milli=-a.quantity_milli AND b.tenant_id<>a.tenant_id AND b.kind='transfer-in' WHERE a.transfer_id=NEW.transfer_id AND a.kind='transfer-out' GROUP BY a.transfer_id HAVING count(*)=1) THEN RAISE EXCEPTION 'Transfer needs matching source and destination entries'; END IF;
 IF NEW.kind IN('transfer-in','transfer-out') AND (SELECT count(*) FROM stock_movements WHERE transfer_id=NEW.transfer_id)<>2 THEN RAISE EXCEPTION 'Transfer must have exactly two entries'; END IF;
 RETURN NULL;END $$;
CREATE CONSTRAINT TRIGGER transfer_pair AFTER INSERT ON stock_movements DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION balanced_transfer();
DO $$ DECLARE tbl text; BEGIN FOREACH tbl IN ARRAY ARRAY['purchase_events','stock_lots','stock_movements'] LOOP EXECUTE format('CREATE TRIGGER retained_history BEFORE UPDATE OR DELETE ON %I FOR EACH ROW EXECUTE FUNCTION freeze_record()',tbl); END LOOP;END $$;
ALTER TABLE purchase_orders ENABLE ROW LEVEL SECURITY;
CREATE POLICY po_select ON purchase_orders FOR SELECT USING(has_tenant(tenant_id));
CREATE POLICY po_insert ON purchase_orders FOR INSERT WITH CHECK(has_tenant(tenant_id) AND created_by=current_actor() AND project_form_action(tenant_id,'purchase_order','create'));
CREATE POLICY po_update ON purchase_orders FOR UPDATE USING(has_tenant(tenant_id) AND project_form_action(tenant_id,'purchase_order','edit')) WITH CHECK(has_tenant(tenant_id));
ALTER TABLE purchase_events ENABLE ROW LEVEL SECURITY;
CREATE POLICY po_event_select ON purchase_events FOR SELECT USING(EXISTS(SELECT 1 FROM purchase_orders p WHERE p.id=purchase_order_id));
CREATE POLICY po_event_insert ON purchase_events FOR INSERT WITH CHECK(actor_id=current_actor() AND EXISTS(SELECT 1 FROM purchase_orders p WHERE p.id=purchase_order_id));
ALTER TABLE stock_lots ENABLE ROW LEVEL SECURITY;
ALTER TABLE stock_movements ENABLE ROW LEVEL SECURITY;
CREATE POLICY stock_select ON stock_movements FOR SELECT USING(has_tenant(tenant_id));
CREATE POLICY stock_insert ON stock_movements FOR INSERT WITH CHECK(has_tenant(tenant_id) AND actor_id=current_actor());
CREATE POLICY lot_select ON stock_lots FOR SELECT USING(has_tenant(origin_tenant_id) OR EXISTS(SELECT 1 FROM stock_movements m WHERE m.lot_id=stock_lots.id));
CREATE POLICY lot_insert ON stock_lots FOR INSERT WITH CHECK(has_tenant(origin_tenant_id) AND project_form_action(origin_tenant_id,'inventory','create'));
CREATE FUNCTION project_delete_guard() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF NEW.deleted AND NOT OLD.deleted AND EXISTS(SELECT 1 FROM stock_movements WHERE tenant_id=OLD.id GROUP BY lot_id HAVING sum(quantity_milli)>0) THEN RAISE EXCEPTION 'Transfer or issue remaining inventory before deleting this project'; END IF;RETURN NEW;END $$;
CREATE TRIGGER project_stock_check BEFORE UPDATE ON tenants FOR EACH ROW EXECUTE FUNCTION project_delete_guard();
UPDATE roles SET body=jsonb_set(jsonb_set(body,'{permissions,purchase_order}','{"actions":["view","create","edit","submit","review","approve","reject"],"fields":{"amount":"edit","description":"edit","materials":"edit"}}'::jsonb,true),'{permissions,inventory}','{"actions":["view","create","edit"],"fields":{"materials":"edit","quantity":"edit"}}'::jsonb,true) WHERE id IN('manager','finance');
UPDATE roles SET body=jsonb_set(jsonb_set(body,'{permissions,purchase_order}','{"actions":["view"],"fields":{"amount":"hidden","description":"view","materials":"view"}}'::jsonb,true),'{permissions,inventory}','{"actions":["view"],"fields":{"materials":"view","quantity":"view"}}'::jsonb,true) WHERE id='engineer';
INSERT INTO form_versions VALUES('purchase_order',1,'{"id":"purchase_order","name":"Purchase Order","version":1,"fields":[],"createdBy":"u1","createdAt":"2026-10-05T08:00:00Z"}'),('inventory',1,'{"id":"inventory","name":"Inventory","version":1,"fields":[],"createdBy":"u1","createdAt":"2026-10-05T08:00:00Z"}');
INSERT INTO purchase_orders(id,tenant_id,number,total,vendor_id,form_version,created_by,body) SELECT 'po1','t1','PO-SKY-001',16500000,'v1',1,'u1',jsonb_build_object('id','po1','tenantId','t1','number','PO-SKY-001','description','Tower A slab materials and equipment hire','vendorId','v1','vendor','Sri Sai Constructions','vendorSnapshot',v.body,'lines','[{"id":"pl1","kind":"purchase","material":"TMT steel · Fe500","unit":"tonne","quantityMilli":2000,"rate":6500000,"total":13000000},{"id":"pl2","kind":"rental","material":"Concrete mixer hire","unit":"day","quantityMilli":10000,"rate":350000,"total":3500000}]'::jsonb,'total',16500000,'formVersion',1,'data','{}'::jsonb,'createdBy','u1','createdAt','2026-10-05T08:00:00Z') FROM vendors v WHERE v.id='v1';
