INSERT INTO form_versions VALUES
('expense',1,'{"id":"expense","name":"Construction Expense","version":1,"fields":[]}'),
('payment',1,'{"id":"payment","name":"Vendor Payment","version":1,"fields":[]}'),
('cost_overview',1,'{"id":"cost_overview","name":"Project Cost Overview","version":1,"fields":[]}'),
('vendor_onboarding',1,'{"id":"vendor_onboarding","name":"Vendor Onboarding","version":1,"fields":[]}');
UPDATE roles SET body=jsonb_set(jsonb_set(body,'{permissions,expense}','{"actions":["view","create","edit","submit","review","approve","reject"],"fields":{"amount":"edit","description":"edit","category":"edit","dates":"edit","remarks":"edit","reference":"edit"}}'),'{permissions,payment}','{"actions":["view","create","edit","submit","review","approve","reject","amend"],"fields":{"amount":"edit","description":"edit","category":"edit","dates":"edit","remarks":"edit","reference":"edit"}}') WHERE body->>'stage' IN('manager','admin','finance');
UPDATE roles SET body=jsonb_set(jsonb_set(body,'{permissions,cost_overview}','{"actions":["view"],"fields":{"amount":"view"}}'),'{permissions,vendor_onboarding}','{"actions":["view","create","edit","submit"],"fields":{"name":"edit","trade":"edit","vendorType":"edit","contactName":"edit","phone":"edit","email":"edit","gstin":"edit","pan":"edit","address":"edit","notes":"edit","onboardingStatus":"view"}}') WHERE body->>'stage'='admin';
CREATE TABLE vendor_invoices(id text PRIMARY KEY,tenant_id text NOT NULL REFERENCES tenants,order_id text NOT NULL,vendor_id text NOT NULL,invoice_number text NOT NULL CHECK(length(trim(invoice_number)) BETWEEN 1 AND 100),invoice_date date NOT NULL,amount bigint NOT NULL CHECK(amount>0 AND amount<=100000000000000),created_by text NOT NULL REFERENCES users,body jsonb NOT NULL,UNIQUE(id,tenant_id),FOREIGN KEY(order_id,tenant_id) REFERENCES work_orders(id,tenant_id),FOREIGN KEY(vendor_id,tenant_id) REFERENCES vendors(id,tenant_id));
CREATE UNIQUE INDEX vendor_invoice_identity ON vendor_invoices(tenant_id,vendor_id,lower(trim(invoice_number)));
CREATE TABLE finance_documents(id text PRIMARY KEY,kind text NOT NULL CHECK(kind IN('expense','payment')),tenant_id text NOT NULL REFERENCES tenants,amount bigint NOT NULL CHECK(amount>0 AND amount<=100000000000000),paid bigint NOT NULL DEFAULT 0 CHECK(paid>=0),tds bigint NOT NULL DEFAULT 0 CHECK(tds>=0),invoice_id text,expense_date date,paid_date date,ref_work_order text,ref_purchase_order text,form_version integer NOT NULL,created_by text NOT NULL REFERENCES users,approval_policy integer NOT NULL DEFAULT 2 CHECK(approval_policy=2),creator_superuser boolean NOT NULL DEFAULT false,body jsonb NOT NULL,FOREIGN KEY(kind,form_version) REFERENCES form_versions(id,version),FOREIGN KEY(invoice_id,tenant_id) REFERENCES vendor_invoices(id,tenant_id),FOREIGN KEY(ref_work_order,tenant_id) REFERENCES work_orders(id,tenant_id),FOREIGN KEY(ref_purchase_order,tenant_id) REFERENCES purchase_orders(id,tenant_id),CHECK(num_nonnulls(ref_work_order,ref_purchase_order)<=1),CHECK((kind='expense' AND invoice_id IS NULL AND expense_date IS NOT NULL AND paid=0 AND tds=0 AND paid_date IS NULL) OR (kind='payment' AND invoice_id IS NOT NULL AND paid_date IS NOT NULL AND amount=paid+tds AND expense_date IS NULL AND ref_work_order IS NULL AND ref_purchase_order IS NULL)));
CREATE TABLE finance_events(id text PRIMARY KEY,document_id text NOT NULL REFERENCES finance_documents,action text NOT NULL CHECK(action IN('submit','review','approve','reject')),stage text NOT NULL DEFAULT '' CHECK(stage IN('','superuser','admin','finance')),actor_id text NOT NULL REFERENCES users,body jsonb NOT NULL,CHECK((action='approve')=(stage<>'')));
CREATE UNIQUE INDEX finance_action_once ON finance_events(document_id,action,stage);
CREATE UNIQUE INDEX finance_distinct_approver ON finance_events(document_id,actor_id) WHERE action='approve';
CREATE TABLE invoice_events(id text PRIMARY KEY,invoice_id text NOT NULL REFERENCES vendor_invoices,status text NOT NULL CHECK(status IN('Open','Closed')),reason text NOT NULL CHECK(length(trim(reason))>0),actor_id text NOT NULL REFERENCES users,payment_event_id text UNIQUE REFERENCES finance_events,body jsonb NOT NULL);
CREATE FUNCTION finance_approved(did text) RETURNS boolean LANGUAGE sql STABLE AS $$ SELECT approval_complete(d.approval_policy,d.creator_superuser,coalesce((SELECT jsonb_agg(jsonb_build_object('action',e.action,'stage',e.stage,'actorId',e.actor_id)) FROM finance_events e WHERE e.document_id=d.id),'[]'::jsonb)) FROM finance_documents d WHERE d.id=did $$;
CREATE FUNCTION invoice_balance(iid text) RETURNS bigint LANGUAGE sql STABLE AS $$ SELECT i.amount-coalesce((SELECT sum(d.amount) FROM finance_documents d WHERE d.invoice_id=i.id AND finance_approved(d.id)),0)::bigint FROM vendor_invoices i WHERE i.id=iid $$;
CREATE FUNCTION invoice_status(iid text) RETURNS text LANGUAGE sql STABLE AS $$ SELECT coalesce((SELECT status FROM invoice_events WHERE invoice_id=iid ORDER BY (body->>'at')::timestamptz DESC,id DESC LIMIT 1),'Open') $$;
CREATE FUNCTION financial_fields_editable(t text,f text) RETURNS boolean LANGUAGE sql STABLE SECURITY DEFINER SET search_path=public,pg_temp AS $$ SELECT is_platform_admin() OR NOT EXISTS(SELECT 1 FROM unnest(ARRAY['amount','description','category','dates','remarks','reference']) k WHERE NOT EXISTS(SELECT 1 FROM membership_roles m JOIN roles r ON r.id=m.role_id WHERE m.tenant_id=t AND m.user_id=current_actor() AND r.body->'permissions'->f->'fields'->>k='edit')) $$;
CREATE FUNCTION invoice_guard() RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER SET search_path=public,pg_temp AS $$
DECLARE w work_orders%ROWTYPE; v vendors%ROWTYPE;
BEGIN
 IF NEW.created_by IS DISTINCT FROM current_actor() OR NOT has_tenant(NEW.tenant_id) OR NOT project_form_action(NEW.tenant_id,'payment','create') OR NOT financial_fields_editable(NEW.tenant_id,'payment') THEN RAISE EXCEPTION 'Invoice creation and field editing permissions are required'; END IF;
 SELECT * INTO w FROM work_orders WHERE id=NEW.order_id FOR UPDATE;
 SELECT * INTO v FROM vendors WHERE id=NEW.vendor_id;
 IF w.tenant_id<>NEW.tenant_id OR w.body->>'vendorId' IS NOT NULL AND w.body->>'vendorId'<>NEW.vendor_id OR NOT EXISTS(SELECT 1 FROM work_order_vendors WHERE order_id=w.id AND vendor_id=v.id) OR v.deleted OR coalesce(v.body->>'onboardingStatus','Active')<>'Active' THEN RAISE EXCEPTION 'Select a work order with an active project vendor'; END IF;
 IF NOT project_form_action(NEW.tenant_id,'work_order','view') OR EXISTS(SELECT 1 FROM tenants WHERE id=NEW.tenant_id AND deleted) THEN RAISE EXCEPTION 'Project work order access is required'; END IF;
 IF length(trim(coalesce(NEW.body->>'description','')))=0 OR length(trim(coalesce(NEW.body->>'category','')))=0 THEN RAISE EXCEPTION 'Description and category are required'; END IF;
 IF NEW.amount+coalesce((SELECT sum(amount) FROM vendor_invoices WHERE order_id=w.id),0)>effective_total(w.id) THEN RAISE EXCEPTION 'Invoices cannot exceed the current work order value'; END IF;
 NEW.body=NEW.body || jsonb_build_object('id',NEW.id,'tenantId',NEW.tenant_id,'orderId',w.id,'orderNumber',w.number,'orderDate',coalesce(w.body->>'orderDate',substring(w.body->>'createdAt',1,10)),'vendorId',v.id,'vendor',v.body->>'name','invoiceNumber',trim(NEW.invoice_number),'invoiceDate',NEW.invoice_date::text,'amount',NEW.amount,'createdBy',NEW.created_by);
 RETURN NEW;
END $$;
CREATE TRIGGER invoice_validation BEFORE INSERT ON vendor_invoices FOR EACH ROW EXECUTE FUNCTION invoice_guard();
CREATE TRIGGER invoice_retained BEFORE UPDATE OR DELETE ON vendor_invoices FOR EACH ROW EXECUTE FUNCTION freeze_record();
CREATE FUNCTION finance_draft_guard() RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER SET search_path=public,pg_temp AS $$
DECLARE i vendor_invoices%ROWTYPE;
BEGIN
 IF TG_OP='DELETE' THEN RAISE EXCEPTION 'Financial history is retained'; END IF;
 IF TG_OP='UPDATE' AND (NEW.id<>OLD.id OR NEW.kind<>OLD.kind OR NEW.tenant_id<>OLD.tenant_id OR NEW.created_by<>OLD.created_by OR NEW.form_version<>OLD.form_version OR EXISTS(SELECT 1 FROM finance_events WHERE document_id=OLD.id)) THEN RAISE EXCEPTION 'Only financial drafts can be edited'; END IF;
 IF (TG_OP='INSERT' AND NEW.created_by IS DISTINCT FROM current_actor()) OR (TG_OP='UPDATE' AND NEW.created_by<>current_actor() AND NOT is_platform_admin()) OR NOT has_tenant(NEW.tenant_id) OR NOT project_form_action(NEW.tenant_id,NEW.kind,CASE WHEN TG_OP='INSERT' THEN 'create' ELSE 'edit' END) OR NOT financial_fields_editable(NEW.tenant_id,NEW.kind) OR EXISTS(SELECT 1 FROM tenants WHERE id=NEW.tenant_id AND deleted) THEN RAISE EXCEPTION 'Financial draft editing permission is required'; END IF;
 IF NEW.kind='payment' THEN
  SELECT * INTO i FROM vendor_invoices WHERE id=NEW.invoice_id;
  IF invoice_status(i.id)<>'Open' OR NEW.amount>invoice_balance(i.id) THEN RAISE EXCEPTION 'Payment exceeds invoice balance or invoice is closed'; END IF;
  IF NEW.paid_date<i.invoice_date THEN RAISE EXCEPTION 'Paid date must be on or after invoice date'; END IF;
 ELSE
  IF length(trim(coalesce(NEW.body->>'description','')))=0 OR length(trim(coalesce(NEW.body->>'category','')))=0 OR length(trim(coalesce(NEW.body->>'payee','')))=0 THEN RAISE EXCEPTION 'Expense description, category and payee are required'; END IF;
  IF NEW.ref_purchase_order IS NOT NULL AND NOT purchase_approved(NEW.ref_purchase_order) THEN RAISE EXCEPTION 'Referenced purchase order must be approved'; END IF;
 END IF;
 NEW.body=NEW.body || jsonb_build_object('id',NEW.id,'kind',NEW.kind,'tenantId',NEW.tenant_id,'amount',NEW.amount,'createdBy',NEW.created_by,'formVersion',NEW.form_version);
 IF NEW.kind='payment' THEN NEW.body=NEW.body || jsonb_build_object('paid',NEW.paid,'tds',NEW.tds,'invoiceId',NEW.invoice_id,'paidDate',NEW.paid_date::text);
 ELSE NEW.body=NEW.body || jsonb_build_object('expenseDate',NEW.expense_date::text,'referenceType',CASE WHEN NEW.ref_work_order IS NOT NULL THEN 'work_order' WHEN NEW.ref_purchase_order IS NOT NULL THEN 'purchase_order' ELSE '' END,'referenceId',coalesce(NEW.ref_work_order,NEW.ref_purchase_order,''));END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER approval_metadata BEFORE INSERT OR UPDATE ON finance_documents FOR EACH ROW EXECUTE FUNCTION approval_document_metadata();
CREATE TRIGGER finance_draft BEFORE INSERT OR UPDATE OR DELETE ON finance_documents FOR EACH ROW EXECUTE FUNCTION finance_draft_guard();
CREATE FUNCTION finance_workflow_guard() RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER SET search_path=public,pg_temp AS $$
DECLARE d finance_documents%ROWTYPE; ev jsonb;
BEGIN
 SELECT * INTO d FROM finance_documents WHERE id=NEW.document_id;
 IF d.kind='payment' THEN PERFORM 1 FROM vendor_invoices WHERE id=d.invoice_id FOR UPDATE; END IF;
 SELECT * INTO d FROM finance_documents WHERE id=NEW.document_id FOR UPDATE;
 SELECT coalesce(jsonb_agg(jsonb_build_object('action',action,'stage',stage,'actorId',actor_id)),'[]'::jsonb) INTO ev FROM finance_events WHERE document_id=d.id;
 PERFORM validate_approval_step(d.approval_policy,d.creator_superuser,d.created_by,d.tenant_id,d.kind,ev,NEW.action,NEW.stage,NEW.actor_id);
 IF d.kind='payment' AND NEW.action IN('submit','approve') AND (invoice_status(d.invoice_id)<>'Open' OR d.amount>invoice_balance(d.invoice_id)) THEN RAISE EXCEPTION 'Approval blocked: payment exceeds invoice balance or invoice is closed'; END IF;
 NEW.body=NEW.body || jsonb_build_object('id',NEW.id,'documentId',NEW.document_id,'action',NEW.action,'stage',NEW.stage,'actorId',NEW.actor_id,'at',to_char(clock_timestamp() AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS.US"Z"'));
 RETURN NEW;
END $$;
CREATE TRIGGER finance_workflow BEFORE INSERT ON finance_events FOR EACH ROW EXECUTE FUNCTION finance_workflow_guard();
CREATE FUNCTION invoice_event_guard() RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER SET search_path=public,pg_temp AS $$
DECLARE i vendor_invoices%ROWTYPE;
BEGIN
 SELECT * INTO i FROM vendor_invoices WHERE id=NEW.invoice_id FOR UPDATE;
 IF NEW.actor_id IS DISTINCT FROM current_actor() OR NOT has_tenant(i.tenant_id) OR EXISTS(SELECT 1 FROM tenants WHERE id=i.tenant_id AND deleted) THEN RAISE EXCEPTION 'Invoice project permission is required'; END IF;
 IF NEW.payment_event_id IS NULL THEN
  IF NOT project_form_action(i.tenant_id,'payment','amend') THEN RAISE EXCEPTION 'Invoice amendment permission is required'; END IF;
 ELSIF pg_trigger_depth()<2 OR NOT EXISTS(SELECT 1 FROM finance_events e JOIN finance_documents d ON d.id=e.document_id WHERE e.id=NEW.payment_event_id AND d.invoice_id=i.id AND e.actor_id=NEW.actor_id AND e.action='approve' AND finance_approved(d.id)) THEN RAISE EXCEPTION 'Automatic status change must originate from approved payment'; END IF;
 IF NEW.status=invoice_status(i.id) THEN RAISE EXCEPTION 'Choose a different invoice status'; END IF;
 IF NEW.status='Closed' AND invoice_balance(i.id)<>0 THEN RAISE EXCEPTION 'An outstanding invoice cannot be closed'; END IF;
 NEW.body=NEW.body || jsonb_build_object('id',NEW.id,'invoiceId',i.id,'status',NEW.status,'reason',NEW.reason,'actorId',NEW.actor_id,'at',to_char(clock_timestamp() AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS.US"Z"'));
 RETURN NEW;
END $$;
CREATE TRIGGER invoice_status_validation BEFORE INSERT ON invoice_events FOR EACH ROW EXECUTE FUNCTION invoice_event_guard();
CREATE FUNCTION close_settled_invoice() RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER SET search_path=public,pg_temp AS $$
DECLARE d finance_documents%ROWTYPE; eid text;
BEGIN
 SELECT * INTO d FROM finance_documents WHERE id=NEW.document_id;
 IF d.kind='payment' AND finance_approved(d.id) AND invoice_balance(d.invoice_id)=0 AND invoice_status(d.invoice_id)='Open' THEN
  eid=gen_random_uuid()::text;
  INSERT INTO invoice_events VALUES(eid,d.invoice_id,'Closed','Invoice fully settled',NEW.actor_id,NEW.id,jsonb_build_object('at',to_char(clock_timestamp() AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS.US"Z"')));
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER close_paid_invoice AFTER INSERT ON finance_events FOR EACH ROW EXECUTE FUNCTION close_settled_invoice();
CREATE TRIGGER finance_events_retained BEFORE UPDATE OR DELETE ON finance_events FOR EACH ROW EXECUTE FUNCTION freeze_record();
CREATE TRIGGER invoice_events_retained BEFORE UPDATE OR DELETE ON invoice_events FOR EACH ROW EXECUTE FUNCTION freeze_record();
DO $$ DECLARE tbl text; BEGIN FOREACH tbl IN ARRAY ARRAY['vendor_invoices','finance_documents'] LOOP
 EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY',tbl);
 EXECUTE format('CREATE POLICY financial_select ON %I FOR SELECT USING(has_tenant(tenant_id))',tbl);
 EXECUTE format('CREATE POLICY financial_insert ON %I FOR INSERT WITH CHECK(has_tenant(tenant_id) AND created_by=current_actor())',tbl);
 END LOOP;END $$;
CREATE POLICY finance_update ON finance_documents FOR UPDATE USING(has_tenant(tenant_id)) WITH CHECK(has_tenant(tenant_id));
ALTER TABLE finance_events ENABLE ROW LEVEL SECURITY;
CREATE POLICY finance_event_select ON finance_events FOR SELECT USING(EXISTS(SELECT 1 FROM finance_documents WHERE id=document_id));
CREATE POLICY finance_event_insert ON finance_events FOR INSERT WITH CHECK(actor_id=current_actor() AND EXISTS(SELECT 1 FROM finance_documents WHERE id=document_id));
ALTER TABLE invoice_events ENABLE ROW LEVEL SECURITY;
CREATE POLICY invoice_event_select ON invoice_events FOR SELECT USING(EXISTS(SELECT 1 FROM vendor_invoices WHERE id=invoice_id));
CREATE POLICY invoice_event_insert ON invoice_events FOR INSERT WITH CHECK(actor_id=current_actor() AND EXISTS(SELECT 1 FROM vendor_invoices WHERE id=invoice_id));
-- Administrators can prepare onboarding intakes; existing directory management
-- and activation remain superuser-only.
CREATE POLICY vendor_onboarding_insert ON vendors FOR INSERT WITH CHECK(has_tenant(tenant_id) AND project_form_action(tenant_id,'vendor_onboarding','create') AND body->>'onboardingStatus'='Draft');
CREATE POLICY vendor_onboarding_update ON vendors FOR UPDATE USING(has_tenant(tenant_id) AND (project_form_action(tenant_id,'vendor_onboarding','edit') OR project_form_action(tenant_id,'vendor_onboarding','submit'))) WITH CHECK(has_tenant(tenant_id));
CREATE FUNCTION vendor_onboarding_guard() RETURNS trigger LANGUAGE plpgsql AS $$ DECLARE k text; BEGIN
 IF NOT is_platform_admin() THEN
  IF TG_OP='UPDATE' AND (NEW.id<>OLD.id OR NEW.body->>'createdBy' IS DISTINCT FROM OLD.body->>'createdBy' OR NEW.body->>'createdAt' IS DISTINCT FROM OLD.body->>'createdAt') THEN RAISE EXCEPTION 'Onboarding identity is immutable'; END IF;
  IF TG_OP='UPDATE' AND NEW.body->>'onboardingStatus'='Submitted' AND (NEW.body-ARRAY['onboardingStatus','updatedAt']) IS DISTINCT FROM (OLD.body-ARRAY['onboardingStatus','updatedAt']) THEN RAISE EXCEPTION 'Submission cannot change vendor details'; END IF;
  IF NEW.body->>'onboardingStatus'='Draft' THEN
   IF NOT project_form_action(NEW.tenant_id,'vendor_onboarding',CASE WHEN TG_OP='INSERT' THEN 'create' ELSE 'edit' END) THEN RAISE EXCEPTION 'Onboarding editing permission is required'; END IF;
   FOREACH k IN ARRAY ARRAY['name','trade','vendorType','contactName','phone','email','gstin','pan','address','notes'] LOOP
    IF NOT EXISTS(SELECT 1 FROM membership_roles m JOIN roles r ON r.id=m.role_id WHERE m.tenant_id=NEW.tenant_id AND m.user_id=current_actor() AND r.body->'permissions'->'vendor_onboarding'->'fields'->>k='edit') THEN RAISE EXCEPTION 'Onboarding field editing permission is required'; END IF;
   END LOOP;
  END IF;
  IF (TG_OP='UPDATE' AND NEW.tenant_id IS DISTINCT FROM OLD.tenant_id) OR NEW.deleted THEN RAISE EXCEPTION 'Only superuser can remove or move vendors'; END IF;
  IF TG_OP='UPDATE' AND OLD.body->>'createdBy' IS DISTINCT FROM current_actor() THEN RAISE EXCEPTION 'Only the creator can edit onboarding'; END IF;
  IF TG_OP='UPDATE' AND NOT ((OLD.body->>'onboardingStatus'='Draft' AND NEW.body->>'onboardingStatus' IN('Draft','Submitted')) OR (OLD.body->>'onboardingStatus'='Submitted' AND NEW.body=OLD.body)) THEN RAISE EXCEPTION 'Only superuser can activate, return or reject onboarding'; END IF;
  IF NEW.body->>'onboardingStatus'='Submitted' AND NOT project_form_action(NEW.tenant_id,'vendor_onboarding','submit') THEN RAISE EXCEPTION 'Onboarding submission permission is required'; END IF;
  IF TG_OP='INSERT' AND (NEW.body->>'createdBy' IS DISTINCT FROM current_actor() OR NEW.body->>'onboardingStatus'<>'Draft') THEN RAISE EXCEPTION 'Onboarding creator is required'; END IF;
 END IF;
 IF NEW.body->>'onboardingStatus' IN('Draft','Submitted') THEN
  FOREACH k IN ARRAY ARRAY['name','trade','vendorType','contactName','address'] LOOP IF length(trim(coalesce(NEW.body->>k,'')))=0 THEN RAISE EXCEPTION 'Required vendor details are missing'; END IF; END LOOP;
  IF coalesce(NEW.body->>'phone','') !~ '^\+?[0-9 ()-]{7,22}$' OR length(regexp_replace(NEW.body->>'phone','[^0-9]','','g')) NOT BETWEEN 7 AND 15 THEN RAISE EXCEPTION 'Invalid vendor phone'; END IF;
  IF coalesce(NEW.body->>'email','')<>'' AND NEW.body->>'email' !~ '^[^[:space:]@]+@[^[:space:]@]+\.[^[:space:]@]+$' THEN RAISE EXCEPTION 'Invalid vendor email'; END IF;
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER onboarding_guard BEFORE INSERT OR UPDATE ON vendors FOR EACH ROW EXECUTE FUNCTION vendor_onboarding_guard();
-- A vendor awaiting activation must not be used by a direct SQL order insert.
CREATE FUNCTION active_order_vendor() RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER SET search_path=public,pg_temp AS $$ BEGIN
 IF EXISTS(SELECT 1 FROM vendors WHERE id=NEW.vendor_id AND (deleted OR coalesce(body->>'onboardingStatus','Active')<>'Active')) THEN RAISE EXCEPTION 'Select an active vendor'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER active_vendor_link BEFORE INSERT ON work_order_vendors FOR EACH ROW EXECUTE FUNCTION active_order_vendor();
CREATE TRIGGER active_purchase_vendor BEFORE INSERT ON purchase_orders FOR EACH ROW EXECUTE FUNCTION active_order_vendor();
