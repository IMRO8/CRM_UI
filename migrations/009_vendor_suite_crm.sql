INSERT INTO form_versions(id,version,body) SELECT id,1,jsonb_build_object('id',id,'version',1,'name',name,'fields','[]'::jsonb) FROM (VALUES('project','Project'),('quotation_request','Quotation Work Package'),('vendor_quotation','Vendor Quotation'),('quotation_selection','Quotation Selection'),('vendor_feedback','Vendor Feedback'),('project_plan','Project Budget & Targets'),('progress','Work Progress'),('sale','Unit Sales'),('collection','Cash Collections'),('dashboard','CRM Dashboard'),('vendor_reporting','Vendor Reporting')) f(id,name);
DO $$ DECLARE r roles%ROWTYPE; f text; keys text[]; editable boolean; permissions jsonb; fields jsonb; k text; BEGIN
 FOR r IN SELECT * FROM roles LOOP
 permissions=r.body->'permissions';
 FOR f,keys IN SELECT * FROM (VALUES('quotation_request',ARRAY['description','reference','weights']),('vendor_quotation',ARRAY['amount','description','vendor','terms','ratings']),('quotation_selection',ARRAY['reference','reason','amount']),('vendor_feedback',ARRAY['vendor','reference','ratings','comments','dates']),('project_plan',ARRAY['amount','targets','dates']),('progress',ARRAY['reference','progress','dates','comments']),('sale',ARRAY['amount','customer','unit','status','dates']),('collection',ARRAY['amount','reference','dates','remarks'])) x(form,keys) LOOP
 editable=r.body->>'stage'='admin' OR (r.body->>'stage'='manager' AND f IN('quotation_request','vendor_quotation','quotation_selection','vendor_feedback','progress')) OR (r.body->>'stage'='finance' AND f IN('quotation_request','vendor_quotation','quotation_selection','sale','collection'));
 fields='{}';FOREACH k IN ARRAY keys LOOP fields=fields||jsonb_build_object(k,CASE WHEN editable THEN 'edit' WHEN r.id='engineer' AND k='amount' THEN 'hidden' ELSE 'view' END);END LOOP;
 permissions=permissions||jsonb_build_object(f,jsonb_build_object('actions',CASE WHEN editable THEN '["view","create","edit","submit","review","approve","reject"]'::jsonb WHEN f IN('vendor_feedback','progress') THEN '["view"]'::jsonb ELSE '[]'::jsonb END,'fields',fields));
 END LOOP;
 permissions=permissions||jsonb_build_object('dashboard',jsonb_build_object('actions','["view"]'::jsonb,'fields',jsonb_build_object('progress','view','cash',CASE WHEN r.body->>'stage' IN('admin','finance') THEN 'view' ELSE 'hidden' END,'sales',CASE WHEN r.body->>'stage' IN('admin','finance') THEN 'view' ELSE 'hidden' END,'budget',CASE WHEN r.body->>'stage'='admin' THEN 'view' ELSE 'hidden' END)),'vendor_reporting',jsonb_build_object('actions','["view"]'::jsonb,'fields',jsonb_build_object('amount',CASE WHEN r.body->>'stage' IN('admin','finance') THEN 'view' ELSE 'hidden' END,'ratings','view','reference','view')));
 permissions=jsonb_set(jsonb_set(jsonb_set(jsonb_set(permissions,'{work_order,fields,quotation}',to_jsonb(CASE WHEN r.id='engineer' THEN 'view' ELSE 'edit' END)),'{work_order,fields,contract}',to_jsonb(CASE WHEN r.id='engineer' THEN 'hidden' ELSE 'edit' END)),'{purchase_order,fields,reference}',to_jsonb(CASE WHEN r.id='engineer' THEN 'view' ELSE 'edit' END)),'{purchase_order,fields,purpose}',to_jsonb(CASE WHEN r.id='engineer' THEN 'view' ELSE 'edit' END));
 UPDATE roles SET body=jsonb_set(body,'{permissions}',permissions) WHERE id=r.id;
 END LOOP;
END $$;
CREATE TABLE operational_records(id text PRIMARY KEY,tenant_id text NOT NULL REFERENCES tenants,kind text NOT NULL CHECK(kind IN('rfq','quotation','award','vendor_feedback','project_plan','progress','sale','collection')),form_id text NOT NULL,form_version int NOT NULL,created_by text NOT NULL REFERENCES users,ref_rfq text,ref_quote text,ref_order text,ref_vendor text,ref_sale text,body jsonb NOT NULL,UNIQUE(id,tenant_id),FOREIGN KEY(form_id,form_version) REFERENCES form_versions,FOREIGN KEY(ref_rfq,tenant_id) REFERENCES operational_records(id,tenant_id),FOREIGN KEY(ref_quote,tenant_id) REFERENCES operational_records(id,tenant_id),FOREIGN KEY(ref_sale,tenant_id) REFERENCES operational_records(id,tenant_id),FOREIGN KEY(ref_order,tenant_id) REFERENCES work_orders(id,tenant_id),FOREIGN KEY(ref_vendor,tenant_id) REFERENCES vendors(id,tenant_id));
CREATE INDEX operational_project_kind ON operational_records(tenant_id,kind);
CREATE UNIQUE INDEX quotation_number_unique ON operational_records(tenant_id,ref_vendor,lower(body->>'number')) WHERE kind='quotation';
CREATE UNIQUE INDEX sale_unit_unique ON operational_records(tenant_id,lower(body->>'unit')) WHERE kind='sale' AND body->>'status'<>'Cancelled';
CREATE UNIQUE INDEX receipt_ref_unique ON operational_records(tenant_id,lower(body->>'reference')) WHERE kind='collection';
CREATE TABLE operational_events(id text PRIMARY KEY,document_id text NOT NULL REFERENCES operational_records,action text NOT NULL CHECK(action IN('submit','review','approve','reject')),stage text NOT NULL DEFAULT '' CHECK(stage IN('','superuser','admin','finance')),actor_id text NOT NULL REFERENCES users,body jsonb NOT NULL,CHECK((action='approve')=(stage<>'')),UNIQUE(document_id,action,stage));
CREATE UNIQUE INDEX operational_distinct_approver ON operational_events(document_id,actor_id) WHERE action='approve';
CREATE FUNCTION selection_approved(did text) RETURNS boolean LANGUAGE sql STABLE AS $$ SELECT approval_complete(2,(d.body->>'creatorSuperuser')::boolean,coalesce((SELECT jsonb_agg(jsonb_build_object('action',action,'stage',stage,'actorId',actor_id)) FROM operational_events WHERE document_id=d.id),'[]')) FROM operational_records d WHERE d.id=did AND d.kind='award' $$;
ALTER TABLE work_orders ADD COLUMN quotation_id text REFERENCES operational_records,ADD COLUMN approval_policy int NOT NULL DEFAULT 0,ADD COLUMN creator_superuser boolean NOT NULL DEFAULT false;
CREATE UNIQUE INDEX one_work_order_per_quote ON work_orders(quotation_id) WHERE quotation_id IS NOT NULL;
CREATE TABLE work_order_events(id text PRIMARY KEY,order_id text NOT NULL REFERENCES work_orders,action text NOT NULL CHECK(action IN('submit','review','approve','reject')),stage text NOT NULL DEFAULT '' CHECK(stage IN('','superuser','admin','finance')),actor_id text NOT NULL REFERENCES users,body jsonb NOT NULL,CHECK((action='approve')=(stage<>'')),UNIQUE(order_id,action,stage));
CREATE UNIQUE INDEX work_order_distinct_approver ON work_order_events(order_id,actor_id) WHERE action='approve';
CREATE FUNCTION work_order_approved(wo text) RETURNS boolean LANGUAGE sql STABLE AS $$ SELECT w.approval_policy=0 OR approval_complete(2,w.creator_superuser,coalesce((SELECT jsonb_agg(jsonb_build_object('action',action,'stage',stage,'actorId',actor_id)) FROM work_order_events WHERE order_id=w.id),'[]')) FROM work_orders w WHERE w.id=wo $$;
CREATE OR REPLACE FUNCTION effective_total(wo text) RETURNS bigint LANGUAGE sql STABLE AS $$ SELECT CASE WHEN work_order_approved(w.id) THEN w.base+coalesce((SELECT sum(a.delta) FROM amendments a WHERE a.order_id=w.id AND amendment_approved(a.id)),0)::bigint ELSE 0 END FROM work_orders w WHERE w.id=wo $$;
CREATE TABLE work_order_boq(order_id text PRIMARY KEY,tenant_id text NOT NULL,name text NOT NULL,mime text NOT NULL CHECK(mime IN('application/pdf','image/png','image/jpeg')),signed boolean NOT NULL CHECK(signed),content bytea NOT NULL CHECK(octet_length(content) BETWEEN 1 AND 1048576),sha256 text NOT NULL,FOREIGN KEY(order_id,tenant_id) REFERENCES work_orders(id,tenant_id));
CREATE FUNCTION validate_record_metadata(t text,f text,ver int,data jsonb,old_data jsonb DEFAULT NULL) RETURNS void LANGUAGE plpgsql SECURITY DEFINER SET search_path=public,pg_temp AS $$ DECLARE template jsonb;field jsonb;k text;v text;n numeric;BEGIN
 SELECT body INTO template FROM form_versions WHERE id=f AND version=ver;
 IF template IS NULL OR jsonb_typeof(data)<>'object' THEN RAISE EXCEPTION 'Form template and custom data required';END IF;
 FOR k IN SELECT jsonb_object_keys(data) LOOP IF NOT EXISTS(SELECT 1 FROM jsonb_array_elements(template->'fields') x WHERE x->>'key'=k) THEN RAISE EXCEPTION 'Unknown custom field';END IF;END LOOP;
 FOR field IN SELECT * FROM jsonb_array_elements(template->'fields') LOOP k=field->>'key';v=trim(coalesce(data->>k,''));
  IF coalesce((field->>'required')::boolean,false) AND v='' THEN RAISE EXCEPTION 'Required custom field: %',k;END IF;
  IF v<>'' THEN
   IF NOT is_platform_admin() AND NOT (coalesce(old_data ? k,false) AND old_data->k=data->k) AND NOT EXISTS(SELECT 1 FROM membership_roles m JOIN roles r ON r.id=m.role_id WHERE m.tenant_id=t AND m.user_id=current_actor() AND r.body->'permissions'->f->'fields'->>k='edit') THEN RAISE EXCEPTION 'Custom field editing permission required';END IF;
   IF field->>'type'='number' THEN n=v::numeric;IF n::text IN('NaN','Infinity','-Infinity') THEN RAISE EXCEPTION 'Finite custom number required';END IF;END IF;
   IF field->>'type'='date' AND (v!~'^\d{4}-\d{2}-\d{2}$' OR to_char(v::date,'YYYY-MM-DD')<>v) THEN RAISE EXCEPTION 'Valid custom date required';END IF;
  END IF;
 END LOOP;
END $$;
CREATE FUNCTION suite_records_guard() RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER SET search_path=public,pg_temp AS $$
DECLARE parent operational_records%ROWTYPE; v vendors%ROWTYPE; f text; keys text[]; k text; amount bigint; received bigint; required text[]; target jsonb; seen text[]:=ARRAY[]::text[];
BEGIN
 IF TG_OP='DELETE' THEN RAISE EXCEPTION 'Operational history is retained'; END IF;
 IF TG_OP='UPDATE' AND (NEW.id<>OLD.id OR NEW.tenant_id<>OLD.tenant_id OR NEW.kind<>OLD.kind OR NEW.created_by<>OLD.created_by OR NEW.form_id<>OLD.form_id OR NEW.form_version<>OLD.form_version OR NEW.kind NOT IN('rfq','quotation','sale')) THEN RAISE EXCEPTION 'Identity and history are immutable'; END IF;
 IF NEW.created_by<>current_actor() AND (TG_OP='INSERT' OR NOT is_platform_admin()) THEN RAISE EXCEPTION 'Creator permission required'; END IF;
 IF NOT has_tenant(NEW.tenant_id) OR EXISTS(SELECT 1 FROM tenants WHERE id=NEW.tenant_id AND deleted) THEN RAISE EXCEPTION 'Active project required'; END IF;
 f=CASE NEW.kind WHEN 'rfq' THEN 'quotation_request' WHEN 'quotation' THEN 'vendor_quotation' WHEN 'award' THEN 'quotation_selection' ELSE NEW.kind END;
 IF NEW.form_id<>f OR NOT project_form_action(NEW.tenant_id,f,CASE WHEN TG_OP='INSERT' THEN 'create' ELSE 'edit' END) THEN RAISE EXCEPTION 'Form action permission required'; END IF;
 keys=CASE NEW.kind WHEN 'rfq' THEN ARRAY['description','reference','weights'] WHEN 'quotation' THEN ARRAY['amount','description','vendor','terms','ratings'] WHEN 'award' THEN ARRAY['reference','reason','amount'] WHEN 'vendor_feedback' THEN ARRAY['vendor','reference','ratings','comments','dates'] WHEN 'project_plan' THEN ARRAY['amount','targets','dates'] WHEN 'progress' THEN ARRAY['reference','progress','dates','comments'] WHEN 'sale' THEN ARRAY['amount','customer','unit','status','dates'] ELSE ARRAY['amount','reference','dates','remarks'] END;
 IF NOT is_platform_admin() THEN FOREACH k IN ARRAY keys LOOP IF NOT EXISTS(SELECT 1 FROM membership_roles m JOIN roles r ON r.id=m.role_id WHERE m.tenant_id=NEW.tenant_id AND m.user_id=current_actor() AND r.body->'permissions'->f->'fields'->>k='edit') THEN RAISE EXCEPTION 'Core field editing permission required'; END IF;END LOOP;END IF;
 required=CASE NEW.kind WHEN 'rfq' THEN ARRAY['title','description','contractType','weights'] WHEN 'quotation' THEN ARRAY['rfqId','vendorId','number','amount','deliveryDays','quality','experience','terms','description','quotationDate'] WHEN 'award' THEN ARRAY['quotationId','reason'] WHEN 'vendor_feedback' THEN ARRAY['vendorId','comments','quality','timeliness','safety','communication','date'] WHEN 'progress' THEN ARRAY['orderId','percent','date','comments'] WHEN 'project_plan' THEN ARRAY['budget','salesTarget','unitTarget','startDate','endDate','monthlyTargets'] WHEN 'sale' THEN ARRAY['unit','customer','status','amount','date'] ELSE ARRAY['saleId','amount','date','reference'] END;
 FOREACH k IN ARRAY required LOOP IF NOT NEW.body ? k OR NEW.body->k='null'::jsonb OR trim(coalesce(NEW.body->>k,''))='' THEN RAISE EXCEPTION 'Required field missing: %',k; END IF;END LOOP;
 PERFORM validate_record_metadata(NEW.tenant_id,f,NEW.form_version,coalesce(NEW.body->'data','{}'),CASE WHEN TG_OP='UPDATE' THEN OLD.body->'data' ELSE NULL END);
 FOREACH k IN ARRAY ARRAY['date','quotationDate','startDate','endDate'] LOOP IF NEW.body ? k AND ((NEW.body->>k)!~'^\d{4}-\d{2}-\d{2}$' OR to_char((NEW.body->>k)::date,'YYYY-MM-DD')<>NEW.body->>k) THEN RAISE EXCEPTION 'Invalid date';END IF;END LOOP;
 NEW.ref_rfq=nullif(NEW.body->>'rfqId','');NEW.ref_quote=nullif(NEW.body->>'quotationId','');NEW.ref_order=nullif(coalesce(NEW.body->>'orderId',NEW.body->>'referenceOrderId'),'');NEW.ref_vendor=nullif(NEW.body->>'vendorId','');NEW.ref_sale=nullif(NEW.body->>'saleId','');
 IF NEW.ref_order IS NOT NULL AND NOT project_form_action(NEW.tenant_id,'work_order','view') THEN RAISE EXCEPTION 'Work order view permission required'; END IF;
 IF NEW.ref_vendor IS NOT NULL THEN SELECT * INTO v FROM vendors WHERE id=NEW.ref_vendor;IF v.tenant_id<>NEW.tenant_id OR v.deleted OR coalesce(v.body->>'onboardingStatus','Active')<>'Active' THEN RAISE EXCEPTION 'Active project vendor required';END IF;END IF;
 IF NEW.kind='rfq' THEN
  IF TG_OP='UPDATE' AND EXISTS(SELECT 1 FROM operational_records WHERE ref_rfq=OLD.id) THEN RAISE EXCEPTION 'Package with quotations is retained'; END IF;
  IF coalesce(trim(NEW.body->>'title'),'')='' OR coalesce(trim(NEW.body->>'description'),'')='' OR NEW.body->>'contractType' NOT IN('Material construction','Labour') THEN RAISE EXCEPTION 'Work package scope and contract required'; END IF;
  IF (SELECT sum(value::int) FROM jsonb_each_text(NEW.body->'weights'))<>100 OR (SELECT count(*) FROM jsonb_object_keys(NEW.body->'weights'))<>4 OR NOT NEW.body->'weights' ?& ARRAY['price','delivery','quality','experience'] OR EXISTS(SELECT 1 FROM jsonb_each_text(NEW.body->'weights') WHERE value::int NOT BETWEEN 0 AND 100) THEN RAISE EXCEPTION 'Comparison weights must total 100'; END IF;
 ELSIF NEW.kind='quotation' THEN
  SELECT * INTO parent FROM operational_records WHERE id=NEW.ref_rfq FOR UPDATE;IF parent.kind IS DISTINCT FROM 'rfq' OR parent.tenant_id<>NEW.tenant_id THEN RAISE EXCEPTION 'Project work package required';END IF;
  IF EXISTS(SELECT 1 FROM operational_records WHERE kind='award' AND ref_rfq=parent.id AND NOT EXISTS(SELECT 1 FROM operational_events WHERE document_id=operational_records.id AND action='reject')) THEN RAISE EXCEPTION 'Package has an active selection'; END IF;
  IF TG_OP='UPDATE' AND EXISTS(SELECT 1 FROM operational_records WHERE ref_quote=OLD.id) THEN RAISE EXCEPTION 'Selected quotation is retained';END IF;
  IF NEW.ref_vendor IS NULL OR coalesce(trim(NEW.body->>'number'),'')='' OR (NEW.body->>'amount')::bigint NOT BETWEEN 1 AND 100000000000000 OR (NEW.body->>'deliveryDays')::int NOT BETWEEN 1 AND 3650 OR (NEW.body->>'quality')::int NOT BETWEEN 1 AND 5 OR (NEW.body->>'experience')::int NOT BETWEEN 1 AND 5 OR coalesce(trim(NEW.body->>'terms'),'')='' OR coalesce(trim(NEW.body->>'description'),'')='' THEN RAISE EXCEPTION 'Quotation fields are invalid'; END IF;
  PERFORM (NEW.body->>'quotationDate')::date;
 ELSIF NEW.kind='award' THEN
  SELECT * INTO parent FROM operational_records WHERE id=NEW.ref_quote;IF parent.kind IS DISTINCT FROM 'quotation' OR parent.tenant_id<>NEW.tenant_id THEN RAISE EXCEPTION 'Project quotation required'; END IF;
  NEW.ref_rfq=parent.ref_rfq;PERFORM 1 FROM operational_records WHERE id=NEW.ref_rfq FOR UPDATE;
  IF EXISTS(SELECT 1 FROM operational_records WHERE kind='award' AND ref_rfq=NEW.ref_rfq AND NOT EXISTS(SELECT 1 FROM operational_events WHERE document_id=operational_records.id AND action='reject')) THEN RAISE EXCEPTION 'Only one active selection per package'; END IF;
  IF coalesce(trim(NEW.body->>'reason'),'')='' THEN RAISE EXCEPTION 'Selection rationale required'; END IF;
  NEW.body=NEW.body||jsonb_build_object('rfqId',NEW.ref_rfq,'amount',(parent.body->>'amount')::bigint,'approvalPolicy',2,'creatorSuperuser',is_platform_admin());
 ELSIF NEW.kind='vendor_feedback' THEN
  IF NEW.ref_vendor IS NULL OR coalesce(trim(NEW.body->>'comments'),'')='' THEN RAISE EXCEPTION 'Vendor and feedback required'; END IF;
  FOREACH k IN ARRAY ARRAY['quality','timeliness','safety','communication'] LOOP IF (NEW.body->>k)::int NOT BETWEEN 1 AND 5 OR NEW.body->>k IS NULL THEN RAISE EXCEPTION 'Feedback ratings must be 1 to 5'; END IF;END LOOP;
  IF NEW.ref_order IS NOT NULL AND NOT EXISTS(SELECT 1 FROM work_order_vendors WHERE order_id=NEW.ref_order AND vendor_id=NEW.ref_vendor) THEN RAISE EXCEPTION 'Feedback work order must belong to vendor'; END IF;PERFORM (NEW.body->>'date')::date;
 ELSIF NEW.kind='progress' THEN
  IF NEW.ref_order IS NULL OR NOT work_order_approved(NEW.ref_order) OR coalesce((NEW.body->>'percent')::int,-1) NOT BETWEEN 0 AND 100 OR coalesce(trim(NEW.body->>'comments'),'')='' THEN RAISE EXCEPTION 'Approved work order, progress and note required';END IF;PERFORM (NEW.body->>'date')::date;
 ELSIF NEW.kind='project_plan' THEN
  IF coalesce((NEW.body->>'budget')::bigint,0) NOT BETWEEN 1 AND 100000000000000 OR coalesce((NEW.body->>'salesTarget')::bigint,-1) NOT BETWEEN 0 AND 100000000000000 OR coalesce((NEW.body->>'unitTarget')::int,-1) NOT BETWEEN 0 AND 1000000 OR (NEW.body->>'endDate')::date<(NEW.body->>'startDate')::date THEN RAISE EXCEPTION 'Invalid budget and targets'; END IF;
  IF jsonb_typeof(NEW.body->'monthlyTargets')<>'array' THEN RAISE EXCEPTION 'Monthly targets must be a list'; END IF;
  FOR target IN SELECT * FROM jsonb_array_elements(NEW.body->'monthlyTargets') LOOP
   IF coalesce(target->>'month','')!~'^\d{4}-(0[1-9]|1[0-2])$' OR target->>'month'=ANY(seen) OR coalesce((target->>'cash')::bigint,-1) NOT BETWEEN 0 AND 100000000000000 OR coalesce((target->>'progress')::int,-1) NOT BETWEEN 0 AND 100 THEN RAISE EXCEPTION 'Invalid monthly target';END IF;
   seen=array_append(seen,target->>'month');
  END LOOP;
 ELSIF NEW.kind='sale' THEN
  IF coalesce(trim(NEW.body->>'unit'),'')='' OR coalesce(trim(NEW.body->>'customer'),'')='' OR NEW.body->>'status' NOT IN('Lead','Reserved','Booked','Cancelled') OR coalesce((NEW.body->>'amount')::bigint,0) NOT BETWEEN 1 AND 100000000000000 THEN RAISE EXCEPTION 'Invalid sale fields';END IF;
  SELECT coalesce(sum((body->>'amount')::bigint),0) INTO received FROM operational_records WHERE kind='collection' AND ref_sale=NEW.id;
  IF received>0 AND (NEW.body->>'status'<>'Booked' OR (NEW.body->>'amount')::bigint<received) THEN RAISE EXCEPTION 'Collected sales cannot be cancelled or reduced below receipts'; END IF;PERFORM (NEW.body->>'date')::date;
 ELSIF NEW.kind='collection' THEN
  SELECT * INTO parent FROM operational_records WHERE id=NEW.ref_sale FOR UPDATE;IF parent.kind IS DISTINCT FROM 'sale' OR parent.tenant_id<>NEW.tenant_id OR parent.body->>'status'<>'Booked' THEN RAISE EXCEPTION 'Booked project sale required';END IF;
  amount=(NEW.body->>'amount')::bigint;SELECT coalesce(sum((body->>'amount')::bigint),0) INTO received FROM operational_records WHERE kind='collection' AND ref_sale=parent.id;
  IF amount IS NULL OR amount NOT BETWEEN 1 AND 100000000000000 OR received+amount>(parent.body->>'amount')::bigint OR (NEW.body->>'date')::date<(parent.body->>'date')::date OR coalesce(trim(NEW.body->>'reference'),'')='' THEN RAISE EXCEPTION 'Invalid receipt amount, date or reference'; END IF;
 END IF;
 NEW.body=NEW.body||jsonb_build_object('id',NEW.id,'tenantId',NEW.tenant_id,'kind',NEW.kind,'createdBy',NEW.created_by,'formVersion',NEW.form_version);
 RETURN NEW;
END $$;
CREATE TRIGGER operational_validation BEFORE INSERT OR UPDATE OR DELETE ON operational_records FOR EACH ROW EXECUTE FUNCTION suite_records_guard();
CREATE FUNCTION operational_workflow() RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER SET search_path=public,pg_temp AS $$ DECLARE d operational_records%ROWTYPE;ev jsonb;BEGIN
 SELECT * INTO d FROM operational_records WHERE id=NEW.document_id FOR UPDATE;IF d.kind IS DISTINCT FROM 'award' THEN RAISE EXCEPTION 'Selection required';END IF;
 SELECT coalesce(jsonb_agg(jsonb_build_object('action',action,'stage',stage,'actorId',actor_id)),'[]') INTO ev FROM operational_events WHERE document_id=d.id;
 PERFORM validate_approval_step(2,(d.body->>'creatorSuperuser')::boolean,d.created_by,d.tenant_id,'quotation_selection',ev,NEW.action,NEW.stage,NEW.actor_id);
 NEW.body=NEW.body||jsonb_build_object('id',NEW.id,'documentId',NEW.document_id,'action',NEW.action,'stage',NEW.stage,'actorId',NEW.actor_id,'at',clock_timestamp());RETURN NEW;
END $$;
CREATE TRIGGER operational_workflow_guard BEFORE INSERT ON operational_events FOR EACH ROW EXECUTE FUNCTION operational_workflow();
CREATE TRIGGER operational_event_history BEFORE UPDATE OR DELETE ON operational_events FOR EACH ROW EXECUTE FUNCTION freeze_record();
CREATE FUNCTION work_order_contract_guard() RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER SET search_path=public,pg_temp AS $$ DECLARE q operational_records%ROWTYPE;r operational_records%ROWTYPE;k text;BEGIN
 NEW.quotation_id=nullif(NEW.body->>'quotationId','');SELECT * INTO q FROM operational_records WHERE id=NEW.quotation_id FOR UPDATE;
 IF q.kind IS DISTINCT FROM 'quotation' OR q.tenant_id<>NEW.tenant_id OR NOT EXISTS(SELECT 1 FROM operational_records a WHERE a.ref_quote=q.id AND selection_approved(a.id)) THEN RAISE EXCEPTION 'Approved selected quotation ID is mandatory';END IF;
 SELECT * INTO r FROM operational_records WHERE id=q.ref_rfq;
 IF NEW.body->>'vendorId' IS DISTINCT FROM q.ref_vendor OR NEW.base>(q.body->>'amount')::bigint OR NEW.created_by<>current_actor() THEN RAISE EXCEPTION 'Work order must use selected vendor and quoted value';END IF;
 IF NOT project_form_action(NEW.tenant_id,'work_order','create') OR NOT project_form_action(NEW.tenant_id,'vendor_quotation','view') THEN RAISE EXCEPTION 'Work order and quotation permissions required';END IF;
 IF NOT is_platform_admin() THEN FOREACH k IN ARRAY ARRAY['quotation','contract','amount','description'] LOOP IF NOT EXISTS(SELECT 1 FROM membership_roles m JOIN roles r ON r.id=m.role_id WHERE m.tenant_id=NEW.tenant_id AND m.user_id=current_actor() AND r.body->'permissions'->'work_order'->'fields'->>k='edit') THEN RAISE EXCEPTION 'Work order field editing required';END IF;END LOOP;END IF;
 PERFORM validate_record_metadata(NEW.tenant_id,NEW.form_id,NEW.form_version,coalesce(NEW.body->'data','{}'));
 NEW.body=NEW.body||jsonb_build_object('id',NEW.id,'tenantId',NEW.tenant_id,'number',NEW.number,'base',NEW.base,'formId',NEW.form_id,'formVersion',NEW.form_version,'createdBy',NEW.created_by);
 NEW.approval_policy=2;NEW.creator_superuser=is_platform_admin();NEW.body=NEW.body||jsonb_build_object('quotationId',q.id,'quotationNumber',q.body->>'number','rfqId',r.id,'contractType',r.body->>'contractType','workflowEnabled',true,'approvalPolicy',2,'creatorSuperuser',NEW.creator_superuser);
 RETURN NEW;
END $$;
CREATE TRIGGER new_work_order_contract BEFORE INSERT ON work_orders FOR EACH ROW EXECUTE FUNCTION work_order_contract_guard();
CREATE FUNCTION boq_guard() RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER SET search_path=public,pg_temp AS $$ BEGIN
 IF NOT project_form_action(NEW.tenant_id,'work_order','create') OR NOT EXISTS(SELECT 1 FROM work_orders WHERE id=NEW.order_id AND tenant_id=NEW.tenant_id AND body->>'contractType'='Material construction' AND created_by=current_actor()) THEN RAISE EXCEPTION 'BOQ order permission required';END IF;
 IF (NEW.mime='application/pdf' AND substring(NEW.content FROM 1 FOR 5)<>decode('255044462d','hex')) OR (NEW.mime='image/png' AND substring(NEW.content FROM 1 FOR 8)<>decode('89504e470d0a1a0a','hex')) OR (NEW.mime='image/jpeg' AND substring(NEW.content FROM 1 FOR 3)<>decode('ffd8ff','hex')) THEN RAISE EXCEPTION 'Invalid BOQ file content';END IF;
 NEW.sha256=encode(digest(NEW.content,'sha256'),'hex');RETURN NEW;
END $$;
CREATE TRIGGER boq_validation BEFORE INSERT ON work_order_boq FOR EACH ROW EXECUTE FUNCTION boq_guard();
CREATE TRIGGER boq_retained BEFORE UPDATE OR DELETE ON work_order_boq FOR EACH ROW EXECUTE FUNCTION freeze_record();
CREATE FUNCTION require_material_boq() RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER SET search_path=public,pg_temp AS $$ BEGIN IF NEW.body->>'contractType'='Material construction' AND NOT EXISTS(SELECT 1 FROM work_order_boq WHERE order_id=NEW.id AND signed) THEN RAISE EXCEPTION 'Signed BOQ attachment required';END IF;RETURN NULL;END $$;
CREATE CONSTRAINT TRIGGER material_boq_required AFTER INSERT ON work_orders DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION require_material_boq();
CREATE FUNCTION work_order_workflow() RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER SET search_path=public,pg_temp AS $$ DECLARE w work_orders%ROWTYPE;ev jsonb;BEGIN
 SELECT * INTO w FROM work_orders WHERE id=NEW.order_id FOR UPDATE;IF w.approval_policy<>2 THEN RAISE EXCEPTION 'Legacy work orders retain original status'; END IF;
 SELECT coalesce(jsonb_agg(jsonb_build_object('action',action,'stage',stage,'actorId',actor_id)),'[]') INTO ev FROM work_order_events WHERE order_id=w.id;
 PERFORM validate_approval_step(2,w.creator_superuser,w.created_by,w.tenant_id,'work_order',ev,NEW.action,NEW.stage,NEW.actor_id);
 IF NEW.action='approve' AND w.body->>'contractType'='Labour' AND NOT EXISTS(SELECT 1 FROM purchase_orders WHERE body->>'orderId'=w.id AND purchase_approved(id)) THEN RAISE EXCEPTION 'Approve a linked labour purchase order first'; END IF;
 NEW.body=NEW.body||jsonb_build_object('id',NEW.id,'orderId',w.id,'action',NEW.action,'stage',NEW.stage,'actorId',NEW.actor_id,'at',clock_timestamp());RETURN NEW;
END $$;
CREATE TRIGGER work_order_workflow_guard BEFORE INSERT ON work_order_events FOR EACH ROW EXECUTE FUNCTION work_order_workflow();
CREATE TRIGGER work_order_events_retained BEFORE UPDATE OR DELETE ON work_order_events FOR EACH ROW EXECUTE FUNCTION freeze_record();
ALTER TABLE purchase_orders ADD COLUMN order_id text;
ALTER TABLE purchase_orders ADD FOREIGN KEY(order_id,tenant_id) REFERENCES work_orders(id,tenant_id);
CREATE FUNCTION purchase_purpose_guard() RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER SET search_path=public,pg_temp AS $$ DECLARE w work_orders%ROWTYPE;l jsonb;BEGIN
 NEW.order_id=nullif(NEW.body->>'orderId','');SELECT * INTO w FROM work_orders WHERE id=NEW.order_id;
 IF w.id IS NULL OR w.tenant_id<>NEW.tenant_id OR coalesce(trim(NEW.body->>'purpose'),'')='' THEN RAISE EXCEPTION 'Project work order and purchase purpose are mandatory'; END IF;
 IF w.body->>'contractType' IS DISTINCT FROM 'Labour' AND NOT work_order_approved(w.id) THEN RAISE EXCEPTION 'Material PO needs an approved work order'; END IF;
 IF EXISTS(SELECT 1 FROM work_order_events WHERE order_id=w.id AND action='reject') THEN RAISE EXCEPTION 'Rejected work order cannot receive purchases';END IF;
 FOR l IN SELECT * FROM jsonb_array_elements(NEW.body->'lines') LOOP IF coalesce(trim(l->>'purpose'),'')='' THEN RAISE EXCEPTION 'Explain why each material or rental is needed';END IF;END LOOP;
 NEW.body=NEW.body||jsonb_build_object('orderId',w.id,'orderNumber',w.number);RETURN NEW;
END $$;
CREATE TRIGGER purchase_purpose BEFORE INSERT OR UPDATE ON purchase_orders FOR EACH ROW EXECUTE FUNCTION purchase_purpose_guard();
CREATE FUNCTION approved_work_amendment() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NOT work_order_approved(NEW.order_id) THEN RAISE EXCEPTION 'Only approved work orders can be amended';END IF;RETURN NEW;END $$;
CREATE TRIGGER approved_order_for_amendment BEFORE INSERT ON amendments FOR EACH ROW EXECUTE FUNCTION approved_work_amendment();
CREATE FUNCTION vendor_tax_required() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF NEW.body->>'onboardingStatus' IN('Draft','Submitted','Active') AND coalesce(NEW.body->>'createdBy','')<>'' AND coalesce(trim(NEW.body->>'pan'),'')='' AND coalesce(trim(NEW.body->>'gstin'),'')='' THEN RAISE EXCEPTION 'PAN or GSTIN is required for onboarding';END IF;RETURN NEW;
END $$;
CREATE TRIGGER onboarding_tax BEFORE INSERT OR UPDATE ON vendors FOR EACH ROW EXECUTE FUNCTION vendor_tax_required();
DO $$ DECLARE tbl text;BEGIN FOREACH tbl IN ARRAY ARRAY['operational_records','work_order_boq'] LOOP
 EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY',tbl);EXECUTE format('CREATE POLICY suite_select ON %I FOR SELECT USING(has_tenant(tenant_id))',tbl);EXECUTE format('CREATE POLICY suite_insert ON %I FOR INSERT WITH CHECK(has_tenant(tenant_id))',tbl);END LOOP;END $$;
CREATE POLICY suite_update ON operational_records FOR UPDATE USING(has_tenant(tenant_id)) WITH CHECK(has_tenant(tenant_id));
ALTER TABLE operational_events ENABLE ROW LEVEL SECURITY;CREATE POLICY suite_event_select ON operational_events FOR SELECT USING(EXISTS(SELECT 1 FROM operational_records WHERE id=document_id));CREATE POLICY suite_event_insert ON operational_events FOR INSERT WITH CHECK(actor_id=current_actor() AND EXISTS(SELECT 1 FROM operational_records WHERE id=document_id));
ALTER TABLE work_order_events ENABLE ROW LEVEL SECURITY;CREATE POLICY order_event_select ON work_order_events FOR SELECT USING(EXISTS(SELECT 1 FROM work_orders WHERE id=order_id));CREATE POLICY order_event_insert ON work_order_events FOR INSERT WITH CHECK(actor_id=current_actor() AND EXISTS(SELECT 1 FROM work_orders WHERE id=order_id));

CREATE FUNCTION approved_expense_reference() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.ref_work_order IS NOT NULL AND NOT work_order_approved(NEW.ref_work_order) THEN RAISE EXCEPTION 'Referenced work order must be approved';END IF;RETURN NEW;END $$;
CREATE TRIGGER approved_expense_work BEFORE INSERT OR UPDATE ON finance_documents FOR EACH ROW EXECUTE FUNCTION approved_expense_reference();
