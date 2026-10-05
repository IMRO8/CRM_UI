-- Existing purchase-order dates are read from their original creation date when
-- no explicit date was captured; historical financial rows are not rewritten.
ALTER TABLE purchase_orders ADD COLUMN order_date date;
CREATE FUNCTION purchase_date_guard() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF NEW.order_date IS NULL THEN NEW.order_date=coalesce(nullif(NEW.body->>'orderDate','')::date,substring(NEW.body->>'createdAt',1,10)::date,current_date); END IF;
 NEW.body=jsonb_set(NEW.body,'{orderDate}',to_jsonb(NEW.order_date::text),true);RETURN NEW;
END $$;
CREATE TRIGGER po_date BEFORE INSERT OR UPDATE ON purchase_orders FOR EACH ROW EXECUTE FUNCTION purchase_date_guard();
CREATE TABLE work_order_purchase_reference(order_id text PRIMARY KEY,tenant_id text NOT NULL,purchase_order_id text NOT NULL,FOREIGN KEY(order_id,tenant_id) REFERENCES work_orders(id,tenant_id),FOREIGN KEY(purchase_order_id,tenant_id) REFERENCES purchase_orders(id,tenant_id));
CREATE FUNCTION approved_po_reference() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF NOT purchase_approved(NEW.purchase_order_id) THEN RAISE EXCEPTION 'Select an approved purchase order in this project'; END IF;RETURN NEW;END $$;
CREATE TRIGGER reference_approved BEFORE INSERT ON work_order_purchase_reference FOR EACH ROW EXECUTE FUNCTION approved_po_reference();
CREATE TRIGGER reference_retained BEFORE UPDATE OR DELETE ON work_order_purchase_reference FOR EACH ROW EXECUTE FUNCTION freeze_record();
ALTER TABLE work_order_purchase_reference ENABLE ROW LEVEL SECURITY;
CREATE POLICY reference_select ON work_order_purchase_reference FOR SELECT USING(has_tenant(tenant_id));
CREATE POLICY reference_insert ON work_order_purchase_reference FOR INSERT WITH CHECK(has_tenant(tenant_id));
CREATE FUNCTION work_item_guard() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE item jsonb; n numeric=0;
BEGIN
 IF NEW.body ? 'items' THEN
  IF jsonb_typeof(NEW.body->'items') IS DISTINCT FROM 'array' OR jsonb_array_length(NEW.body->'items') NOT BETWEEN 1 AND 100 THEN RAISE EXCEPTION 'Add work-order items'; END IF;
  FOR item IN SELECT * FROM jsonb_array_elements(NEW.body->'items') LOOP
   IF coalesce(item->>'description','')='' OR coalesce(item->>'unit','')='' OR NOT coalesce(item->>'quantityMilli','') ~ '^\d+$' OR NOT coalesce(item->>'rate','') ~ '^\d+$' OR (item->>'quantityMilli')::numeric NOT BETWEEN 1 AND 1000000000 OR (item->>'rate')::numeric NOT BETWEEN 0 AND 100000000000 OR NOT coalesce(item->>'startDate','') ~ '^\d{4}-\d{2}-\d{2}$' OR NOT coalesce(item->>'endDate','') ~ '^\d{4}-\d{2}-\d{2}$' THEN RAISE EXCEPTION 'Invalid work-order item'; END IF;
   IF (item->>'endDate')::date<(item->>'startDate')::date THEN RAISE EXCEPTION 'Period end must be on or after the start'; END IF;
   n=n+round((item->>'quantityMilli')::numeric*(item->>'rate')::numeric/1000);
   IF (item->>'amount')::numeric IS DISTINCT FROM round((item->>'quantityMilli')::numeric*(item->>'rate')::numeric/1000) THEN RAISE EXCEPTION 'Item amount must equal quantity times rate'; END IF;
  END LOOP;
  IF n<>NEW.base THEN RAISE EXCEPTION 'Work-order total must equal item amounts'; END IF;
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER work_items_validate BEFORE INSERT ON work_orders FOR EACH ROW EXECUTE FUNCTION work_item_guard();
UPDATE roles SET body=jsonb_set(body,'{permissions,work_order,fields}',coalesce(body#>'{permissions,work_order,fields}','{}'::jsonb)||CASE WHEN id='engineer' THEN '{"quantity":"view","unit":"view","period":"view","purchase_reference":"view"}'::jsonb ELSE '{"quantity":"edit","unit":"edit","period":"edit","purchase_reference":"edit"}'::jsonb END,true) WHERE id IN('engineer','manager','finance');
UPDATE roles SET body=jsonb_set(body,'{permissions,purchase_order,fields}',coalesce(body#>'{permissions,purchase_order,fields}','{}'::jsonb)||jsonb_build_object('order_date',CASE WHEN id='engineer' THEN 'view' ELSE 'edit' END),true) WHERE id IN('engineer','manager','finance');
CREATE FUNCTION complete_work_reference() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE p purchase_orders%ROWTYPE; captured_date text;
BEGIN
 IF coalesce(NEW.body->>'purchaseOrderId','')<>'' THEN
  IF NOT EXISTS(SELECT 1 FROM work_order_purchase_reference r WHERE r.order_id=NEW.id AND r.tenant_id=NEW.tenant_id AND r.purchase_order_id=NEW.body->>'purchaseOrderId') THEN RAISE EXCEPTION 'Work-order purchase reference is missing'; END IF;
  SELECT * INTO p FROM purchase_orders WHERE id=NEW.body->>'purchaseOrderId';
  captured_date=coalesce(p.order_date::text,substring(p.body->>'createdAt',1,10));
  IF NEW.body->>'purchaseOrderNumber' IS DISTINCT FROM p.number OR NEW.body->>'purchaseOrderDate' IS DISTINCT FROM captured_date THEN RAISE EXCEPTION 'Purchase-order number and date must match the referenced order'; END IF;
 END IF;
 RETURN NULL;
END $$;
CREATE CONSTRAINT TRIGGER work_reference_complete AFTER INSERT ON work_orders DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION complete_work_reference();
