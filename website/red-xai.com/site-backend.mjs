import { createHash, randomBytes } from 'node:crypto';
import { promises as fs, constants as fsConstants } from 'node:fs';
import path from 'node:path';

export const CATEGORIES = Object.freeze(['announcements','updates','maintenance','videos','podcasts','videos-podcasts','recommended-brands','partnerships']);
export const MFA_METHODS = new Set(['otp','totp','sms','call','mfa','webauthn']);
export const AUTHORITY = 'http://127.0.0.1:4200/api/accounts/me';
export const MAX_JSON = 512 * 1024;
export class SiteError extends Error {
  constructor(status, code) { super(code); this.status = status; this.code = code; }
}
export const fail = (status, code) => { throw new SiteError(status, code); };
export const accountKey = id => createHash('sha256').update(id).digest('hex');
export const plainObject = value => value !== null && typeof value === 'object' && !Array.isArray(value) && [Object.prototype,null].includes(Object.getPrototypeOf(value));
export function text(value, max, required = false) {
  if (typeof value !== 'string' || value.length > max || /[\u0000-\u0008\u000b\u000c\u000e-\u001f\u007f]/u.test(value)) fail(400,'invalid_text');
  const out = value.trim();
  if (required && !out) fail(400,'required_text');
  return out;
}
export function sessionCookie(headers) {
  const source = headers.get ? headers.get('cookie') : headers.cookie;
  if (source && (typeof source !== 'string' || source.length > 16384)) fail(401,'signed_out');
  const selected = (source || '').split(';').map(x => x.trim()).filter(x => x.startsWith('redx_session='));
  if (!selected.length) return '';
  if (selected.length !== 1 || selected[0].length <= 13 || selected[0].length > 8192 || /[\r\n\u0000-\u0020\u007f]/u.test(selected[0])) fail(401,'signed_out');
  return selected[0];
}
export async function boundedJSON(response, max = 32768) {
  if ((response.headers.get('content-type') || '').split(';',1)[0].trim().toLowerCase()!=='application/json') fail(503,'authority_unavailable');
  const chunks = []; let total = 0;
  if (response.body) for await (const chunk of response.body) {
    const data = Buffer.from(chunk); total += data.length;
    if (total > max) { try { await response.body.cancel(); } catch {} fail(503,'authority_unavailable'); }
    chunks.push(data);
  }
  try { return JSON.parse(Buffer.concat(chunks).toString('utf8')); } catch { fail(503,'authority_unavailable'); }
}
export function canonicalIdentity(json, { ownerIds = new Set(), now = Date.now() } = {}) {
  const a = json?.account;
  if (!plainObject(a) || typeof a.id !== 'string' || !a.id || a.id.length > 256 || /[\u0000-\u0020\u007f]/u.test(a.id)
    || typeof a.email !== 'string' || a.email.length > 254 || !/^[^\s@]+@[^\s@]+\.[^\s@]+$/u.test(a.email)
    || !Number.isSafeInteger(json.expiresMs) || json.expiresMs <= now
    || !Array.isArray(json.amr) || !json.amr.length || json.amr.some(v => typeof v !== 'string' || !v || v.length > 40)
    || json.active === false || a.active === false || a.disabled === true || a.banned === true || a.deleted === true
    || a.emailVerified === false || a.email_verified === false
    || (a.status !== undefined && !['active','activated'].includes(a.status))
    || (a.state !== undefined && !['active','activated'].includes(a.state))) fail(401,'signed_out');
  const roles = Array.isArray(a.roles) ? a.roles.filter(x => typeof x === 'string' && /^[a-z][a-z0-9_-]{0,39}$/u.test(x)).slice(0,20) : [];
  const owner = ownerIds.has(a.id) && roles.includes('owner') && json.amr.some(x => MFA_METHODS.has(x));
  if (!owner && ['owner','__unassigned__'].includes(a.id)) fail(401,'signed_out');
  return Object.freeze({ id:a.id, email:a.email.toLowerCase(), username:typeof a.username === 'string' ? a.username.slice(0,120) : '',
    displayName:typeof a.name === 'string' ? a.name.slice(0,120) : typeof a.username === 'string' ? a.username.slice(0,120) : a.email,
    roles, amr:[...json.amr], owner, expiresMs:json.expiresMs });
}
export async function resolveIdentity(headers, { fetchImpl = fetch, ownerIds = new Set(), now = Date.now } = {}) {
  const cookie = sessionCookie(headers);
  if (!cookie) fail(401,'signed_out');
  let response;
  try {
    response = await fetchImpl(AUTHORITY, { method:'GET', headers:{accept:'application/json',cookie}, redirect:'manual',
      cache:'no-store', signal:AbortSignal.timeout(5000) });
  } catch { fail(503,'authority_unavailable'); }
  if (response.status === 401) fail(401,'signed_out');
  if (response.status !== 200) fail(503,'authority_unavailable');
  try{return canonicalIdentity(await boundedJSON(response), {ownerIds,now:now()});}catch(e){if(e instanceof SiteError)throw e;fail(503,'authority_unavailable');}
}
const escapeHtml = value => String(value).replace(/[&<>"']/gu, c => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
export function safeLink(value) {
  const href = text(value,2048,true);
  if (href.startsWith('/') && !href.startsWith('//') && !/[\\\r\n]/u.test(href)) return href;
  let u; try { u = new URL(href); } catch { fail(400,'invalid_link'); }
  if (!['http:','https:'].includes(u.protocol) || u.username || u.password) fail(400,'invalid_link');
  return u.href;
}
export function normalizeDocument(value, media = {}) {
  if (!plainObject(value) || value.type !== 'doc' || !Array.isArray(value.children) || value.children.length > 200) fail(400,'invalid_document');
  let nodes = 0, characters = 0, images = 0;
  const budget = () => { if (++nodes > 2000 || characters > 100000) fail(400,'document_too_large'); };
  const cleanText = v => { text(v,100000); characters += v.length; budget(); return v; };
  const inline = (values, depth = 0) => {
    if (!Array.isArray(values) || values.length > 500 || depth > 2) fail(400,'invalid_inline');
    return values.map(v => {
      budget(); if (!plainObject(v)) fail(400,'invalid_inline');
      if (v.type === 'text') {
        if (typeof v.text !== 'string' || v.text.length > 100000 || /[\u0000-\u0008\u000b\u000c\u000e-\u001f\u007f]/u.test(v.text)) fail(400,'invalid_text');
        characters += v.text.length; budget();
        const marks = v.marks === undefined ? [] : v.marks;
        if (!Array.isArray(marks) || marks.length > 5 || marks.some(x => !['bold','italic','underline','strike','code'].includes(x))) fail(400,'invalid_marks');
        return {type:'text',text:v.text,marks:[...new Set(marks)]};
      }
      if (v.type === 'link' && depth === 0) return {type:'link',href:safeLink(v.href),children:inline(v.children,depth+1)};
      fail(400,'invalid_inline');
    });
  };
  const children = value.children.map(v => {
    budget(); if (!plainObject(v)) fail(400,'invalid_block');
    if (['paragraph','heading','blockquote'].includes(v.type)) {
      const out = {type:v.type,children:inline(v.children)};
      if (v.type === 'heading') { if (![2,3,4].includes(v.level)) fail(400,'invalid_heading'); out.level=v.level; }
      return out;
    }
    if (v.type === 'codeBlock') return {type:'codeBlock',text:cleanText(v.text)};
    if (v.type === 'list') {
      if (typeof v.ordered !== 'boolean' || !Array.isArray(v.items) || v.items.length > 100) fail(400,'invalid_list');
      return {type:'list',ordered:v.ordered,items:v.items.map(x => inline(x))};
    }
    if (v.type === 'image') {
      if (++images > 20 || typeof v.mediaId !== 'string' || !/^[a-f0-9]{32}$/u.test(v.mediaId) || !media[v.mediaId]) fail(400,'invalid_image');
      return {type:'image',mediaId:v.mediaId,alt:text(v.alt || '',300),caption:text(v.caption || '',500)};
    }
    fail(400,'invalid_block');
  });
  budget();
  return {type:'doc',children};
}
export function documentHtml(doc) {
  const renderInline = values => values.map(v => {
    if (v.type === 'link') return '<a href="' + escapeHtml(v.href) + '" rel="noopener noreferrer">' + renderInline(v.children) + '</a>';
    let out = escapeHtml(v.text).replace(/\n/gu,'<br>');
    for (const mark of v.marks || []) { const tag = {bold:'strong',italic:'em',underline:'u',strike:'s',code:'code'}[mark]; out = '<'+tag+'>'+out+'</'+tag+'>'; }
    return out;
  }).join('');
  return doc.children.map(v => {
    if (v.type === 'paragraph') return '<p>'+renderInline(v.children)+'</p>';
    if (v.type === 'heading') return '<h'+v.level+'>'+renderInline(v.children)+'</h'+v.level+'>';
    if (v.type === 'blockquote') return '<blockquote>'+renderInline(v.children)+'</blockquote>';
    if (v.type === 'codeBlock') return '<pre><code>'+escapeHtml(v.text)+'</code></pre>';
    if (v.type === 'list') { const tag=v.ordered?'ol':'ul'; return '<'+tag+'>'+v.items.map(x=>'<li>'+renderInline(x)+'</li>').join('')+'</'+tag+'>'; }
    if (v.type === 'image') return '<figure><img src="/api/site/media/'+v.mediaId+'" alt="'+escapeHtml(v.alt)+'" loading="lazy">'+(v.caption?'<figcaption>'+escapeHtml(v.caption)+'</figcaption>':'')+'</figure>';
    return '';
  }).join('');
}
export function validatePost(input, media, existing = null, now = Date.now()) {
  if (!plainObject(input)) fail(400,'invalid_post');
  const allowed = new Set(['title','category','body','status','startsAt','endsAt']);
  if (Object.keys(input).some(x => !allowed.has(x))) fail(400,'invalid_post_field');
  const merged = {...(existing || {}),...input};
  const title = text(merged.title,180,true);
  if (!CATEGORIES.includes(merged.category) || !['draft','published'].includes(merged.status || 'draft')) fail(400,'invalid_post');
  const body = normalizeDocument(merged.body,media);
  const content = v => v.type === 'image' || (typeof v.text === 'string' && !!v.text.trim()) || (v.children || []).some(content) || (v.items || []).some(items=>items.some(content));
  if(!body.children.some(content))fail(400,'body_required');
  const out = {title,category:merged.category,body,status:merged.status || 'draft'};
  for (const key of ['startsAt','endsAt']) {
    const value = merged[key];
    if (value === undefined || value === null || value === '') continue;
    if (typeof value !== 'string' || value.length > 40 || !/^\d{4}-\d{2}-\d{2}T/u.test(value) || !Number.isFinite(Date.parse(value))) fail(400,'invalid_schedule');
    out[key] = new Date(value).toISOString();
  }
  if (out.startsAt && out.endsAt && Date.parse(out.endsAt) <= Date.parse(out.startsAt)) fail(400,'invalid_schedule');
  if (out.status === 'published') out.publishedAt = existing?.publishedAt || new Date(now).toISOString();
  return out;
}
export const publicPost = post => ({id:post.id,title:post.title,category:post.category,body:post.body,html:documentHtml(post.body),status:post.status,
  createdAt:post.createdAt,updatedAt:post.updatedAt,publishedAt:post.publishedAt || null,startsAt:post.startsAt || null,endsAt:post.endsAt || null,deletedAt:post.deletedAt || null});
export function validatePreferences(input) {
  if (!plainObject(input)) fail(400,'invalid_preferences');
  const out = {};
  for (const [key,value] of Object.entries(input)) {
    if (key === 'theme') { if (!['system','dark','light'].includes(value)) fail(400,'invalid_preferences'); out[key]=value; }
    else if (['reducedMotion','emailNotifications','browserNotifications'].includes(key)) { if (typeof value !== 'boolean') fail(400,'invalid_preferences'); out[key]=value; }
    else if (key === 'timeZone') { try { new Intl.DateTimeFormat('en',{timeZone:value}); } catch { fail(400,'invalid_preferences'); } out[key]=text(value,80,true); }
    else if (key === 'locale') { try { new Intl.DateTimeFormat(value); } catch { fail(400,'invalid_preferences'); } out[key]=text(value,40,true); }
    else fail(400,'invalid_preferences');
  }
  return out;
}
const initialState = () => ({schema:1,posts:[],media:{},preferences:{}});
async function syncSupportDirectory(directory) {
  if(process.platform==='win32')return; // Native Linux durability is checked separately.
  const fd=await fs.open(directory,'r');try{await fd.sync();}finally{await fd.close();}
}
export class PrivateStore {
  constructor(directory, { testMode = false, now = Date.now } = {}) { this.directory=path.resolve(directory); this.testMode=testMode; this.now=now; this.state=null; this.queue=Promise.resolve(); }
  async init() {
    if(!this.testMode)throw new Error('JSON CMS storage requires an explicit local fixture harness');
    await fs.mkdir(this.directory,{recursive:true,mode:0o700});
    const stat=await fs.lstat(this.directory);
    if (!stat.isDirectory() || stat.isSymbolicLink() || (!this.testMode && process.platform !== 'win32' && (stat.mode & 0o077))) throw new Error('Private state directory required');
    await fs.mkdir(path.join(this.directory,'media'),{mode:0o700}).catch(e=>{if(e.code!=='EEXIST')throw e;});
    const mediaStat=await fs.lstat(path.join(this.directory,'media'));
    if(!mediaStat.isDirectory()||mediaStat.isSymbolicLink())throw new Error('Invalid media directory');
    const stateFile=path.join(this.directory,'state.json');
    let data;
    try { const s=await fs.lstat(stateFile); if(!s.isFile()||s.isSymbolicLink()||s.size>32*1024*1024||(!this.testMode&&process.platform!=='win32'&&(s.mode&0o077)))throw new Error('Invalid state file'); data=JSON.parse(await fs.readFile(stateFile,'utf8')); }
    catch(e) { if(e.code!=='ENOENT')throw new Error('Invalid persisted state'); data=initialState(); }
    if(data?.schema!==1||!Array.isArray(data.posts)||!plainObject(data.media)||!plainObject(data.preferences))throw new Error('Invalid persisted state');
    for(const p of data.posts) {
      if(!plainObject(p)||!/^p_[a-f0-9]{32}$/u.test(p.id)||!['draft','published','deleted'].includes(p.status))throw new Error('Invalid persisted post');
      try { const cleaned=validatePost({...Object.fromEntries(['title','category','body','startsAt','endsAt'].filter(k=>p[k]!==undefined).map(k=>[k,p[k]])),status:p.status==='deleted'?'draft':p.status},data.media,p,this.now()); p.body=cleaned.body; }
      catch { throw new Error('Invalid persisted post'); }
    }
    this.state=data;
    return this;
  }
  snapshot() { if(!this.state)throw new Error('Store not initialized'); return structuredClone(this.state); }
  async audit(event, identity, target = '') {
    const file=path.join(this.directory,'audit.jsonl');
    const fd=await fs.open(file,fsConstants.O_WRONLY|fsConstants.O_CREAT|fsConstants.O_APPEND|(fsConstants.O_NOFOLLOW || 0),0o600);
    try { await fd.writeFile(JSON.stringify({at:new Date(this.now()).toISOString(),event,actor:accountKey(identity.id),target})+'\n'); await fd.sync(); } finally { await fd.close(); }
    await syncSupportDirectory(this.directory);
  }
  async mutate(event, identity, target, change) {
    const task=this.queue.then(async()=>{
      await this.audit(event+'.attempt',identity,target);
      const next=this.snapshot(); const result=await change(next);
      const file=path.join(this.directory,'state.json'),tmp=path.join(this.directory,'.state-'+randomBytes(12).toString('hex')+'.tmp');
      let fd;
      try {
        fd=await fs.open(tmp,'wx',0o600); await fd.writeFile(JSON.stringify(next)+'\n'); await fd.sync(); await fd.close(); fd=null;
        await fs.rename(tmp,file);
        if(process.platform!=='win32'){const d=await fs.open(this.directory,'r');try{await d.sync();}finally{await d.close();}}
        this.state=next;
      } catch(e) { if(fd)await fd.close().catch(()=>{}); await fs.unlink(tmp).catch(()=>{}); throw e; }
      await this.audit(event+'.committed',identity,target);
      return result;
    });
    this.queue=task.catch(()=>{});
    return task;
  }
  async addMedia(bytes, info, identity) {
    const id=randomBytes(16).toString('hex'),file=path.join(this.directory,'media',id+'.webp');
    const fd=await fs.open(file,'wx',0o600);
    try { await fd.writeFile(bytes); await fd.sync(); } finally { await fd.close(); }
    await syncSupportDirectory(path.join(this.directory,'media'));
    const metadata={id,contentType:'image/webp',bytes:bytes.length,width:info.width,height:info.height,createdAt:new Date(this.now()).toISOString()};
    await this.mutate('media.create',identity,id,next=>{next.media[id]=metadata;});
    return {...metadata,url:'/api/site/media/'+id};
  }
  async mediaBytes(id) {
    const file=path.join(this.directory,'media',id+'.webp'),st=await fs.lstat(file);
    if(!st.isFile()||st.isSymbolicLink()||st.size>10*1024*1024)fail(404,'not_found');
    return fs.readFile(file);
  }
}
export function imageKind(bytes) {
  if(bytes.length>=8 && bytes.subarray(0,8).equals(Buffer.from([137,80,78,71,13,10,26,10])))return 'image/png';
  if(bytes.length>=3 && bytes[0]===255&&bytes[1]===216&&bytes[2]===255)return 'image/jpeg';
  if(bytes.length>=12&&bytes.subarray(0,4).toString()==='RIFF'&&bytes.subarray(8,12).toString()==='WEBP')return 'image/webp';
  if(bytes.length>=6&&['GIF87a','GIF89a'].includes(bytes.subarray(0,6).toString()))return 'image/gif';
  return '';
}
export async function processImage(bytes, declaredType) {
  if (!['image/png','image/jpeg','image/webp','image/gif'].includes(declaredType) || imageKind(bytes)!==declaredType) fail(415,'unsupported_image');
  let sharp; try { sharp=(await import('sharp')).default; } catch { fail(503,'image_processing_unavailable'); }
  try {
    const {data,info}=await sharp(bytes,{limitInputPixels:16777216,failOn:'warning',animated:false}).rotate().webp({quality:88}).toBuffer({resolveWithObject:true});
    if(!info.width||!info.height||data.length>10*1024*1024)fail(413,'image_too_large');
    return {bytes:data,width:info.width,height:info.height};
  } catch(e) { if(e instanceof SiteError)throw e; fail(415,'invalid_image'); }
}
