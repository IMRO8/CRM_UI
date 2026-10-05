-- Upgrade only approval routing metadata. Completed approvals, amounts and events
-- retain their original policy. Existing pending documents adopt the new rule.
ALTER TABLE amendments ADD COLUMN approval_policy integer NOT NULL DEFAULT 1 CHECK(approval_policy IN(1,2));
ALTER TABLE amendments ADD COLUMN creator_superuser boolean NOT NULL DEFAULT false;
ALTER TABLE purchase_orders ADD COLUMN approval_policy integer NOT NULL DEFAULT 1 CHECK(approval_policy IN(1,2));
ALTER TABLE purchase_orders ADD COLUMN creator_superuser boolean NOT NULL DEFAULT false;
ALTER TABLE amendments DISABLE TRIGGER immutable_history;
UPDATE amendments a SET creator_superuser=u.superuser,approval_policy=CASE WHEN EXISTS(SELECT 1 FROM amendment_events e WHERE e.amendment_id=a.id AND (e.action='reject' OR (e.action='approve' AND e.stage='finance'))) THEN 1 ELSE 2 END FROM users u WHERE u.id=a.created_by;
ALTER TABLE amendments ENABLE TRIGGER immutable_history;
ALTER TABLE purchase_orders DISABLE TRIGGER po_draft;
ALTER TABLE purchase_orders DISABLE TRIGGER po_date;
UPDATE purchase_orders p SET creator_superuser=u.superuser,approval_policy=CASE WHEN EXISTS(SELECT 1 FROM purchase_events e WHERE e.purchase_order_id=p.id AND (e.action='reject' OR (e.action='approve' AND e.stage='finance'))) THEN 1 ELSE 2 END FROM users u WHERE u.id=p.created_by;
ALTER TABLE purchase_orders ENABLE TRIGGER po_draft;
ALTER TABLE purchase_orders ENABLE TRIGGER po_date;
ALTER TABLE amendments ALTER COLUMN approval_policy SET DEFAULT 2;
ALTER TABLE purchase_orders ALTER COLUMN approval_policy SET DEFAULT 2;
ALTER TABLE amendment_events DROP CONSTRAINT amendment_events_stage_check;
ALTER TABLE amendment_events ADD CHECK(stage IN('','manager','admin','finance','superuser'));
ALTER TABLE purchase_events DROP CONSTRAINT purchase_events_stage_check;
ALTER TABLE purchase_events ADD CHECK(stage IN('','manager','admin','finance','superuser'));
-- A previous-policy manager approval remains in history but does not prevent
-- that person from giving one approval under the new policy.
DROP INDEX distinct_approvers;
CREATE UNIQUE INDEX distinct_approvers ON amendment_events(amendment_id,actor_id) WHERE action='approve' AND stage<>'manager';
DROP INDEX po_distinct_approvers;
CREATE UNIQUE INDEX po_distinct_approvers ON purchase_events(purchase_order_id,actor_id) WHERE action='approve' AND stage<>'manager';
INSERT INTO roles(id,body) SELECT 'admin',jsonb_set(body || '{"id":"admin","name":"Admin","stage":"admin"}'::jsonb,'{permissions,work_order,fields,amount}','"edit"') FROM (SELECT coalesce((SELECT body FROM roles WHERE id='manager'),(SELECT body FROM roles WHERE id='finance'),'{"permissions":{"work_order":{"actions":["view","review","approve","reject"],"fields":{"amount":"edit"}},"purchase_order":{"actions":["view","review","approve","reject"],"fields":{"amount":"view","description":"view","materials":"view","order_date":"view"}}}}'::jsonb) body) defaults ON CONFLICT(id) DO NOTHING;

CREATE FUNCTION approval_document_metadata() RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER SET search_path=public,pg_temp AS $$ BEGIN
 IF TG_OP='INSERT' THEN
  NEW.approval_policy=2;
  SELECT superuser INTO NEW.creator_superuser FROM users WHERE id=NEW.created_by;
 ELSIF NEW.approval_policy IS DISTINCT FROM OLD.approval_policy OR NEW.creator_superuser IS DISTINCT FROM OLD.creator_superuser THEN
  RAISE EXCEPTION 'Approval policy and creator status are immutable';
 END IF;
 NEW.body=NEW.body || jsonb_build_object('id',NEW.id,'createdBy',NEW.created_by,'approvalPolicy',NEW.approval_policy,'creatorSuperuser',NEW.creator_superuser);
 RETURN NEW;
END $$;
CREATE TRIGGER approval_metadata BEFORE INSERT ON amendments FOR EACH ROW EXECUTE FUNCTION approval_document_metadata();
CREATE TRIGGER approval_metadata BEFORE INSERT OR UPDATE ON purchase_orders FOR EACH ROW EXECUTE FUNCTION approval_document_metadata();

CREATE FUNCTION approval_complete(policy integer,own boolean,events jsonb) RETURNS boolean LANGUAGE sql IMMUTABLE AS $$
 SELECT NOT EXISTS(SELECT 1 FROM jsonb_array_elements(events) e WHERE e->>'action'='reject') AND
 CASE WHEN policy=1 THEN EXISTS(SELECT 1 FROM jsonb_array_elements(events) e WHERE e->>'action'='approve' AND e->>'stage'='finance')
 ELSE EXISTS(SELECT 1 FROM jsonb_array_elements(events) su WHERE su->>'action'='approve' AND su->>'stage'='superuser' AND
 (own OR EXISTS(SELECT 1 FROM jsonb_array_elements(events) partner WHERE partner->>'action'='approve' AND partner->>'stage' IN('admin','finance') AND partner->>'actorId'<>su->>'actorId'))) END
$$;
CREATE FUNCTION amendment_approved(aid text) RETURNS boolean LANGUAGE sql STABLE AS $$
 SELECT approval_complete(a.approval_policy,a.creator_superuser,coalesce((SELECT jsonb_agg(jsonb_build_object('action',e.action,'stage',e.stage,'actorId',e.actor_id)) FROM amendment_events e WHERE e.amendment_id=a.id),'[]'::jsonb)) FROM amendments a WHERE a.id=aid
$$;
CREATE OR REPLACE FUNCTION effective_total(wo text) RETURNS bigint LANGUAGE sql STABLE AS $$
 SELECT w.base+coalesce((SELECT sum(a.delta) FROM amendments a WHERE a.order_id=wo AND amendment_approved(a.id)),0)::bigint FROM work_orders w WHERE w.id=wo
$$;
CREATE OR REPLACE FUNCTION purchase_approved(p text) RETURNS boolean LANGUAGE sql STABLE AS $$
 SELECT approval_complete(po.approval_policy,po.creator_superuser,coalesce((SELECT jsonb_agg(jsonb_build_object('action',e.action,'stage',e.stage,'actorId',e.actor_id)) FROM purchase_events e WHERE e.purchase_order_id=po.id),'[]'::jsonb)) FROM purchase_orders po WHERE po.id=p
$$;
CREATE FUNCTION project_stage_action(t text,st text,f text) RETURNS boolean LANGUAGE sql STABLE SECURITY DEFINER SET search_path=public,pg_temp AS $$
 SELECT EXISTS(SELECT 1 FROM membership_roles m JOIN roles r ON r.id=m.role_id WHERE m.tenant_id=t AND m.user_id=current_actor() AND r.body->>'stage'=st AND (r.body->'permissions'->f->'actions') ? 'approve')
$$;
CREATE FUNCTION validate_approval_step(policy integer,own boolean,creator text,t text,f text,events jsonb,act text,st text,actor text) RETURNS void LANGUAGE plpgsql AS $$ BEGIN
 IF actor IS DISTINCT FROM current_actor() OR NOT has_tenant(t) OR NOT project_form_action(t,f,act) THEN RAISE EXCEPTION 'Financial action permission is required'; END IF;
 IF EXISTS(SELECT 1 FROM tenants WHERE id=t AND deleted) THEN RAISE EXCEPTION 'Project is deleted'; END IF;
 IF policy=1 OR approval_complete(policy,own,events) OR EXISTS(SELECT 1 FROM jsonb_array_elements(events) e WHERE e->>'action'='reject') THEN RAISE EXCEPTION 'Document is already finalized'; END IF;
 IF act='submit' THEN
  IF jsonb_array_length(events)>0 THEN RAISE EXCEPTION 'Only drafts can be submitted'; END IF;
  IF actor<>creator AND NOT is_platform_admin() THEN RAISE EXCEPTION 'Only the creator can submit'; END IF;
  RETURN;
 END IF;
 IF actor=creator AND NOT is_platform_admin() THEN RAISE EXCEPTION 'Creator cannot review, approve or reject own submission'; END IF;
 IF NOT EXISTS(SELECT 1 FROM jsonb_array_elements(events) e WHERE e->>'action'='submit') THEN RAISE EXCEPTION 'Submission is required'; END IF;
 IF act='approve' THEN
  IF EXISTS(SELECT 1 FROM jsonb_array_elements(events) e WHERE e->>'action'='approve' AND e->>'stage'<>'manager' AND e->>'actorId'=actor) THEN RAISE EXCEPTION 'Two different approvers are required'; END IF;
  IF own THEN
   IF st<>'superuser' OR NOT is_platform_admin() THEN RAISE EXCEPTION 'Superuser submissions require only superuser approval'; END IF;
  ELSE
   IF NOT EXISTS(SELECT 1 FROM jsonb_array_elements(events) e WHERE e->>'action'='review') THEN RAISE EXCEPTION 'Review is required'; END IF;
   IF st='superuser' THEN
    IF NOT is_platform_admin() THEN RAISE EXCEPTION 'Superuser approval is required'; END IF;
   ELSIF st IN('admin','finance') THEN
    IF is_platform_admin() OR NOT project_stage_action(t,st,f) THEN RAISE EXCEPTION 'Admin or Finance approval stage permission is required'; END IF;
    IF EXISTS(SELECT 1 FROM jsonb_array_elements(events) e WHERE e->>'action'='approve' AND e->>'stage' IN('admin','finance')) THEN RAISE EXCEPTION 'Admin or Finance partner approval is already recorded'; END IF;
   ELSE RAISE EXCEPTION 'Approval requires Superuser plus an Admin or Finance approver';
   END IF;
  END IF;
 END IF;
END $$;
CREATE OR REPLACE FUNCTION enforce_workflow() RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER SET search_path=public,pg_temp AS $$
DECLARE a amendments%ROWTYPE; w work_orders%ROWTYPE; ev jsonb; total bigint;
BEGIN
 SELECT * INTO a FROM amendments WHERE id=NEW.amendment_id;
 SELECT * INTO w FROM work_orders WHERE id=a.order_id FOR UPDATE;
 SELECT coalesce(jsonb_agg(jsonb_build_object('action',action,'stage',stage,'actorId',actor_id)),'[]'::jsonb) INTO ev FROM amendment_events WHERE amendment_id=a.id;
 PERFORM validate_approval_step(a.approval_policy,a.creator_superuser,a.created_by,w.tenant_id,w.form_id,ev,NEW.action,NEW.stage,NEW.actor_id);
 IF NEW.action='approve' THEN
  SELECT effective_total(a.order_id)+a.delta INTO total;
  IF total<0 OR total>100000000000000 THEN RAISE EXCEPTION 'Approval blocked: resulting total is outside the allowed range'; END IF;
 END IF;
 NEW.body=NEW.body || jsonb_build_object('id',NEW.id,'amendmentId',NEW.amendment_id,'action',NEW.action,'stage',NEW.stage,'actorId',NEW.actor_id,'superuserApproval',is_platform_admin() AND NEW.action='approve');
 RETURN NEW;
END $$;
CREATE OR REPLACE FUNCTION purchase_workflow_guard() RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER SET search_path=public,pg_temp AS $$
DECLARE p purchase_orders%ROWTYPE; ev jsonb;
BEGIN
 SELECT * INTO p FROM purchase_orders WHERE id=NEW.purchase_order_id FOR UPDATE;
 SELECT coalesce(jsonb_agg(jsonb_build_object('action',action,'stage',stage,'actorId',actor_id)),'[]'::jsonb) INTO ev FROM purchase_events WHERE purchase_order_id=p.id;
 PERFORM validate_approval_step(p.approval_policy,p.creator_superuser,p.created_by,p.tenant_id,p.form_id,ev,NEW.action,NEW.stage,NEW.actor_id);
 NEW.body=NEW.body || jsonb_build_object('id',NEW.id,'purchaseOrderId',NEW.purchase_order_id,'action',NEW.action,'stage',NEW.stage,'actorId',NEW.actor_id,'superuserApproval',is_platform_admin() AND NEW.action='approve');
 RETURN NEW;
END $$;
