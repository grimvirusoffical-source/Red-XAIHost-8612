import fs from 'node:fs';
import {fileURLToPath} from 'node:url';
import {parsePrivateKey,makeClient} from './apple_metadata.mjs';

const TARGETS={'com.redxai.database':'6816596433','com.redxai.host':'6816600008'};
export function blankFieldPatch(current,desired) {
  const patch={},preserved=[];
  for(const [field,value] of Object.entries(desired)) {
    if(typeof value!=='string'||!value.trim()) throw new Error('INVALID_DRAFT_FIELD');
    if(current[field]===value) continue;
    if(current[field]===null||current[field]===undefined||current[field]==='') patch[field]=value;
    else preserved.push(field);
  }
  return {patch,preserved};
}
export function validateDraft(draft) {
  if(!TARGETS[draft.bundleId]||draft.locale!=='en-US') throw new Error('UNAPPROVED_TARGET');
  for(const [field,maximum] of Object.entries({name:30,subtitle:30,description:4000,promotionalText:170,keywords:100,betaDescription:4000})) {
    if(typeof draft[field]!=='string'||draft[field].length<1||draft[field].length>maximum) throw new Error('DRAFT_LENGTH_INVALID');
  }
  if(Buffer.byteLength(draft.keywords)>100) throw new Error('KEYWORDS_TOO_LONG');
}
export async function applyListings(request,drafts) {
  if(!Array.isArray(drafts)||drafts.length!==2||new Set(drafts.map(d=>d.bundleId)).size!==2) throw new Error('EXPECTED_TWO_APPS');
  drafts.forEach(validateDraft);
  const report={operation:'fill-empty-preview-text-fields',apps:[],untouched:['screenshots','app icons','pricing','privacy declarations','age ratings','review submission','copyright','support and privacy URLs']};
  async function api(path,params={},method='GET',body=null) {
    const result=await request(path,params,method,body);
    if(!result.ok) throw new Error(`APPLE_HTTP_${result.status}_${(result.codes||[]).join('_')}`);
    return result.body;
  }
  async function update(resource,type,desired,entry) {
    const {patch,preserved}=blankFieldPatch(resource.attributes||{},desired);
    if(preserved.length) entry.preserved.push({type,id:resource.id,fields:preserved});
    if(!Object.keys(patch).length) return;
    const path=`/v1/${type}/${resource.id}`;
    await api(path,{},'PATCH',{data:{type,id:resource.id,attributes:patch}});
    const checked=await api(path);
    for(const [key,value] of Object.entries(patch)) {
      if(checked.data?.attributes?.[key]!==value) throw new Error('METADATA_READBACK_MISMATCH');
    }
    entry.updated.push({type,id:resource.id,fields:Object.keys(patch),verified:true});
  }
  for(const draft of drafts) {
    const id=TARGETS[draft.bundleId];
    const entry={bundleId:draft.bundleId,id,updated:[],preserved:[],errors:[]};
    report.apps.push(entry);
    try {
      const app=(await api(`/v1/apps/${id}`,{'fields[apps]':'name,bundleId,primaryLocale'})).data;
      if(app?.attributes?.bundleId!==draft.bundleId) throw new Error('APP_IDENTITY_MISMATCH');
      const infos=(await api(`/v1/apps/${id}/appInfos`,{limit:10})).data||[];
      const editable=infos.filter(x=>(x.attributes.state||x.attributes.appStoreState)==='PREPARE_FOR_SUBMISSION');
      if(editable.length!==1) throw new Error('APP_INFO_NOT_UNIQUELY_EDITABLE');
      const infoLocs=(await api(`/v1/appInfos/${editable[0].id}/appInfoLocalizations`,{limit:20})).data||[];
      const loc=infoLocs.find(x=>x.attributes?.locale===draft.locale);
      if(!loc) throw new Error('APP_INFO_LOCALE_MISSING');
      await update(loc,'appInfoLocalizations',{subtitle:draft.subtitle},entry);
      const versions=(await api(`/v1/apps/${id}/appStoreVersions`,{'filter[platform]':'IOS',limit:10})).data||[];
      const version=versions.find(v=>v.attributes.versionString==='1.0'&&(v.attributes.appVersionState||v.attributes.appStoreState)==='PREPARE_FOR_SUBMISSION');
      if(!version) throw new Error('EXPECTED_EDITABLE_IOS_VERSION_MISSING');
      const versionLocs=(await api(`/v1/appStoreVersions/${version.id}/appStoreVersionLocalizations`,{limit:20})).data||[];
      const versionLoc=versionLocs.find(x=>x.attributes?.locale===draft.locale);
      if(!versionLoc) throw new Error('VERSION_LOCALE_MISSING');
      await update(versionLoc,'appStoreVersionLocalizations',{description:draft.description,keywords:draft.keywords,promotionalText:draft.promotionalText},entry);
      const betaLocs=(await api(`/v1/apps/${id}/betaAppLocalizations`,{limit:20})).data||[];
      const beta=betaLocs.find(x=>x.attributes?.locale===draft.locale);
      const desired={description:draft.betaDescription,feedbackEmail:'redxdevelopment1998@gmail.com'};
      if(beta) await update(beta,'betaAppLocalizations',desired,entry);
      else {
        const created=await api('/v1/betaAppLocalizations',{},'POST',{data:{type:'betaAppLocalizations',attributes:{locale:draft.locale,...desired},relationships:{app:{data:{type:'apps',id}}}}});
        const checked=await api(`/v1/apps/${id}/betaAppLocalizations`,{limit:20});
        const saved=(checked.data||[]).find(x=>x.id===created.data?.id&&x.attributes?.locale===draft.locale);
        if(!saved||saved.attributes.description!==desired.description||saved.attributes.feedbackEmail!==desired.feedbackEmail) throw new Error('BETA_READBACK_MISMATCH');
        entry.updated.push({type:'betaAppLocalizations',id:saved.id,fields:['locale',...Object.keys(desired)],verified:true});
      }
    } catch(error) {
      const message=String(error.message||'UNKNOWN_ERROR');
      entry.errors.push(/^[A-Z0-9_.-]+$/.test(message)?message:'METADATA_OPERATION_FAILED');
    }
  }
  return report;
}
async function main() {
  try {
    const draft=JSON.parse(fs.readFileSync('store/ios/metadata-draft.json','utf8'));
    const key=parsePrivateKey(process.env.APPLE_PRIVATE_INPUT||'');
    if(!key) throw new Error('APPLE_PRIVATE_INPUT_INVALID');
    const request=makeClient({key,keyID:'8L986HWCXB',individual:false,includeScope:false});
    const report=await applyListings(request,draft.apps);
    console.log(JSON.stringify(report,null,2));
    if(process.env.GITHUB_STEP_SUMMARY) {
      const lines=['## Apple listing text verification',...report.apps.map(a=>`${a.bundleId}: ${a.updated.length} verified resource updates; ${a.errors.length} errors; ${a.preserved.length} existing-field groups preserved.`),'No release or review was submitted.'];
      fs.appendFileSync(process.env.GITHUB_STEP_SUMMARY,lines.join('\n')+'\n');
    }
    if(report.apps.some(a=>a.errors.length)) process.exitCode=1;
  } catch { console.error('Listing setup failed before completion; no private values are logged.');process.exitCode=1; }
}
if(process.argv[1]&&fileURLToPath(import.meta.url)===process.argv[1]) await main();
