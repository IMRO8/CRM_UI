import {seed} from '../src/domain.js';import {writeFileSync} from 'node:fs';
// Historical demo seed helper: vendor definitions are introduced by migration 003.
const s=seed();s.forms=s.forms.filter(f=>f.id!=="vendor");const q=x=>"'"+String(x).replaceAll("'","''")+"'",j=x=>q(JSON.stringify(x))+'::jsonb';
const lines=["SELECT set_config('app.user_id','u1',true);"];
for(const u of s.users)lines.push(`INSERT INTO users VALUES(${q(u.id)},${q(u.name)},${q(u.title)},${!!u.superuser},crypt('BuildLedger-demo-2026!',gen_salt('bf')));`);
for(const t of s.tenants)lines.push(`INSERT INTO tenants VALUES(${q(t.id)},${q(t.code)},${j(t)});`);
for(const r of s.roles)lines.push(`INSERT INTO roles VALUES(${q(r.id)},${j(r)});`);
for(const m of s.memberships){lines.push(`INSERT INTO memberships VALUES(${q(m.tenantId)},${q(m.userId)});`);for(const r of m.roleIds)lines.push(`INSERT INTO membership_roles VALUES(${q(m.tenantId)},${q(m.userId)},${q(r)});`);}
for(const f of s.forms)lines.push(`INSERT INTO form_versions VALUES(${q(f.id)},${f.version},${j(f)});`);
for(const r of s.reras)lines.push(`INSERT INTO rera_accounts VALUES(${q(r.id)},${q(r.tenantId)},${q(r.number)},${j(r)});`);
for(const o of s.orders)lines.push(`INSERT INTO work_orders VALUES(${q(o.id)},${q(o.tenantId)},${q(o.number)},${o.base},${q(o.formId)},${o.formVersion},${q(o.createdBy)},${j(o)});`);
for(const a of s.amendments)lines.push(`INSERT INTO amendments VALUES(${q(a.id)},${q(a.orderId)},${a.delta},${q(a.createdBy)},NULL,${j(a)});`);
for(const e of s.events)lines.push(`INSERT INTO amendment_events VALUES(${q(e.id)},${q(e.amendmentId)},${q(e.action)},${q(e.stage??'')},${q(e.actorId)},${j(e)});`);
for(const a of s.audit)lines.push(`INSERT INTO audit_log VALUES(${q(a.id)},${q(a.tenantId)},${q(a.actorId)},${j(a)});`);
writeFileSync('migrations/002_demo_seed.sql',lines.join('\n')+'\n');
