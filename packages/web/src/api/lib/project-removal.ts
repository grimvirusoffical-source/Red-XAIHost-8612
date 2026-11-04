import {ORPCError} from '@orpc/server';
import {storageClient} from '../database/__client';
import {currentTenantId} from './tenant-context';
import {newId,nowSeconds} from './ids';

export const REMOVING_PROJECT_STATUS='removing';
export const PROJECT_REMOVAL_SCOPE='project_removal';
export const REMOVAL_CANCEL_REASON='Project removal requested.';
export const REMOVAL_CLAIM_SECONDS=90;
export function safeRemovalSlug(value:unknown):value is string {return typeof value==='string'&&/^[a-z0-9][a-z0-9_-]{0,127}$/i.test(value);}

// Required nodes survive cancellation and retries in the existing job ledger.
const TARGETS=`WITH scope(tenant_id,project_id) AS (VALUES (?,?)), target_nodes AS (
 SELECT p.node_id FROM projects p JOIN scope s ON p.tenant_id=s.tenant_id AND p.id=s.project_id WHERE p.node_id IS NOT NULL
 UNION SELECT r.node_id FROM project_replicas r JOIN scope s ON r.tenant_id=s.tenant_id AND r.project_id=s.project_id
 UNION SELECT d.node_id FROM deployments d JOIN scope s ON d.tenant_id=s.tenant_id AND d.project_id=s.project_id
 WHERE d.node_id IS NOT NULL AND ((d.action='remove' AND d.trigger='delete') OR d.status IN ('queued','claimed','building','running') OR (d.status='cancelled' AND d.error='${REMOVAL_CANCEL_REASON}'))
)`;
const READY=`p.status='${REMOVING_PROJECT_STATUS}' AND NOT EXISTS (
 SELECT 1 FROM target_nodes t WHERE NOT EXISTS (
  SELECT 1 FROM deployments d WHERE d.tenant_id=p.tenant_id AND d.project_id=p.id AND d.node_id=t.node_id AND d.action='remove' AND d.trigger='delete' AND d.status='succeeded'
 ))`;

/** Reserve the slug/ID before purging atomically. Old delayed workers can never
 * tear down a later project at that slug. Audit metadata has identifiers only. */
export async function finalizeProjectRemoval(projectId:string):Promise<boolean> {
  const tenantId=currentTenantId(),now=nowSeconds(),tombstone='removed_'+projectId;
  const result=await storageClient.batch([
    {sql:`${TARGETS} INSERT OR IGNORE INTO activity(id,tenant_id,scope,ref_id,message,level,meta,created_at)
      SELECT 'removed_'||p.id,p.tenant_id,'${PROJECT_REMOVAL_SCOPE}',p.id,'Workloads removed; project deleted.','success',json_object('version',1,'projectId',p.id,'slug',p.slug),?
      FROM projects p JOIN scope s ON p.tenant_id=s.tenant_id AND p.id=s.project_id WHERE ${READY}`,args:[tenantId,projectId,now]},
    {sql:`DELETE FROM projects WHERE tenant_id=? AND id=? AND status='${REMOVING_PROJECT_STATUS}' AND EXISTS (SELECT 1 FROM activity WHERE tenant_id=? AND id=? AND scope='${PROJECT_REMOVAL_SCOPE}' AND ref_id=? AND json_valid(meta) AND json_extract(meta,'$.version')=1 AND json_extract(meta,'$.projectId')=projects.id AND json_extract(meta,'$.slug')=projects.slug)`,args:[tenantId,projectId,tenantId,tombstone,projectId]},
    {sql:`DELETE FROM project_replicas WHERE tenant_id=? AND project_id=? AND NOT EXISTS (SELECT 1 FROM projects WHERE tenant_id=? AND id=?) AND EXISTS (SELECT 1 FROM activity WHERE tenant_id=? AND id=? AND scope='${PROJECT_REMOVAL_SCOPE}')`,args:[tenantId,projectId,tenantId,projectId,tenantId,tombstone]},
    {sql:`UPDATE domains SET project_id=NULL WHERE tenant_id=? AND project_id=? AND NOT EXISTS (SELECT 1 FROM projects WHERE tenant_id=? AND id=?) AND EXISTS (SELECT 1 FROM activity WHERE tenant_id=? AND id=? AND scope='${PROJECT_REMOVAL_SCOPE}')`,args:[tenantId,projectId,tenantId,projectId,tenantId,tombstone]},
  ],'write');
  return result[1]!.rowsAffected===1;
}

export async function requestProjectRemoval(projectId:string):Promise<{found:boolean;pending:boolean;name?:string}> {
  const tenantId=currentTenantId(),now=nowSeconds();
  const existing=await storageClient.execute({sql:'SELECT id,name,slug FROM projects WHERE tenant_id=? AND id=?',args:[tenantId,projectId]});
  if(!existing.rows.length)return {found:false,pending:false};
  if(!safeRemovalSlug(existing.rows[0]!.slug))throw new ORPCError('CONFLICT',{message:'This legacy project identifier requires review before worker teardown.'});
  const invalid=await storageClient.execute({sql:`${TARGETS} SELECT 1 FROM target_nodes t
    LEFT JOIN nodes n ON n.id=t.node_id AND n.tenant_id=(SELECT tenant_id FROM scope)
    WHERE n.id IS NULL AND NOT EXISTS (SELECT 1 FROM deployments d JOIN scope s ON d.tenant_id=s.tenant_id AND d.project_id=s.project_id WHERE d.node_id=t.node_id AND d.action='remove' AND d.trigger='delete' AND d.status='succeeded') LIMIT 1`,args:[tenantId,projectId]});
  if(invalid.rows.length)throw new ORPCError('CONFLICT',{message:'A referenced node is no longer in this workspace. Reconcile its workloads before removing this project.'});
  // Intent, cancellation and all currently owned teardown targets are one write
  // batch. Failed attempts retain their history and retry with a new job ID.
  await storageClient.batch([
    {sql:`UPDATE projects SET status='${REMOVING_PROJECT_STATUS}',auto_deploy=0,updated_at=? WHERE tenant_id=? AND id=?`,args:[now,tenantId,projectId]},
    {sql:`UPDATE deployments SET status='cancelled',error=?,finished_at=? WHERE tenant_id=? AND project_id=? AND action<>'remove' AND status IN ('queued','claimed','building','running')`,args:[REMOVAL_CANCEL_REASON,now,tenantId,projectId]},
    {sql:`${TARGETS} INSERT INTO deployments(id,tenant_id,project_id,node_id,status,action,trigger,created_at)
      SELECT 'dep_'||lower(hex(randomblob(12))),s.tenant_id,s.project_id,t.node_id,'queued','remove','delete',?
      FROM target_nodes t CROSS JOIN scope s JOIN nodes n ON n.id=t.node_id AND n.tenant_id=s.tenant_id
      WHERE EXISTS (SELECT 1 FROM projects p WHERE p.id=s.project_id AND p.tenant_id=s.tenant_id AND p.status='${REMOVING_PROJECT_STATUS}')
      AND NOT EXISTS (SELECT 1 FROM deployments d WHERE d.tenant_id=s.tenant_id AND d.project_id=s.project_id AND d.node_id=t.node_id AND d.action='remove' AND d.trigger='delete' AND d.status IN ('queued','claimed','building','running','succeeded'))`,args:[tenantId,projectId,now]},
  ],'write');
  const complete=await finalizeProjectRemoval(projectId);
  return {found:true,pending:!complete,name:String(existing.rows[0]!.name)};
}

/** Removal is idempotent and its slug stays reserved. Expired deliveries retry
 * after lost responses/reconnects. Busy upgraded workers retain active leases. */
export async function requeueNodeRemovals(nodeId:string,options:{restart?:boolean;busy?:boolean}={}):Promise<void> {
  const tenantId=currentTenantId(),cutoff=nowSeconds()-REMOVAL_CLAIM_SECONDS;
  await storageClient.execute({sql:`UPDATE deployments SET status='queued',started_at=NULL,finished_at=NULL,error=NULL
    WHERE tenant_id=? AND node_id=? AND action='remove' AND trigger='delete' AND status IN ('claimed','building','running')
    AND (?=1 OR (?=1 AND (started_at IS NULL OR started_at<=?)))
    AND EXISTS (SELECT 1 FROM projects p WHERE p.id=deployments.project_id AND p.tenant_id=deployments.tenant_id AND p.status='${REMOVING_PROJECT_STATUS}')`,args:[tenantId,nodeId,options.restart?1:0,options.busy===true?0:1,cutoff]});
}

/** Retain these reservations with audit history; no application API edits them. */
export async function projectRemovalReservations():Promise<{ids:Set<string>;slugs:Set<string>}> {
  const rows=await storageClient.execute(`SELECT id,ref_id,meta FROM activity WHERE scope='${PROJECT_REMOVAL_SCOPE}'`);
  const ids=new Set<string>(),slugs=new Set<string>();
  for(const row of rows.rows) {
    let value;try {value=JSON.parse(String(row.meta));}catch {throw new Error('Project removal reservation metadata refused');}
    if(value?.version!==1||typeof value.projectId!=='string'||value.projectId!==row.ref_id||row.id!=='removed_'+value.projectId||!safeRemovalSlug(value.slug))throw new Error('Project removal reservation metadata refused');
    ids.add(value.projectId);slugs.add(value.slug);
  }
  return {ids,slugs};
}

/** A late deployment callback must not recreate a replica after removal starts. */
export async function upsertMutableReplica(projectId:string,nodeId:string,patch:{status:string;lastDeploymentId?:string|null;lastError?:string|null;lastDeployedAt?:number|null}):Promise<string|null> {
  const tenantId=currentTenantId();
  const found=await storageClient.execute({sql:'SELECT id,last_deployment_id,last_deployed_at FROM project_replicas WHERE tenant_id=? AND project_id=? AND node_id=?',args:[tenantId,projectId,nodeId]});
  const previous=found.rows[0];
  const row=await storageClient.execute({sql:`INSERT INTO project_replicas(id,tenant_id,project_id,node_id,status,last_deployment_id,last_error,last_deployed_at,updated_at)
    SELECT ?,?,?,?,?,?,?,?,? WHERE EXISTS (SELECT 1 FROM projects WHERE tenant_id=? AND id=? AND status<>'${REMOVING_PROJECT_STATUS}') AND EXISTS (SELECT 1 FROM nodes WHERE tenant_id=? AND id=?)
    ON CONFLICT(project_id,node_id) DO UPDATE SET status=excluded.status,last_deployment_id=excluded.last_deployment_id,last_error=excluded.last_error,last_deployed_at=excluded.last_deployed_at,updated_at=excluded.updated_at
    WHERE project_replicas.tenant_id=excluded.tenant_id AND EXISTS (SELECT 1 FROM projects WHERE tenant_id=? AND id=? AND status<>'${REMOVING_PROJECT_STATUS}') RETURNING id`,
    args:[typeof previous?.id==='string'?previous.id:newId('rep'),tenantId,projectId,nodeId,patch.status,patch.lastDeploymentId??previous?.last_deployment_id??null,patch.lastError??null,patch.lastDeployedAt??previous?.last_deployed_at??null,nowSeconds(),tenantId,projectId,tenantId,nodeId,tenantId,projectId]});
  return row.rows.length?String(row.rows[0]!.id):null;
}
