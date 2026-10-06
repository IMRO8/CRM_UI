import {PGlite} from '@electric-sql/pglite';
import {pgcrypto} from '@electric-sql/pglite/contrib/pgcrypto';
import {readFileSync,readdirSync} from 'node:fs';
import assert from 'node:assert/strict';
import {test} from 'node:test';
test('PostgreSQL enforces direct superuser approval and both required pairs after migration',async()=>{
 const db=new PGlite({extensions:{pgcrypto}});await db.waitReady;
 const root=new URL('../migrations/',import.meta.url);
 for(const file of readdirSync(root).filter(f=>f.endsWith('.sql')).sort())await db.exec('BEGIN;'+readFileSync(new URL(file,root),'utf8')+'COMMIT;');
 const q=sql=>db.query(sql),exec=sql=>db.exec(sql);
 assert.equal((await q("SELECT effective_total('w1') total")).rows[0].total,140000000);
 assert.deepEqual((await q("SELECT approval_policy FROM amendments ORDER BY id")).rows.map(r=>r.approval_policy),[1,2,2]);
 assert.equal((await q("SELECT body->>'stage' stage FROM roles WHERE id='admin'")).rows[0].stage,'admin');
 await exec("CREATE ROLE ledger_app;GRANT USAGE ON SCHEMA public TO ledger_app;GRANT SELECT,INSERT,UPDATE ON ALL TABLES IN SCHEMA public TO ledger_app;GRANT DELETE ON membership_roles TO ledger_app;SET ROLE ledger_app;SELECT set_config('app.user_id','u1',false);");
 // Already pending superuser amendment finishes without another review/approval.
 await exec("INSERT INTO amendment_events VALUES('su-a3','a3','approve','superuser','u1','{}');");
 assert.equal((await q("SELECT effective_total('w3') total")).rows[0].total,30000000);
 await assert.rejects(exec("INSERT INTO amendment_events VALUES('again-a3','a3','approve','finance','u1','{}')"),/finalized/);
 // Drafts still need submission; the superuser's explicit approval is not automatic.
 await assert.rejects(exec("INSERT INTO purchase_events VALUES('draft-fast','po1','approve','superuser','u1','{}')"),/Submission/);
 await exec("INSERT INTO purchase_events VALUES('submit-po1','po1','submit','','u1','{}');");
 assert.equal((await q("SELECT purchase_approved('po1') ok")).rows[0].ok,false);
 await exec("SELECT set_config('app.user_id','u3',false);");
 await assert.rejects(exec("INSERT INTO purchase_events VALUES('finance-own-po','po1','approve','finance','u3','{}')"),/Superuser submissions/);
 await exec("SELECT set_config('app.user_id','u1',false);INSERT INTO purchase_events VALUES('su-po1','po1','approve','superuser','u1','{}');");
 assert.equal((await q("SELECT purchase_approved('po1') ok")).rows[0].ok,true);
 await exec("INSERT INTO stock_lots VALUES('direct-lot','po1','t1','pl1','{}');");
 await assert.rejects(exec("INSERT INTO stock_lots VALUES('rental-lot','po1','t1','pl2','{}')"),/purchased materials/);
 // Assign the new Admin role separately, leaving Project Manager intact.
 await exec("INSERT INTO membership_roles VALUES('t1','u2','admin');");
 const amendment=async(id,creator='u4',delta=100)=>{
  await exec(`SELECT set_config('app.user_id','${creator}',false);INSERT INTO amendments(id,order_id,delta,created_by,body) VALUES('${id}','w1',${delta},'${creator}','{"approvalPolicy":1,"creatorSuperuser":true}');INSERT INTO amendment_events VALUES('${id}-submit','${id}','submit','','${creator}','{}');`);
  assert.equal((await q(`SELECT creator_superuser FROM amendments WHERE id='${id}'`)).rows[0].creator_superuser,false);
  await exec(`SELECT set_config('app.user_id','u2',false);INSERT INTO amendment_events VALUES('${id}-review','${id}','review','','u2','{}');`);
 };
 // Give creator u4 financial command permissions for these isolated tests.
 await exec("INSERT INTO membership_roles VALUES('t1','u4','manager');");
 for(const [id,partner,suFirst] of [['admin-pair','admin',false],['admin-reverse','admin',true],['finance-pair','finance',false],['finance-reverse','finance',true]]){
  await amendment(id);
  const other=partner==='admin'?'u2':'u3';
  for(const [actor,stage] of (suFirst?[['u1','superuser'],[other,partner]]:[[other,partner],['u1','superuser']])){
   await exec(`SELECT set_config('app.user_id','${actor}',false);INSERT INTO amendment_events VALUES('${id}-${stage}','${id}','approve','${stage}','${actor}','{}');`);
   if(stage===(suFirst?'superuser':partner))assert.equal((await q(`SELECT amendment_approved('${id}') ok`)).rows[0].ok,false);
  }
  assert.equal((await q(`SELECT amendment_approved('${id}') ok`)).rows[0].ok,true);
 }
 await amendment('missing-su');
 await exec("SELECT set_config('app.user_id','u2',false);INSERT INTO amendment_events VALUES('missing-admin','missing-su','approve','admin','u2','{}');SELECT set_config('app.user_id','u3',false);");
 await assert.rejects(exec("INSERT INTO amendment_events VALUES('missing-finance','missing-su','approve','finance','u3','{}')"),/already recorded/);
 assert.equal((await q("SELECT amendment_approved('missing-su') ok")).rows[0].ok,false);
 await exec("SELECT set_config('app.user_id','u2',false);");
 await assert.rejects(exec("INSERT INTO amendment_events VALUES('repeat-admin','missing-su','approve','admin','u2','{}')"),/different approvers/);
 await exec("SELECT set_config('app.user_id','u4',false);");
 await assert.rejects(exec("INSERT INTO amendment_events VALUES('creator-approve','missing-su','approve','manager','u4','{}')"),/Creator/);
 await exec("SELECT set_config('app.user_id','u3',false);");
 await assert.rejects(exec("INSERT INTO amendment_events VALUES('fake-su','missing-su','approve','superuser','u3','{}')"),/Superuser approval/);
 // Approval roles are project-specific; a manager does not inherit Admin.
 await exec("SELECT set_config('app.user_id','u1',false);DELETE FROM membership_roles WHERE tenant_id='t1' AND user_id='u2' AND role_id='admin';");
 await amendment('manager-not-admin');
 await exec("SELECT set_config('app.user_id','u2',false);");
 await assert.rejects(exec("INSERT INTO amendment_events VALUES('manager-invalid','manager-not-admin','approve','admin','u2','{}')"),/stage permission/);
 await exec("SELECT set_config('app.user_id','u1',false);");
 assert.equal((await q("UPDATE amendments SET approval_policy=1 WHERE id='missing-su' RETURNING id")).rows.length,0);
 assert.equal((await q("SELECT approval_policy FROM amendments WHERE id='missing-su'")).rows[0].approval_policy,2);
 await exec("INSERT INTO membership_roles VALUES('t1','u2','admin');");
 // A submitted regular PO stays excluded after Finance, until Superuser approves.
 await exec("SELECT set_config('app.user_id','u2',false);INSERT INTO purchase_orders(id,tenant_id,number,total,vendor_id,form_version,created_by,body) SELECT 'regular-po','t1','PO-REGULAR',total,vendor_id,form_version,'u2',body || jsonb_build_object('id','regular-po','createdBy','u2','orderId','w1','purpose','Tower A reinforcement','lines',(SELECT jsonb_agg(value||jsonb_build_object('purpose','Tower A concrete works')) FROM jsonb_array_elements(body->'lines'))) FROM purchase_orders WHERE id='po1';INSERT INTO purchase_events VALUES('rp-submit','regular-po','submit','','u2','{}');SELECT set_config('app.user_id','u3',false);INSERT INTO purchase_events VALUES('rp-review','regular-po','review','','u3','{}'),('rp-finance','regular-po','approve','finance','u3','{}');");
 assert.equal((await q("SELECT purchase_approved('regular-po') ok")).rows[0].ok,false);
 await exec("SELECT set_config('app.user_id','u1',false);INSERT INTO purchase_events VALUES('rp-su','regular-po','approve','superuser','u1','{}');");
 assert.equal((await q("SELECT purchase_approved('regular-po') ok")).rows[0].ok,true);
 await exec("SELECT set_config('app.user_id','u1',false);INSERT INTO amendments(id,order_id,delta,created_by,body) VALUES('too-negative','w1',-200000000,'u1','{}');INSERT INTO amendment_events VALUES('negative-submit','too-negative','submit','','u1','{}');");
 await assert.rejects(exec("INSERT INTO amendment_events VALUES('negative-su','too-negative','approve','superuser','u1','{}')"),/outside the allowed range/);
 await assert.rejects(exec("UPDATE purchase_orders SET approval_policy=1 WHERE id='regular-po'"),/immutable/);
 console.log('PASS: direct approval, Admin/Superuser and Finance/Superuser in either order, creator exclusions, forged metadata, legacy totals and inventory eligibility.');
 await db.close();
});
