import http from 'node:http';
import { promises as fs } from 'node:fs';
import path from 'node:path';
import { fileURLToPath,pathToFileURL } from 'node:url';
import { createHash,randomBytes } from 'node:crypto';
import { CATEGORIES,MAX_JSON,SiteError,accountKey,fail,plainObject,processImage,publicPost,resolveIdentity,validatePost,validatePreferences } from './site-backend.mjs';
import { RedXCmsStore } from './redx-cms-store.mjs';
import {readVortexRelease} from './vortex-release.mjs';
import { attachUpgrades,proxyHttp,proxyTarget,requestURL,requireOrigin } from './gateway-proxy.mjs';
const HERE=path.dirname(fileURLToPath(import.meta.url));
export const SITE_FILES=Object.freeze(['app.js','styles.css','catalog.json','public-secrets-docs.json','cli-installation.md','site/api.js','site/auth.js','site/catalog.js','site/dom.js','site/editor.js','site/legal.js','site/pages.js','site/session.js']);
const MIME={'.html':'text/html; charset=utf-8','.js':'text/javascript; charset=utf-8','.css':'text/css; charset=utf-8','.json':'application/json; charset=utf-8','.md':'text/plain; charset=utf-8'};
const DEFAULT_PREFS={theme:'system',reducedMotion:false,emailNotifications:false,browserNotifications:false,timeZone:'UTC',locale:'en'};
const CSP="default-src 'self'; script-src 'self'; style-src 'self' https://fonts.googleapis.com; font-src 'self' https://fonts.gstatic.com; img-src 'self' data: blob:; connect-src 'self'; frame-src 'self'; object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'self'";
function headers(res){res.setHeader('X-Content-Type-Options','nosniff');res.setHeader('Referrer-Policy','strict-origin-when-cross-origin');res.setHeader('X-Frame-Options','SAMEORIGIN');res.setHeader('Content-Security-Policy',CSP);res.setHeader('Permissions-Policy','camera=(self), microphone=(), geolocation=()');res.setHeader('Cache-Control','no-store');}
function json(res,status,value){res.statusCode=status;res.setHeader('Content-Type','application/json; charset=utf-8');res.setHeader('Cache-Control','no-store');res.setHeader('Vary','Cookie');res.end(JSON.stringify(value)+'\n');}
export async function readBody(req,max=MAX_JSON,jsonRequired=true){
  if(jsonRequired&&(req.headers['content-type']||'').split(';',1)[0].trim().toLowerCase()!=='application/json')fail(415,'json_required');
  const length=req.headers['content-length'];if(length!==undefined&&(!/^\d+$/u.test(String(length))||Number(length)>max))fail(413,'body_too_large');
  let total=0;const chunks=[];for await(const chunk of req){const b=Buffer.from(chunk);total+=b.length;if(total>max)fail(413,'body_too_large');chunks.push(b);}
  const bytes=Buffer.concat(chunks);if(!jsonRequired)return bytes;
  try{const value=JSON.parse(bytes.toString('utf8'));if(!plainObject(value))fail(400,'invalid_json');return value;}catch(e){if(e instanceof SiteError)throw e;fail(400,'invalid_json');}
}
async function assetSnapshot(directory){
  const files=new Map(),hash=createHash('sha256');
  for(const name of ['index.html',...SITE_FILES].sort()){
    try{const file=path.join(directory,name),s=await fs.lstat(file);if(!s.isFile()||s.isSymbolicLink()||!s.size||s.size>5*1024*1024)throw Error();const bytes=await fs.readFile(file);files.set(name,bytes);hash.update(name+'\0').update(bytes);}catch{fail(503,'site_assets_unavailable');}
  }
  for(const [name,bytes]of files)if(name.endsWith('.js'))for(const match of bytes.toString('utf8').matchAll(/\b(?:from\s*|import\s*(?:\(\s*)?)(['"])(\.\.?\/[^'"]+)\1/gu)){
    const dependency=path.posix.normalize(path.posix.join(path.posix.dirname(name),match[2]));
    if(!SITE_FILES.includes(dependency))fail(503,'site_assets_unavailable');
  }
  for(const [name,bytes]of files)if(name.endsWith('.json')){try{if(!plainObject(JSON.parse(bytes.toString('utf8'))))throw Error();}catch{fail(503,'site_assets_unavailable');}}
  const html=files.get('index.html').toString('utf8');
  for(const name of ['app.js','styles.css'])if(!['"',"'"].some(quote=>html.includes(quote+'/'+name+quote)))fail(503,'site_assets_unavailable');
  return {revision:hash.digest('hex'),files};
}
function referenced(state,id){return state.posts.some(p=>p.status==='published'&&!p.deletedAt&&p.body.children.some(v=>v.type==='image'&&v.mediaId===id));}
const readState=async store=>typeof store.read==='function'?store.read():store.snapshot();
// Central billing stays unavailable until the real product-specific contracts
// have been reviewed. Environment URLs must not enable a guessed adapter.
function publicConfig(config,store){return {registration:{signupPath:'/signup',activationPath:'/activate',termsPath:'/tos',authority:'existing-site'},capabilities:{cms:!!store,media:!!store&&config.imagesAvailable===true,resendVerification:false,deleteAccount:false,subscriptions:false},downloads:config.downloads||{},socials:config.socials||{},categories:CATEGORIES};}
export async function createSiteHandler(options={}){
  const config={publicOrigin:'https://red-xai.com',staticDirectory:HERE,fetchImpl:fetch,ownerIds:new Set(),now:Date.now,trustCloudflared:false,...options};
  const harness=config.testMode===true&&config.development===true&&/^http:\/\/127\.0\.0\.1(?::\d+)?$/u.test(config.publicOrigin)&&config.fetchImpl!==fetch;
  if(config.testMode||config.development){if(!harness)throw Error('Development requires an explicit loopback fixture authority');}
  else if(config.publicOrigin!=='https://red-xai.com')throw Error('Reviewed public origin required');
  config.ownerIds=config.ownerIds instanceof Set?config.ownerIds:new Set(config.ownerIds);
  const store=config.store||null,revisions=new Map();
  if(store&&!harness&&!(store instanceof RedXCmsStore))throw Error('Production requires native RedX CMS storage');
  const initial=await assetSnapshot(config.staticDirectory);revisions.set(initial.revision,initial.files);
  const identity=req=>resolveIdentity(req.headers,{fetchImpl:config.fetchImpl,ownerIds:config.ownerIds,now:config.now});
  const owner=async req=>{const a=await identity(req);if(!a.owner)fail(403,'owner_required');return a;};
  const cms=()=>{if(!store)fail(503,'cms_not_configured');return store;};
  const handler=async(req,res)=>{
    try{
      if(!['GET','HEAD','POST','PATCH','PUT','DELETE','OPTIONS'].includes(req.method))fail(405,'method_not_allowed');
      const url=requestURL(req.url,config.publicOrigin),p=url.pathname;
      if(!proxyTarget(url))headers(res);
      if(p==='/api/site/health'&&req.method==='GET'){await assetSnapshot(config.staticDirectory);return json(res,200,{ok:true,service:'redxai-site',cmsConfigured:!!store,cmsStorage:store?config.storeMode||'configured-adapter':'unconfigured'});}
      if(p==='/api/site/config'&&req.method==='GET')return json(res,200,publicConfig(config,store));
      if(p==='/api/site/releases/vortex'&&req.method==='GET')return json(res,200,await readVortexRelease(config.fetchImpl));
      if(p==='/api/site/session'&&req.method==='GET'){
        try{const a=await identity(req);return json(res,200,{signedIn:true,owner:a.owner,expiresMs:a.expiresMs,capabilities:publicConfig(config,store).capabilities,account:{id:a.id,username:a.username,displayName:a.displayName,email:a.email,roles:a.roles,amr:a.amr}});}
        catch(e){if(e instanceof SiteError&&e.status===401)return json(res,200,{signedIn:false,owner:false,capabilities:publicConfig(config,store).capabilities});throw e;}
      }
      if(p==='/api/site/posts'&&req.method==='GET'){
        const s=cms();let all=false;
        if(url.searchParams.get('scope')==='owner'){const a=await owner(req);await s.audit('posts.read',a,'all');all=true;}
        const category=url.searchParams.get('category');if(category&&!CATEGORIES.includes(category))fail(400,'invalid_category');
        const state=await readState(s),posts=state.posts.filter(v=>(all||v.status==='published'&&!v.deletedAt)&&(!category||v.category===category)).sort((a,b)=>(b.publishedAt||b.createdAt).localeCompare(a.publishedAt||a.createdAt)).slice(0,300).map(publicPost);
        return json(res,200,{posts});
      }
      const pm=/^\/api\/site\/posts\/(p_[a-f0-9]{32})$/u.exec(p);
      if(p==='/api/site/posts'&&req.method==='POST'){
        requireOrigin(req,config.publicOrigin);const a=await owner(req),s=cms(),input=await readBody(req),id='p_'+randomBytes(16).toString('hex');
        const result=await s.mutate('post.create',a,id,next=>{if(next.posts.length>=5000)fail(409,'post_limit');const clean=validatePost(input,next.media,null,config.now()),stamp=new Date(config.now()).toISOString(),post={...clean,id,createdAt:stamp,updatedAt:stamp,createdBy:accountKey(a.id)};next.posts.push(post);return publicPost(post);});
        return json(res,201,{post:result});
      }
      if(pm&&['PATCH','DELETE'].includes(req.method)){
        requireOrigin(req,config.publicOrigin);const a=await owner(req),s=cms(),input=req.method==='PATCH'?await readBody(req):null;
        const result=await s.mutate(req.method==='DELETE'?'post.delete':'post.update',a,pm[1],next=>{const post=next.posts.find(v=>v.id===pm[1]);if(!post)fail(404,'not_found');const stamp=new Date(config.now()).toISOString();if(req.method==='DELETE'){post.status='deleted';post.deletedAt=stamp;post.updatedAt=stamp;}else{Object.assign(post,validatePost(input,next.media,post,config.now()),{updatedAt:stamp});delete post.deletedAt;}return publicPost(post);});
        return json(res,200,{post:result});
      }
      if(p==='/api/site/media'&&req.method==='POST'){
        requireOrigin(req,config.publicOrigin);const a=await owner(req),s=cms(),type=(req.headers['content-type']||'').split(';',1)[0].toLowerCase(),bytes=await readBody(req,10*1024*1024,false),image=await(config.imageProcessor||processImage)(bytes,type);
        if(!Buffer.isBuffer(image.bytes)||image.bytes.length>10*1024*1024||!Number.isSafeInteger(image.width)||!Number.isSafeInteger(image.height)||image.width<1||image.height<1)fail(415,'invalid_image');
        return json(res,201,{media:await s.addMedia(image.bytes,image,a)});
      }
      const mm=/^\/api\/site\/media\/([a-f0-9]{32})$/u.exec(p);
      if(mm&&['GET','HEAD'].includes(req.method)){
        const s=cms(),state=await readState(s),media=state.media[mm[1]];if(!media)fail(404,'not_found');
        if(!referenced(state,mm[1])){const a=await owner(req);await s.audit('media.read',a,mm[1]);}
        const bytes=await s.mediaBytes(mm[1]);res.statusCode=200;res.setHeader('Content-Type','image/webp');res.setHeader('Content-Length',bytes.length);return res.end(req.method==='HEAD'?undefined:bytes);
      }
      if(p==='/api/site/preferences'&&['GET','PATCH'].includes(req.method)){
        if(req.method==='PATCH')requireOrigin(req,config.publicOrigin);const a=await identity(req),s=cms(),key=accountKey(a.id);
        if(req.method==='GET')return json(res,200,{preferences:{...DEFAULT_PREFS,...(await readState(s)).preferences[key]}});
        const clean=validatePreferences(await readBody(req));return json(res,200,{preferences:await s.mutate('preferences.update',a,'self',next=>{const prefs={...DEFAULT_PREFS,...next.preferences[key],...clean};next.preferences[key]=prefs;return prefs;})});
      }
      if(p==='/api/site/subscriptions'&&req.method==='GET'){
        fail(503,'subscriptions_not_configured');
      }
      if(p==='/api/site/checkout'&&req.method==='POST'){
        requireOrigin(req,config.publicOrigin);await identity(req);fail(503,'subscriptions_not_configured');
      }
      if(['/api/site/account/delete','/api/site/verification/resend'].includes(p)){
        if(!['GET','HEAD','OPTIONS'].includes(req.method))requireOrigin(req,config.publicOrigin);fail(503,p.includes('resend')?'verification_resend_unavailable':'account_deletion_unavailable');
      }
      const target=proxyTarget(url);if(target)return(config.proxy||proxyHttp)(req,res,target,config);
      if(p.startsWith('/api/site/'))fail(404,'not_found');
      if(!['GET','HEAD'].includes(req.method))fail(405,'method_not_allowed');
      const revisionMatch=/^\/site-assets\/([a-f0-9]{64})\/(.+)$/u.exec(p);
      if(revisionMatch||SITE_FILES.includes(p.slice(1))){
        const current=await assetSnapshot(config.staticDirectory);revisions.set(current.revision,current.files);while(revisions.size>3)revisions.delete(revisions.keys().next().value);
        const name=revisionMatch?revisionMatch[2]:p.slice(1),map=revisionMatch?revisions.get(revisionMatch[1]):current.files;
        if(!SITE_FILES.includes(name)||!map?.has(name))fail(404,'not_found');const bytes=map.get(name);res.statusCode=200;res.setHeader('Content-Type',MIME[path.extname(name)]);res.setHeader('Content-Length',bytes.length);return res.end(req.method==='HEAD'?undefined:bytes);
      }
      if(path.extname(p)||p.startsWith('/api/')||p.startsWith('/site-assets/'))fail(404,'not_found');
      const current=await assetSnapshot(config.staticDirectory);revisions.set(current.revision,current.files);while(revisions.size>3)revisions.delete(revisions.keys().next().value);
      let html=current.files.get('index.html').toString('utf8');
      for(const name of ['app.js','styles.css'])for(const quote of ['"',"'"])html=html.replaceAll(quote+'/'+name+quote,quote+'/site-assets/'+current.revision+'/'+name+quote);
      const bytes=Buffer.from(html);res.statusCode=200;res.setHeader('Content-Type','text/html; charset=utf-8');res.setHeader('Content-Length',bytes.length);return res.end(req.method==='HEAD'?undefined:bytes);
    }catch(e){if(res.headersSent){res.destroy?.();return;}headers(res);const error=e instanceof SiteError?e:new SiteError(503,'service_unavailable');return json(res,error.status,{error:error.code});}
  };
  return {handler,config,store};
}
export async function startServer(env=process.env){
  if(env.REDX_SITE_DEV_STATE_DIR||env.REDX_SITE_MODE==='development')throw Error('Production entry point forbids JSON CMS storage; use an explicit loopback fixture harness');
  let store=null,storeMode='unconfigured';
  if(env.REDX_SITE_CMS_DATABASE||env.REDX_SITE_CMS_KEY_FILE){
    store=await new RedXCmsStore({directory:env.REDX_SITE_STATE_DIR,database:env.REDX_SITE_CMS_DATABASE,keyFile:env.REDX_SITE_CMS_KEY_FILE,durability:env.REDX_SITE_CMS_DURABILITY,singleWriter:env.REDX_SITE_CMS_SINGLE_WRITER==='true'}).init();storeMode='native-redx-single-writer';
  }
  let imagesAvailable=false;try{await import('sharp');imagesAvailable=true;}catch{}
  let site;try{site=await createSiteHandler({store,storeMode,imagesAvailable,publicOrigin:'https://red-xai.com',ownerIds:new Set((env.REDX_WEBSITE_OWNER_ACCOUNT_IDS||'').split(',').map(x=>x.trim()).filter(Boolean)),trustCloudflared:env.REDX_SITE_TRUST_CLOUDFLARED==='true'});}catch(e){await store?.close?.();throw e;}
  const server=http.createServer(site.handler);attachUpgrades(server,site.config);server.on('close',()=>store?.close?.());
  try{await new Promise((resolve,reject)=>{server.once('error',reject);server.listen(14210,'127.0.0.1',resolve);});return server;}catch(e){await store?.close?.();throw e;}
}
if(process.argv[1]&&pathToFileURL(path.resolve(process.argv[1])).href===import.meta.url)startServer().then(()=>process.stdout.write('Red-XAI website gateway listening on 127.0.0.1:14210\n')).catch(()=>{process.stderr.write('Website gateway startup failed; verify private configuration.\n');process.exitCode=1;});
