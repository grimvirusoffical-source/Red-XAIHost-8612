import {boundedJSON,fail,plainObject} from './site-backend.mjs';
export const VORTEX_RELEASE_SOURCE='http://127.0.0.1:14202/host/api/releases/vortex';
const ROOT='https://github.com/grimvirusoffical-source/red-xai-vortex/releases/';
function releaseURL(value,download=false){
  if(typeof value!=='string'||value.length>2048)fail(503,'vortex_release_unavailable');
  let u;try{u=new URL(value);}catch{fail(503,'vortex_release_unavailable');}
  if(u.username||u.password||u.search||u.hash||!u.href.startsWith(ROOT)||(download&&!u.pathname.startsWith('/grimvirusoffical-source/red-xai-vortex/releases/download/')))fail(503,'vortex_release_unavailable');
  return u.href;
}
export async function readVortexRelease(fetchImpl=fetch){
  let response;try{response=await fetchImpl(VORTEX_RELEASE_SOURCE,{method:'GET',headers:{accept:'application/json'},redirect:'manual',cache:'no-store',signal:AbortSignal.timeout(10000)});}catch{fail(503,'vortex_release_unavailable');}
  if(response.status!==200)fail(503,'vortex_release_unavailable');
  const value=await boundedJSON(response,256*1024);
  if(!plainObject(value)||typeof value.version!=='string'||!value.version||value.version.length>100||!Array.isArray(value.assets)||value.assets.length>100)fail(503,'vortex_release_unavailable');
  return {version:value.version,publishedAt:typeof value.publishedAt==='string'?value.publishedAt.slice(0,100):'',releaseUrl:releaseURL(value.releaseUrl),assets:value.assets.map(a=>{
    if(!plainObject(a)||typeof a.name!=='string'||!a.name||a.name.length>200||/[\u0000-\u001f\u007f]/u.test(a.name)||!Number.isSafeInteger(a.size)||a.size<0)fail(503,'vortex_release_unavailable');
    return {name:a.name,url:releaseURL(a.url,true),size:a.size};
  })};
}
