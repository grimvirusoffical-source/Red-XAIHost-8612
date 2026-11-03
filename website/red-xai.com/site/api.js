export class ApiError extends Error {
  constructor(status, data = {}) { super(typeof data.message === 'string' ? data.message : messages[data.error] || 'The request could not be completed.'); this.status = status; this.code = data.error || 'request_failed'; this.data = data; }
}
const messages = {
  signed_out:'Your website session has ended. Sign in again.', authority_unavailable:'The account service is temporarily unavailable. Please retry.',
  forbidden:'This operation requires the site owner’s authenticated account.', same_origin_required:'Reload this page and try again.',
  verification_resend_unavailable:'Verification email resend is not available through this service yet. Contact support for help.',
  account_delete_unavailable:'Automatic account deletion is not available through this service yet. Contact support for help.',
  image_processing_unavailable:'Image processing is temporarily unavailable. Your post has not been published.',
  invalid_image:'That image could not be read. Try a JPEG, PNG, WebP or GIF.', unsupported_image:'Choose a JPEG, PNG, WebP or GIF.',
  image_too_large:'The image exceeds the supported size or dimensions.', owner_required:'Only the site owner can publish content.',
};
export async function request(path, { method = 'GET', json, body, headers = {}, signal, primary = false } = {}) {
  if (typeof path !== 'string' || /[\\\u0000-\u0020\u007f]/u.test(path) || !(path.startsWith('/api/site/') || path.startsWith('/api/accounts/'))) throw new TypeError('A same-origin API path is required');
  const canonical=new URL(path,'https://red-xai.com');
  if(canonical.origin!=='https://red-xai.com'||!(canonical.pathname.startsWith('/api/site/')||canonical.pathname.startsWith('/api/accounts/')))throw new TypeError('A same-origin API path is required');
  const options = { method, credentials:'same-origin', cache:'no-store', redirect:'error', headers:{accept:'application/json',...headers}, signal:signal || AbortSignal.timeout(15000) };
  if (json !== undefined) { options.headers['content-type']='application/json'; options.body=JSON.stringify(json); }
  else if (body !== undefined) options.body=body;
  let response; try { response=await fetch(path,options); } catch (e) { if (e.name==='AbortError') throw e; throw new ApiError(0,{message:'The service could not be reached. Check your connection and retry.'}); }
  if(primary&&response.status===204)return {status:204,json:{}};
  let data; try { data=await response.json(); } catch { throw new ApiError(response.status,{message:'The service returned an unreadable response. Please retry.'}); }
  if (primary) return {status:response.status,json:data};
  if (!response.ok) throw new ApiError(response.status,data);
  return data;
}
export const primary = (path, json, token) => request('/api/accounts/'+path, {method:'POST',json,primary:true,headers:token?{'x-redx-flow':token}:{}});
export const assetURL = name => new URL('../'+name, import.meta.url).href;
export async function assetJSON(name) { const response = await fetch(assetURL(name),{cache:'no-cache',credentials:'same-origin',redirect:'error'}); if (!response.ok) throw new Error('Documentation is temporarily unavailable.'); return response.json(); }
export async function assetText(name) { const response = await fetch(assetURL(name),{cache:'no-cache',credentials:'same-origin',redirect:'error'}); if (!response.ok) throw new Error('Documentation is temporarily unavailable.'); return response.text(); }

export function loginOutcome(status, json) {
  if (!json || typeof json !== 'object' || Array.isArray(json)) return 'error';
  if (status !== 200) return /(?:activat|verif[^.]{0,30}email|email[^.]{0,30}verif)/iu.test(String(json.error||'')+' '+String(json.message||'')) ? 'verification' : 'error';
  if (json.next === '2fa' && typeof json.token === 'string' && json.token) return 'challenge';
  if (json.next === '2fa_setup' && typeof json.token === 'string' && json.token) return 'setup';
  if ((json.next === undefined || json.next === '') && json.token === undefined && json.challenge === undefined && json.error === undefined && (json.pending === undefined || Array.isArray(json.pending) && json.pending.length === 0)) return 'complete';
  return 'error';
}
