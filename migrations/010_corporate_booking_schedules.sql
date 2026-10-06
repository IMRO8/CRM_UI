-- Manual quotation decisions; no scoring or ranking. Existing migrations remain immutable.
ALTER TABLE operational_records DISABLE TRIGGER operational_validation;
UPDATE operational_records SET body=(body - 'quality' - 'experience' - 'score') || jsonb_build_object('creditDays',30) WHERE kind='quotation';
UPDATE operational_records SET body=body-'weights' WHERE kind='rfq';
ALTER TABLE operational_records ENABLE TRIGGER operational_validation;
CREATE OR REPLACE FUNCTION suite_records_guard() RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER SET search_path=public,pg_temp AS $$
DECLARE parent operational_records%ROWTYPE; v vendors%ROWTYPE; f text; keys text[]; k text; amount bigint; received bigint; required text[]; target jsonb; seen text[]:=ARRAY[]::text[];
BEGIN
 IF TG_OP='DELETE' THEN RAISE EXCEPTION 'Operational history is retained'; END IF;
 IF TG_OP='UPDATE' AND (NEW.id<>OLD.id OR NEW.tenant_id<>OLD.tenant_id OR NEW.kind<>OLD.kind OR NEW.created_by<>OLD.created_by OR NEW.form_id<>OLD.form_id OR NEW.form_version<>OLD.form_version OR NEW.kind NOT IN('rfq','quotation','sale')) THEN RAISE EXCEPTION 'Identity and history are immutable'; END IF;
 IF NEW.created_by<>current_actor() AND (TG_OP='INSERT' OR NOT is_platform_admin()) THEN RAISE EXCEPTION 'Creator permission required'; END IF;
 IF NOT has_tenant(NEW.tenant_id) OR EXISTS(SELECT 1 FROM tenants WHERE id=NEW.tenant_id AND deleted) THEN RAISE EXCEPTION 'Active project required'; END IF;
 f=CASE NEW.kind WHEN 'rfq' THEN 'quotation_request' WHEN 'quotation' THEN 'vendor_quotation' WHEN 'award' THEN 'quotation_selection' ELSE NEW.kind END;
 IF NEW.form_id<>f OR NOT project_form_action(NEW.tenant_id,f,CASE WHEN TG_OP='INSERT' THEN 'create' ELSE 'edit' END) THEN RAISE EXCEPTION 'Form action permission required'; END IF;
 keys=CASE NEW.kind WHEN 'rfq' THEN ARRAY['description','reference'] WHEN 'quotation' THEN ARRAY['amount','description','vendor','terms'] WHEN 'award' THEN ARRAY['reference','reason','amount'] WHEN 'vendor_feedback' THEN ARRAY['vendor','reference','ratings','comments','dates'] WHEN 'project_plan' THEN ARRAY['amount','targets','dates'] WHEN 'progress' THEN ARRAY['reference','progress','dates','comments'] WHEN 'sale' THEN ARRAY['amount','customer','unit','status','dates'] ELSE ARRAY['amount','reference','dates','remarks'] END;
 IF NOT is_platform_admin() THEN FOREACH k IN ARRAY keys LOOP IF NOT EXISTS(SELECT 1 FROM membership_roles m JOIN roles r ON r.id=m.role_id WHERE m.tenant_id=NEW.tenant_id AND m.user_id=current_actor() AND r.body->'permissions'->f->'fields'->>k='edit') THEN RAISE EXCEPTION 'Core field editing permission required'; END IF;END LOOP;END IF;
 required=CASE NEW.kind WHEN 'rfq' THEN ARRAY['title','description','contractType'] WHEN 'quotation' THEN ARRAY['rfqId','vendorId','number','amount','deliveryDays','creditDays','terms','description','quotationDate'] WHEN 'award' THEN ARRAY['quotationId','reason'] WHEN 'vendor_feedback' THEN ARRAY['vendorId','comments','quality','timeliness','safety','communication','date'] WHEN 'progress' THEN ARRAY['orderId','percent','date','comments'] WHEN 'project_plan' THEN ARRAY['budget','salesTarget','unitTarget','startDate','endDate','monthlyTargets'] WHEN 'sale' THEN ARRAY['unit','customer','status','amount','date'] ELSE ARRAY['saleId','amount','date','reference'] END;
 FOREACH k IN ARRAY required LOOP IF NOT NEW.body ? k OR NEW.body->k='null'::jsonb OR trim(coalesce(NEW.body->>k,''))='' THEN RAISE EXCEPTION 'Required field missing: %',k; END IF;END LOOP;
 PERFORM validate_record_metadata(NEW.tenant_id,f,NEW.form_version,coalesce(NEW.body->'data','{}'),CASE WHEN TG_OP='UPDATE' THEN OLD.body->'data' ELSE NULL END);
 FOREACH k IN ARRAY ARRAY['date','quotationDate','startDate','endDate'] LOOP IF NEW.body ? k AND ((NEW.body->>k)!~'^\d{4}-\d{2}-\d{2}$' OR to_char((NEW.body->>k)::date,'YYYY-MM-DD')<>NEW.body->>k) THEN RAISE EXCEPTION 'Invalid date';END IF;END LOOP;
 NEW.ref_rfq=nullif(NEW.body->>'rfqId','');NEW.ref_quote=nullif(NEW.body->>'quotationId','');NEW.ref_order=nullif(coalesce(NEW.body->>'orderId',NEW.body->>'referenceOrderId'),'');NEW.ref_vendor=nullif(NEW.body->>'vendorId','');NEW.ref_sale=nullif(NEW.body->>'saleId','');
 IF NEW.ref_order IS NOT NULL AND NOT project_form_action(NEW.tenant_id,'work_order','view') THEN RAISE EXCEPTION 'Work order view permission required'; END IF;
 IF NEW.ref_vendor IS NOT NULL THEN SELECT * INTO v FROM vendors WHERE id=NEW.ref_vendor;IF v.tenant_id<>NEW.tenant_id OR v.deleted OR coalesce(v.body->>'onboardingStatus','Active')<>'Active' THEN RAISE EXCEPTION 'Active project vendor required';END IF;END IF;
 IF NEW.kind='rfq' THEN
  IF TG_OP='UPDATE' AND EXISTS(SELECT 1 FROM operational_records WHERE ref_rfq=OLD.id) THEN RAISE EXCEPTION 'Package with quotations is retained'; END IF;
  IF coalesce(trim(NEW.body->>'title'),'')='' OR coalesce(trim(NEW.body->>'description'),'')='' OR NEW.body->>'contractType' NOT IN('Material construction','Labour') THEN RAISE EXCEPTION 'Work package scope and contract required'; END IF;
 ELSIF NEW.kind='quotation' THEN
  SELECT * INTO parent FROM operational_records WHERE id=NEW.ref_rfq FOR UPDATE;IF parent.kind IS DISTINCT FROM 'rfq' OR parent.tenant_id<>NEW.tenant_id THEN RAISE EXCEPTION 'Project work package required';END IF;
  IF EXISTS(SELECT 1 FROM operational_records WHERE kind='award' AND ref_rfq=parent.id AND NOT EXISTS(SELECT 1 FROM operational_events WHERE document_id=operational_records.id AND action='reject')) THEN RAISE EXCEPTION 'Package has an active selection'; END IF;
  IF TG_OP='UPDATE' AND EXISTS(SELECT 1 FROM operational_records WHERE ref_quote=OLD.id) THEN RAISE EXCEPTION 'Selected quotation is retained';END IF;
  IF NEW.ref_vendor IS NULL OR coalesce(trim(NEW.body->>'number'),'')='' OR (NEW.body->>'amount')::bigint NOT BETWEEN 1 AND 100000000000000 OR (NEW.body->>'deliveryDays')::int NOT BETWEEN 1 AND 3650 OR (NEW.body->>'creditDays')::int NOT BETWEEN 0 AND 3650 OR coalesce(trim(NEW.body->>'terms'),'')='' OR coalesce(trim(NEW.body->>'description'),'')='' THEN RAISE EXCEPTION 'Quotation fields are invalid'; END IF;
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
  SELECT coalesce(sum((body->>'amount')::bigint+coalesce((body->>'tds')::bigint,0)),0) INTO received FROM operational_records WHERE kind='collection' AND ref_sale=NEW.id;
  IF received>0 AND (NEW.body->>'status'<>'Booked' OR (NEW.body->>'amount')::bigint<received) THEN RAISE EXCEPTION 'Collected sales cannot be cancelled or reduced below receipts'; END IF;PERFORM (NEW.body->>'date')::date;
 ELSIF NEW.kind='collection' THEN
  SELECT * INTO parent FROM operational_records WHERE id=NEW.ref_sale FOR UPDATE;IF parent.kind IS DISTINCT FROM 'sale' OR parent.tenant_id<>NEW.tenant_id OR parent.body->>'status'<>'Booked' THEN RAISE EXCEPTION 'Booked project sale required';END IF;
  amount=(NEW.body->>'amount')::bigint+coalesce((NEW.body->>'tds')::bigint,0);IF coalesce((NEW.body->>'tds')::bigint,0)<0 THEN RAISE EXCEPTION 'Invalid TDS';END IF;SELECT coalesce(sum((body->>'amount')::bigint+coalesce((body->>'tds')::bigint,0)),0) INTO received FROM operational_records WHERE kind='collection' AND ref_sale=parent.id;
  IF amount IS NULL OR amount NOT BETWEEN 1 AND 100000000000000 OR received+amount>(parent.body->>'amount')::bigint OR (NEW.body->>'date')::date<(parent.body->>'date')::date OR coalesce(trim(NEW.body->>'reference'),'')='' THEN RAISE EXCEPTION 'Invalid receipt amount, date or reference'; END IF;
 END IF;
 NEW.body=NEW.body||jsonb_build_object('id',NEW.id,'tenantId',NEW.tenant_id,'kind',NEW.kind,'createdBy',NEW.created_by,'formVersion',NEW.form_version);
 RETURN NEW;
END $$;
CREATE TABLE erp_extensions(id text PRIMARY KEY,tenant_id text NOT NULL REFERENCES tenants(id),kind text NOT NULL CHECK(kind IN('corporate_account','corporate_payment','booking','schedule_template','payment_schedule','milestone','demand','receipt_allocation')),body jsonb NOT NULL,UNIQUE(id,tenant_id));
CREATE TABLE erp_extension_events(id text PRIMARY KEY,document_id text NOT NULL REFERENCES erp_extensions(id),action text NOT NULL CHECK(action IN('submit','review','approve','reject')),stage text NOT NULL DEFAULT '',actor_id text NOT NULL REFERENCES users(id),body jsonb NOT NULL);
CREATE TABLE bank_account_numbers(number text PRIMARY KEY,account_id text NOT NULL UNIQUE,account_type text NOT NULL CHECK(account_type IN('rera','corporate')));
INSERT INTO bank_account_numbers SELECT number,id,'rera' FROM rera_accounts;
CREATE FUNCTION reserve_bank_account() RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER SET search_path=public,pg_temp AS $$ DECLARE n text;BEGIN
 IF TG_TABLE_NAME='erp_extensions' AND to_jsonb(NEW)->>'kind'<>'corporate_account' THEN RETURN NEW;END IF;
 IF NOT is_platform_admin() THEN RAISE EXCEPTION 'Only Superuser registers accounts';END IF;
 IF TG_TABLE_NAME='rera_accounts' THEN n=to_jsonb(NEW)->>'number';ELSE n=NEW.body->>'number';END IF;
 IF n!~'^\d{6,34}$' THEN RAISE EXCEPTION 'Account needs 6–34 digits';END IF;
 INSERT INTO bank_account_numbers VALUES(n,NEW.id,CASE WHEN TG_TABLE_NAME='rera_accounts' THEN 'rera' ELSE 'corporate' END);
 RETURN NEW;END $$;
CREATE TRIGGER reserve_rera BEFORE INSERT ON rera_accounts FOR EACH ROW EXECUTE FUNCTION reserve_bank_account();
CREATE TRIGGER reserve_corporate BEFORE INSERT ON erp_extensions FOR EACH ROW EXECUTE FUNCTION reserve_bank_account();
DO $$ DECLARE f text;keys text[];r record;perm jsonb;editable boolean;BEGIN
 FOR f,keys IN SELECT * FROM (VALUES('corporate_account',ARRAY['details']),('corporate_payment',ARRAY['amount','reference','details','dates']),('booking',ARRAY['amount','reference','details','dates']),('schedule_template',ARRAY['details']),('payment_schedule',ARRAY['amount','reference','details','dates']),('milestone',ARRAY['details','dates']),('demand',ARRAY['amount','reference','details','dates']),('receipt_allocation',ARRAY['amount','reference'])) x(f,keys) LOOP
 INSERT INTO form_versions VALUES(f,1,jsonb_build_object('id',f,'name',replace(initcap(f),'_',' '),'version',1,'fields','[]'::jsonb));
 FOR r IN SELECT * FROM roles LOOP
 editable=r.body->>'stage' IN('admin','finance') OR (f='milestone' AND r.body->>'stage'='manager');
 perm=jsonb_build_object('actions',CASE WHEN NOT editable THEN '[]'::jsonb WHEN f IN('corporate_account','schedule_template') THEN '["view"]'::jsonb ELSE '["view","create","edit","submit","review","approve","reject"]'::jsonb END,'fields',(SELECT jsonb_object_agg(k,CASE WHEN NOT editable THEN 'hidden' WHEN f IN('corporate_account','schedule_template') THEN 'view' ELSE 'edit' END) FROM unnest(keys) k));
 UPDATE roles SET body=jsonb_set(body,ARRAY['permissions',f],perm,true) WHERE id=r.id;
 END LOOP;END LOOP;END $$;
CREATE FUNCTION extension_approved(d text) RETURNS boolean LANGUAGE sql STABLE AS $$ SELECT EXISTS(SELECT 1 FROM erp_extensions e WHERE e.id=d AND EXISTS(SELECT 1 FROM erp_extension_events WHERE document_id=d AND action='approve' AND stage='superuser') AND ((e.body->>'creatorSuperuser')::boolean OR EXISTS(SELECT 1 FROM erp_extension_events WHERE document_id=d AND action='approve' AND stage IN('admin','finance'))) AND NOT EXISTS(SELECT 1 FROM erp_extension_events WHERE document_id=d AND action='reject')) $$;
CREATE FUNCTION validate_item_metadata(t text,f text,ver int,data jsonb) RETURNS void LANGUAGE plpgsql SECURITY DEFINER SET search_path=public,pg_temp AS $$ DECLARE fields jsonb;field jsonb;k text;v text;n numeric;BEGIN
 SELECT coalesce(body->'itemFields','[]') INTO fields FROM form_versions WHERE id=f AND version=ver;
 IF jsonb_typeof(data)<>'object' OR EXISTS(SELECT 1 FROM jsonb_object_keys(data) x WHERE NOT EXISTS(SELECT 1 FROM jsonb_array_elements(fields) j WHERE j->>'key'=x)) THEN RAISE EXCEPTION 'Unknown item field';END IF;
 FOR field IN SELECT * FROM jsonb_array_elements(fields) LOOP k=field->>'key';v=trim(coalesce(data->>k,''));IF coalesce((field->>'required')::boolean,false) AND v='' THEN RAISE EXCEPTION 'Required item field: %',k;END IF;
 IF v<>'' THEN IF NOT is_platform_admin() AND NOT EXISTS(SELECT 1 FROM membership_roles m JOIN roles r ON r.id=m.role_id WHERE m.tenant_id=t AND m.user_id=current_actor() AND r.body->'permissions'->f->'fields'->>('item_'||k)='edit') THEN RAISE EXCEPTION 'Item field editing permission required';END IF;
 IF field->>'type'='number' THEN n=v::numeric;IF n::text IN('NaN','Infinity','-Infinity') THEN RAISE EXCEPTION 'Finite item number required';END IF;END IF;
 IF field->>'type'='date' AND (v!~'^\d{4}-\d{2}-\d{2}$' OR to_char(v::date,'YYYY-MM-DD')<>v) THEN RAISE EXCEPTION 'Valid item date required';END IF;END IF;END LOOP;END $$;
CREATE FUNCTION item_metadata_guard() RETURNS trigger LANGUAGE plpgsql AS $$ DECLARE l jsonb;f text;BEGIN f=CASE WHEN TG_TABLE_NAME='work_orders' THEN 'work_order' ELSE 'purchase_order' END;FOR l IN SELECT * FROM jsonb_array_elements(coalesce(NEW.body->CASE WHEN f='work_order' THEN 'items' ELSE 'lines' END,'[]')) LOOP PERFORM validate_item_metadata(NEW.tenant_id,f,NEW.form_version,coalesce(l->'itemData','{}'));END LOOP;RETURN NEW;END $$;
CREATE TRIGGER item_details_work BEFORE INSERT ON work_orders FOR EACH ROW EXECUTE FUNCTION item_metadata_guard();
CREATE TRIGGER item_details_purchase BEFORE INSERT OR UPDATE ON purchase_orders FOR EACH ROW EXECUTE FUNCTION item_metadata_guard();
CREATE FUNCTION pricing_total(p jsonb) RETURNS bigint LANGUAGE plpgsql IMMUTABLE AS $$ DECLARE a bigint;b bigint;g int;td int;l jsonb;BEGIN
 IF jsonb_typeof(p->'partA')<>'array' OR jsonb_typeof(p->'partB')<>'array' THEN RAISE EXCEPTION 'Charge lists required';END IF;
 a=0;b=0;g=(p->>'gstBps')::int;td=(p->>'tdsBps')::int;IF g IS NULL OR td IS NULL OR g NOT BETWEEN 0 AND 10000 OR td NOT BETWEEN 0 AND 10000 THEN RAISE EXCEPTION 'Invalid configured tax rates';END IF;
 FOR l IN SELECT * FROM jsonb_array_elements(p->'partA') LOOP IF coalesce(l->>'label','')='' OR coalesce((l->>'amount')::bigint,-1)<0 THEN RAISE EXCEPTION 'Invalid charge';END IF;a=a+(l->>'amount')::bigint;END LOOP;
 FOR l IN SELECT * FROM jsonb_array_elements(p->'partB') LOOP IF coalesce(l->>'label','')='' OR coalesce((l->>'amount')::bigint,-1)<0 OR coalesce((l->>'gstBps')::int,-1) NOT BETWEEN 0 AND 10000 THEN RAISE EXCEPTION 'Invalid possession charge';END IF;b=b+(l->>'amount')::bigint+round((l->>'amount')::numeric*(l->>'gstBps')::numeric/10000);END LOOP;
 IF a<=0 OR a+b>100000000000000 THEN RAISE EXCEPTION 'Invalid sale value';END IF;RETURN a+round(a::numeric*g/10000)::bigint+b;END $$;
CREATE FUNCTION schedule_snapshot(b jsonb,t jsonb) RETURNS jsonb LANGUAGE plpgsql IMMUTABLE AS $$ DECLARE p jsonb;btotal bigint;g int;td int;other bigint;bb bigint;r jsonb;rows jsonb:='[]';tot jsonb;percent int:=0;c bigint;used bigint:=0;ug bigint:=0;ut bigint:=0;base bigint;gst bigint;tds bigint;BEGIN
 p=b->'pricing';PERFORM pricing_total(p);SELECT sum((value->>'amount')::bigint) INTO btotal FROM jsonb_array_elements(p->'partA');g=(p->>'gstBps')::int;td=(p->>'tdsBps')::int;
 SELECT coalesce(sum((value->>'amount')::bigint),0),coalesce(sum((value->>'amount')::bigint+round((value->>'amount')::numeric*(value->>'gstBps')::int/10000)::bigint),0) INTO bb,other FROM jsonb_array_elements(p->'partB');
 FOR r IN SELECT * FROM jsonb_array_elements(t->'rows') LOOP percent=percent+(r->>'percent')::int;c=round(btotal::numeric*percent/100)::bigint;base=c-used;gst=round(c::numeric*g/10000)::bigint-ug;tds=round(c::numeric*td/10000)::bigint-ut;used=c;ug=ug+gst;ut=ut+tds;rows=rows||jsonb_build_array(r||jsonb_build_object('base',base,'gst',gst,'tds',tds,'gross',base+gst,'net',base+gst-tds));END LOOP;
 rows=rows||jsonb_build_array(jsonb_build_object('code','possession','label','Part B · possession charges','trigger','milestone','delayDays',0,'percent',null,'base',bb,'gst',other-bb,'tds',0,'gross',other,'net',other));
 tot=jsonb_build_object('base',btotal,'gst',round(btotal::numeric*g/10000)::bigint,'tds',round(btotal::numeric*td/10000)::bigint,'partB',other,'grandTotal',pricing_total(p));RETURN jsonb_build_object('rows',rows,'totals',tot);END $$;

CREATE FUNCTION extension_guard() RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER SET search_path=public,pg_temp AS $$ DECLARE p erp_extensions%ROWTYPE;q erp_extensions%ROWTYPE;s operational_records%ROWTYPE;e finance_documents%ROWTYPE;r jsonb;l jsonb;keys text[];k text;total bigint;used bigint;due date;code text;wing text;BEGIN
 IF TG_OP='DELETE' THEN RAISE EXCEPTION 'Records retained';END IF;
 IF TG_OP='UPDATE' AND (NEW.kind NOT IN('booking','corporate_account','corporate_payment') OR NEW.id<>OLD.id OR NEW.tenant_id<>OLD.tenant_id OR NEW.kind<>OLD.kind OR NEW.body->>'createdBy'<>OLD.body->>'createdBy') THEN RAISE EXCEPTION 'Record history retained';END IF;
 IF NOT has_tenant(NEW.tenant_id) OR NOT project_form_action(NEW.tenant_id,NEW.kind,CASE WHEN TG_OP='INSERT' THEN 'create' ELSE 'edit' END) THEN RAISE EXCEPTION 'Form action required';END IF;
 keys=CASE WHEN NEW.kind='corporate_account' OR NEW.kind='schedule_template' THEN ARRAY['details'] WHEN NEW.kind='receipt_allocation' THEN ARRAY['amount','reference'] WHEN NEW.kind='milestone' THEN ARRAY['details','dates'] ELSE ARRAY['amount','reference','details','dates'] END;
 IF NOT is_platform_admin() THEN FOREACH k IN ARRAY keys LOOP IF NOT EXISTS(SELECT 1 FROM membership_roles m JOIN roles r ON r.id=m.role_id WHERE m.tenant_id=NEW.tenant_id AND m.user_id=current_actor() AND r.body->'permissions'->NEW.kind->'fields'->>k='edit') THEN RAISE EXCEPTION 'Field editing permission required';END IF;END LOOP;END IF;
 IF TG_OP='INSERT' THEN NEW.body=NEW.body||jsonb_build_object('createdBy',current_actor(),'creatorSuperuser',is_platform_admin(),'approvalPolicy',2,'createdAt',clock_timestamp());ELSE NEW.body=NEW.body||jsonb_build_object('createdBy',OLD.body->>'createdBy','createdAt',OLD.body->>'createdAt','creatorSuperuser',OLD.body->'creatorSuperuser','approvalPolicy',2,'formVersion',OLD.body->'formVersion');IF NOT is_platform_admin() AND OLD.body->>'createdBy'<>current_actor() THEN RAISE EXCEPTION 'Creator permission required';END IF;END IF;
 PERFORM validate_record_metadata(NEW.tenant_id,NEW.kind,(NEW.body->>'formVersion')::int,coalesce(NEW.body->'data','{}'),CASE WHEN TG_OP='UPDATE' THEN OLD.body->'data' ELSE NULL END);
 IF NEW.kind='corporate_account' THEN
 IF NOT is_platform_admin() OR coalesce(NEW.body->>'ifsc','')!~'^[A-Z]{4}0[A-Z0-9]{6}$' OR trim(coalesce(NEW.body->>'bank',''))='' OR trim(coalesce(NEW.body->>'label',''))='' THEN RAISE EXCEPTION 'Superuser and valid bank details required';END IF;
 IF jsonb_typeof(NEW.body->'active') IS DISTINCT FROM 'boolean' OR ((NEW.body->>'active')::boolean=false AND NOT EXISTS(SELECT 1 FROM erp_extensions WHERE kind='corporate_account' AND id<>NEW.id AND (body->>'active')::boolean)) THEN RAISE EXCEPTION 'Keep one active corporate account';END IF;
 IF TG_OP='UPDATE' AND NEW.body->>'number' IS DISTINCT FROM OLD.body->>'number' THEN RAISE EXCEPTION 'Keep account number and one active account';END IF;
 ELSIF NEW.kind='corporate_payment' THEN
 SELECT * INTO e FROM finance_documents WHERE id=NEW.body->>'expenseId' FOR UPDATE;SELECT * INTO p FROM erp_extensions WHERE id=NEW.body->>'accountId';
 IF e.kind IS DISTINCT FROM 'expense' OR e.tenant_id<>NEW.tenant_id OR NOT finance_approved(e.id) OR p.kind IS DISTINCT FROM 'corporate_account' OR coalesce((p.body->>'active')::boolean,false)=false THEN RAISE EXCEPTION 'Approved expense and active corporate account required';END IF;
 IF TG_OP='UPDATE' AND EXISTS(SELECT 1 FROM erp_extension_events WHERE document_id=OLD.id) THEN RAISE EXCEPTION 'Only draft payments editable';END IF;
 total=coalesce((NEW.body->>'paid')::bigint,-1)+coalesce((NEW.body->>'tds')::bigint,-1);SELECT coalesce(sum((body->>'amount')::bigint),0) INTO used FROM erp_extensions WHERE kind='corporate_payment' AND body->>'expenseId'=e.id AND extension_approved(id);
 IF coalesce((NEW.body->>'paid')::bigint,-1)<0 OR coalesce((NEW.body->>'tds')::bigint,-1)<0 OR total<=0 OR total+used>e.amount OR (NEW.body->>'paidDate')::date<e.expense_date OR trim(coalesce(NEW.body->>'reference',''))='' THEN RAISE EXCEPTION 'Invalid expense settlement';END IF;
 NEW.body=NEW.body||jsonb_build_object('amount',total,'payee',e.body->>'payee','description',e.body->>'description','category',e.body->>'category');
 ELSIF NEW.kind='booking' THEN
 SELECT * INTO s FROM operational_records WHERE id=NEW.body->>'saleId' FOR UPDATE;IF s.kind IS DISTINCT FROM 'sale' OR s.tenant_id<>NEW.tenant_id OR s.body->>'status'<>'Booked' OR pricing_total(NEW.body->'pricing')<>(s.body->>'amount')::bigint OR NEW.body->>'bookingDate'<>s.body->>'date' THEN RAISE EXCEPTION 'Booked sale and matching pricing/date required';END IF;
 IF trim(coalesce(NEW.body->'details'->'applicant'->>'fullName',''))='' OR trim(coalesce(NEW.body->'details'->'applicant'->>'phone',''))='' OR coalesce(NEW.body->'details'->'applicant'->>'email','')!~'^\S+@\S+\.\S+$' THEN RAISE EXCEPTION 'Applicant contact required';END IF;
 IF TG_OP='UPDATE' AND EXISTS(SELECT 1 FROM erp_extensions WHERE kind='payment_schedule' AND body->>'bookingId'=OLD.id) AND (NEW.body->'pricing'<>OLD.body->'pricing' OR NEW.body->>'wing'<>OLD.body->>'wing' OR NEW.body->>'bookingDate'<>OLD.body->>'bookingDate') THEN RAISE EXCEPTION 'Schedule pricing retained';END IF;
 ELSIF NEW.kind='schedule_template' THEN
 IF NOT is_platform_admin() OR jsonb_typeof(NEW.body->'rows')<>'array' OR jsonb_array_length(NEW.body->'rows') NOT BETWEEN 1 AND 50 OR (SELECT sum((value->>'percent')::int) FROM jsonb_array_elements(NEW.body->'rows'))<>100 OR (SELECT count(DISTINCT value->>'code') FROM jsonb_array_elements(NEW.body->'rows'))<>jsonb_array_length(NEW.body->'rows') THEN RAISE EXCEPTION 'Superuser and percentages totalling 100 required';END IF;
 FOR r IN SELECT * FROM jsonb_array_elements(NEW.body->'rows') LOOP IF r->>'code'='possession' OR r->>'code'!~'^\w{1,60}$' OR (r->>'percent')::int NOT BETWEEN 1 AND 100 OR r->>'trigger' NOT IN('booking','milestone') OR (r->>'delayDays')::int NOT BETWEEN 0 AND 3650 THEN RAISE EXCEPTION 'Invalid instalment';END IF;END LOOP;
 ELSIF NEW.kind='payment_schedule' THEN
 SELECT * INTO p FROM erp_extensions WHERE id=NEW.body->>'bookingId';SELECT * INTO q FROM erp_extensions WHERE id=NEW.body->>'templateId';IF p.kind IS DISTINCT FROM 'booking' OR q.kind IS DISTINCT FROM 'schedule_template' OR p.tenant_id<>NEW.tenant_id OR q.tenant_id<>NEW.tenant_id THEN RAISE EXCEPTION 'Project booking and template required';END IF;
 IF NEW.body->'rows'<>schedule_snapshot(p.body,q.body)->'rows' OR NEW.body->'totals'<>schedule_snapshot(p.body,q.body)->'totals' THEN RAISE EXCEPTION 'Schedule amounts must match booking percentages and tax';END IF;
 NEW.body=NEW.body||jsonb_build_object('saleId',p.body->>'saleId','unit',p.body->>'unit','wing',p.body->>'wing','bookingDate',p.body->>'bookingDate');
 ELSIF NEW.kind='milestone' THEN
 IF NEW.body->>'date' IS NULL OR (NEW.body->>'date')::date>current_date OR trim(coalesce(NEW.body->>'note',''))='' THEN RAISE EXCEPTION 'Completion date and note required';END IF;
 ELSIF NEW.kind='demand' THEN
 SELECT * INTO p FROM erp_extensions WHERE id=NEW.body->>'scheduleId';IF p.kind IS DISTINCT FROM 'payment_schedule' OR p.tenant_id<>NEW.tenant_id THEN RAISE EXCEPTION 'Project schedule required';END IF;
 SELECT value INTO r FROM jsonb_array_elements(p.body->'rows') WHERE value->>'code'=NEW.body->>'code';IF r IS NULL THEN RAISE EXCEPTION 'Instalment not found';END IF;
 IF EXISTS(SELECT 1 FROM erp_extensions d WHERE kind='demand' AND d.body->>'scheduleId'=p.id AND d.body->>'code'=r->>'code' AND NOT EXISTS(SELECT 1 FROM erp_extension_events WHERE document_id=d.id AND action='reject')) THEN RAISE EXCEPTION 'Active demand already exists';END IF;
 due=(p.body->>'bookingDate')::date;
 IF r->>'trigger'='milestone' THEN SELECT (body->>'date')::date INTO due FROM erp_extensions WHERE kind='milestone' AND tenant_id=NEW.tenant_id AND body->>'code'=r->>'code' AND coalesce(body->>'wing','') IN('',p.body->>'wing') ORDER BY (coalesce(body->>'wing','')=p.body->>'wing') DESC,(body->>'createdAt')::timestamptz DESC,id DESC LIMIT 1;END IF;
 IF due IS NULL THEN RAISE EXCEPTION 'Milestone completion required';END IF;due=due+(r->>'delayDays')::int;
 IF coalesce((r->>'gross')::bigint,0)<=0 THEN RAISE EXCEPTION 'No payment due';END IF;
 IF NOT EXISTS(SELECT 1 FROM rera_accounts WHERE id=NEW.body->>'accountId' AND tenant_id=NEW.tenant_id) THEN RAISE EXCEPTION 'Project RERA account required';END IF;
 SELECT * INTO s FROM operational_records WHERE id=p.body->>'saleId';SELECT body INTO l FROM rera_accounts WHERE id=NEW.body->>'accountId';NEW.body=NEW.body||r||jsonb_build_object('dueDate',due,'saleId',p.body->>'saleId','unit',p.body->>'unit','customer',s.body->>'customer','bank',jsonb_build_object('bank',l->>'bank','number',l->>'number','ifsc',l->>'ifsc','label',l->>'label'),'amount',(r->>'gross')::bigint);
 ELSIF NEW.kind='receipt_allocation' THEN
 SELECT * INTO p FROM erp_extensions WHERE id=NEW.body->>'demandId' FOR UPDATE;SELECT * INTO s FROM operational_records WHERE id=NEW.body->>'receiptId' FOR UPDATE;
 IF p.kind IS DISTINCT FROM 'demand' OR s.kind IS DISTINCT FROM 'collection' OR p.tenant_id<>NEW.tenant_id OR s.tenant_id<>NEW.tenant_id OR NOT extension_approved(p.id) OR p.body->>'saleId'<>s.ref_sale THEN RAISE EXCEPTION 'Matching receipt and approved demand required';END IF;
 total=(NEW.body->>'amount')::bigint;IF total<=0 OR total IS NULL OR total+(SELECT coalesce(sum((body->>'amount')::bigint),0) FROM erp_extensions WHERE kind='receipt_allocation' AND body->>'receiptId'=s.id)>(s.body->>'amount')::bigint+coalesce((s.body->>'tds')::bigint,0) OR total+(SELECT coalesce(sum((body->>'amount')::bigint),0) FROM erp_extensions WHERE kind='receipt_allocation' AND body->>'demandId'=p.id)>(p.body->>'gross')::bigint THEN RAISE EXCEPTION 'Allocation exceeds receipt or demand balance';END IF;
 END IF;
 IF NEW.kind='receipt_allocation' THEN IF coalesce((NEW.body->>'tds')::bigint,0)<0 OR coalesce((NEW.body->>'tds')::bigint,0)>total OR coalesce((NEW.body->>'tds')::bigint,0)+(SELECT coalesce(sum((body->>'tds')::bigint),0) FROM erp_extensions WHERE kind='receipt_allocation' AND body->>'receiptId'=s.id)>coalesce((s.body->>'tds')::bigint,0) OR total-coalesce((NEW.body->>'tds')::bigint,0)+(SELECT coalesce(sum((body->>'amount')::bigint-coalesce((body->>'tds')::bigint,0)),0) FROM erp_extensions WHERE kind='receipt_allocation' AND body->>'receiptId'=s.id)>(s.body->>'amount')::bigint OR coalesce((NEW.body->>'tds')::bigint,0)+(SELECT coalesce(sum((body->>'tds')::bigint),0) FROM erp_extensions WHERE kind='receipt_allocation' AND body->>'demandId'=p.id)>(p.body->>'tds')::bigint THEN RAISE EXCEPTION 'Allocation exceeds cash or TDS';END IF;END IF;
 NEW.body=NEW.body||jsonb_build_object('id',NEW.id,'kind',NEW.kind,'tenantId',NEW.tenant_id);RETURN NEW;END $$;
CREATE TRIGGER extension_validation BEFORE INSERT OR UPDATE OR DELETE ON erp_extensions FOR EACH ROW EXECUTE FUNCTION extension_guard();
CREATE FUNCTION extension_workflow() RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER SET search_path=public,pg_temp AS $$ DECLARE d erp_extensions%ROWTYPE;e finance_documents%ROWTYPE;ev jsonb;used bigint;BEGIN
 SELECT * INTO d FROM erp_extensions WHERE id=NEW.document_id FOR UPDATE;IF d.kind NOT IN('demand','corporate_payment') THEN RAISE EXCEPTION 'Financial record required';END IF;
 SELECT coalesce(jsonb_agg(jsonb_build_object('action',action,'stage',stage,'actorId',actor_id)),'[]') INTO ev FROM erp_extension_events WHERE document_id=d.id;
 PERFORM validate_approval_step(2,(d.body->>'creatorSuperuser')::boolean,d.body->>'createdBy',d.tenant_id,d.kind,ev,NEW.action,NEW.stage,NEW.actor_id);
 IF d.kind='corporate_payment' AND NEW.action IN('submit','approve') THEN
 SELECT * INTO e FROM finance_documents WHERE id=d.body->>'expenseId' FOR UPDATE;SELECT coalesce(sum((body->>'amount')::bigint),0) INTO used FROM erp_extensions WHERE kind='corporate_payment' AND body->>'expenseId'=e.id AND extension_approved(id);
 IF used+(d.body->>'amount')::bigint>e.amount OR NOT EXISTS(SELECT 1 FROM erp_extensions WHERE id=d.body->>'accountId' AND (body->>'active')::boolean) THEN RAISE EXCEPTION 'Expense settlement exceeds balance or account inactive';END IF;END IF;
 NEW.body=NEW.body||jsonb_build_object('id',NEW.id,'documentId',d.id,'action',NEW.action,'stage',NEW.stage,'actorId',NEW.actor_id,'at',clock_timestamp());RETURN NEW;END $$;
CREATE TRIGGER extension_workflow_guard BEFORE INSERT ON erp_extension_events FOR EACH ROW EXECUTE FUNCTION extension_workflow();
CREATE TRIGGER extension_events_retained BEFORE UPDATE OR DELETE ON erp_extension_events FOR EACH ROW EXECUTE FUNCTION freeze_record();
CREATE UNIQUE INDEX one_booking_per_sale ON erp_extensions((body->>'saleId')) WHERE kind='booking';
CREATE UNIQUE INDEX one_schedule_per_sale ON erp_extensions((body->>'saleId')) WHERE kind='payment_schedule';
CREATE UNIQUE INDEX unique_demand_number ON erp_extensions(tenant_id,lower(body->>'number')) WHERE kind='demand';
CREATE UNIQUE INDEX unique_corporate_payment_ref ON erp_extensions((body->>'accountId'),lower(body->>'reference')) WHERE kind='corporate_payment';
CREATE FUNCTION retain_scheduled_sale() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.kind='sale' AND EXISTS(SELECT 1 FROM erp_extensions WHERE kind='payment_schedule' AND body->>'saleId'=OLD.id) AND (NEW.body->>'amount'<>OLD.body->>'amount' OR NEW.body->>'unit'<>OLD.body->>'unit' OR NEW.body->>'date'<>OLD.body->>'date' OR NEW.body->>'status'<>OLD.body->>'status') THEN RAISE EXCEPTION 'Scheduled sale value and unit retained';END IF;RETURN NEW;END $$;
CREATE TRIGGER scheduled_sale_guard BEFORE UPDATE ON operational_records FOR EACH ROW EXECUTE FUNCTION retain_scheduled_sale();
ALTER TABLE erp_extensions ENABLE ROW LEVEL SECURITY;
CREATE POLICY extension_select ON erp_extensions FOR SELECT USING(has_tenant(tenant_id) OR kind='corporate_account');
CREATE POLICY extension_insert ON erp_extensions FOR INSERT WITH CHECK(has_tenant(tenant_id));
CREATE POLICY extension_update ON erp_extensions FOR UPDATE USING(has_tenant(tenant_id) OR kind='corporate_account' AND is_platform_admin()) WITH CHECK(has_tenant(tenant_id));
ALTER TABLE erp_extension_events ENABLE ROW LEVEL SECURITY;
CREATE POLICY extension_events_select ON erp_extension_events FOR SELECT USING(EXISTS(SELECT 1 FROM erp_extensions WHERE id=document_id));
CREATE POLICY extension_events_insert ON erp_extension_events FOR INSERT WITH CHECK(actor_id=current_actor() AND EXISTS(SELECT 1 FROM erp_extensions WHERE id=document_id));

ALTER TABLE erp_extensions DISABLE TRIGGER extension_validation;
INSERT INTO erp_extensions SELECT 'reference-schedule-'||id,id,'schedule_template',jsonb_build_object('id','reference-schedule-'||id,'kind','schedule_template','tenantId',id,'createdAt','2026-10-06T08:00:00Z','createdBy','u1','formVersion',1,'data','{}'::jsonb,'name','Reference · Part A + possession','version',1,'rows','[{"code":"booking","label":"Booking","percent":5,"trigger":"booking","delayDays":0},{"code":"agreement","label":"Agreement","percent":15,"trigger":"booking","delayDays":5},{"code":"excavation","label":"Excavation completion","percent":5,"trigger":"milestone","delayDays":15},{"code":"footing","label":"Footing completion","percent":6,"trigger":"milestone","delayDays":15},{"code":"ground","label":"Ground floor slab","percent":6,"trigger":"milestone","delayDays":15},{"code":"parking","label":"Parking slab","percent":5,"trigger":"milestone","delayDays":15},{"code":"slab_3","label":"Floor 3 slab","percent":5,"trigger":"milestone","delayDays":15},{"code":"slab_6","label":"Floor 6 slab","percent":5,"trigger":"milestone","delayDays":15},{"code":"slab_9","label":"Floor 9 slab","percent":5,"trigger":"milestone","delayDays":15},{"code":"slab_12","label":"Floor 12 slab","percent":5,"trigger":"milestone","delayDays":15},{"code":"slab_15","label":"Floor 15 slab","percent":5,"trigger":"milestone","delayDays":15},{"code":"slab_18","label":"Floor 18 slab","percent":5,"trigger":"milestone","delayDays":15},{"code":"slab_21","label":"Floor 21 slab","percent":5,"trigger":"milestone","delayDays":15},{"code":"slab_24","label":"Floor 24 slab","percent":5,"trigger":"milestone","delayDays":15},{"code":"superstructure","label":"Superstructure completion","percent":5,"trigger":"milestone","delayDays":15},{"code":"flooring","label":"Flooring & painting","percent":4,"trigger":"milestone","delayDays":15},{"code":"electrical","label":"Electrical & plumbing","percent":4,"trigger":"milestone","delayDays":15},{"code":"registration","label":"Registration","percent":5,"trigger":"milestone","delayDays":0}]'::jsonb) FROM tenants WHERE NOT deleted;
ALTER TABLE erp_extensions ENABLE TRIGGER extension_validation;

UPDATE roles SET body=jsonb_set(jsonb_set(body,'{permissions,payment_schedule}','{"actions":["view"],"fields":{"amount":"hidden","reference":"view","details":"view","dates":"view"}}'::jsonb),'{permissions,schedule_template}','{"actions":["view"],"fields":{"details":"view"}}'::jsonb) WHERE body->>'stage'='manager';

CREATE FUNCTION new_project_schedule() RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER SET search_path=public,pg_temp AS $$ DECLARE r jsonb;BEGIN
 SELECT body->'rows' INTO r FROM erp_extensions WHERE kind='schedule_template' AND id LIKE 'reference-schedule-%' ORDER BY id LIMIT 1;
 IF r IS NOT NULL THEN INSERT INTO erp_extensions VALUES('reference-schedule-'||NEW.id,NEW.id,'schedule_template',jsonb_build_object('formVersion',1,'data','{}'::jsonb,'name','Reference · Part A + possession','version',1,'rows',r));END IF;RETURN NULL;END $$;
CREATE TRIGGER new_project_schedule AFTER INSERT ON tenants FOR EACH ROW EXECUTE FUNCTION new_project_schedule();
