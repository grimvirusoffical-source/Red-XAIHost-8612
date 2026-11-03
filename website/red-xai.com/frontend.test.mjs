import test from 'node:test';
import assert from 'node:assert/strict';
import {readFile,readdir} from 'node:fs/promises';
import {safeHref,safeNext,detectPlatform} from './site/dom.js';
import {loginOutcome,request,ApiError} from './site/api.js';
import {normalizeSession,identityKey,usesPrimaryPage,isPrivatePath} from './site/session.js';
import {filterProducts,priceValue,priceLabel,platformOptions,productPlatforms,storeEmptyState} from './site/catalog.js';
import {serializeBody,bodyHasContent,renderBody,validMediaId} from './site/editor.js';
import {normalizeDocument} from './site-backend.mjs';
import {legalPage,contactPage,cloudPcPage,vortexPage,workspacePage,EXISTING_TOOLS} from './site/pages.js';
import {CATEGORIES} from './site/editor.js';

test('normal password completion requires a canonical session; setup/challenges never imply completion',()=>{
  assert.equal(loginOutcome(200,{}),'complete');
  assert.equal(loginOutcome(200,{next:'2fa',token:'flow'}),'challenge');
  assert.equal(loginOutcome(200,{next:'2fa_setup',token:'flow'}),'setup');
  for(const data of [{next:'2fa_setup'},{token:'flow'},{pending:['totp']},{next:'unexpected'},{error:'wrong'},null])assert.equal(loginOutcome(200,data),'error');
  assert.equal(loginOutcome(403,{error:'email_activation_required'}),'verification');
  assert.equal(loginOutcome(401,{message:'Invalid username or password'}),'error');
});
test('session identity comes from the canonical ID; names/emails do not grant owner',()=>{
  const a={signedIn:true,owner:false,expiresMs:Date.now()+100000,account:{id:'account-A',email:'owner@example.test',roles:['owner'],username:'owner'}};
  assert.equal(normalizeSession(a).owner,false);
  const b={...a,account:{...a.account,id:'account-B'}};
  assert.notEqual(identityKey(a),identityKey(b));
  assert.notEqual(identityKey(a),identityKey({...a,owner:true}));
  for(const bad of [{signedIn:true},{...a,expiresMs:0},{...a,account:{email:'x'}},{}])assert.throws(()=>normalizeSession(bad));
  assert.deepEqual(normalizeSession({signedIn:false,owner:true}).owner,false);
});
test('login continuation rejects external/loop targets and preserves native Quick Login',()=>{
  for(const value of ['https://evil.test','//evil.test','/\\evil.test','/signin','/api/accounts/me','/signup','/products/evil\n'])assert.equal(safeNext(value),'/');
  for(const value of ['/db','/host','/products/software','/redxai-connect?request=opaque','/redxai-connectx?code=opaque'])assert.equal(safeNext(value),value);
  for(const path of ['/signup','/activate','/tos','/redxai-connect','/redxai-connectx/request'])assert.equal(usesPrimaryPage(path),true);
  assert.equal(usesPrimaryPage('/signin'),false);assert.equal(isPrivatePath('/db'),true);
});
test('links reject executable schemes and platform detection distinguishes mobile',()=>{
  for(const href of ['javascript:alert(1)','data:text/html,evil','//evil.test','https://user:pass@example.test','https://example.test/ x','/\\evil'])assert.equal(safeHref(href),'#');
  assert.equal(safeHref('/contact'),'/contact');assert.equal(safeHref('https://example.test'),'https://example.test/');
  assert.equal(detectPlatform({userAgent:'Mozilla iPhone Mac OS X'}),'mobile');assert.equal(detectPlatform({platform:'MacIntel'}),'macos');assert.equal(detectPlatform({platform:'Win32'}),'windows');assert.equal(detectPlatform({platform:'Linux x86_64'}),'linux');
});
test('catalog never treats unannounced prices as free and orders unknown prices last',()=>{
  const items=[{category:'software',name:'Soon',price:{kind:'unannounced'}},{category:'software',name:'Paid',price:{kind:'paid',amount:19,currency:'USD'}},{category:'cli',name:'CLI',price:{kind:'free'}}];
  assert.equal(priceValue(items[0]),null);assert.equal(priceLabel(items[0]),'Price to be announced');
  assert.deepEqual(filterProducts(items,{price:'free'}).map(x=>x.name),['CLI']);
  assert.deepEqual(filterProducts(items,{sort:'price-low'}).map(x=>x.name),['CLI','Paid','Soon']);
  assert.deepEqual(filterProducts(items,{sort:'price-high'}).map(x=>x.name),['Paid','CLI','Soon']);
  assert.deepEqual(filterProducts(items,{category:'software',min:'1'}).map(x=>x.name),['Paid']);
});
test('platform metadata composes with price/range filters and keeps unspecified platforms honest',()=>{
  const items=[{category:'software',name:'Desktop free',platforms:['Windows',' macOS '],price:{kind:'free'}},{category:'software',name:'Windows paid',platforms:['Windows'],price:{kind:'paid',amount:20}},{category:'software',name:'Mac soon',platforms:['macOS'],price:{kind:'unannounced'}},{category:'software',name:'Unknown paid',platforms:['To be announced'],price:{kind:'paid',amount:5}},{category:'cli',name:'CLI',platforms:['Linux'],price:{kind:'free'}}];
  assert.deepEqual(filterProducts(items,{category:'software',platform:'windows',price:'free'}).map(p=>p.name),['Desktop free']);
  assert.deepEqual(filterProducts(items,{platform:'Windows',price:'paid',min:'10',max:'30'}).map(p=>p.name),['Windows paid']);
  assert.deepEqual(filterProducts(items,{platform:'macos',price:'unannounced'}).map(p=>p.name),['Mac soon']);
  assert.deepEqual(filterProducts(items,{platform:'unknown',price:'paid'}).map(p=>p.name),['Unknown paid']);
  assert.deepEqual(productPlatforms({}),[]);assert.deepEqual(platformOptions(items),[['linux','Linux'],['macos','macOS'],['windows','Windows']]);
  assert.deepEqual(filterProducts(items,{category:'games',platform:'windows',price:'free'}),[]);
  assert.equal(storeEmptyState('games',0).title,'Nothing listed yet');assert.equal(storeEmptyState('software',items.length).title,'No matching products');
});
test('API client sends same-origin cookies and does not follow redirects or accept out-of-scope paths',async()=>{
  const prior=globalThis.fetch;const calls=[];globalThis.fetch=async(path,options)=>{calls.push([path,options]);return {ok:true,status:200,json:async()=>({ok:true})};};
  try{await request('/api/site/preferences',{method:'PATCH',json:{theme:'dark'}});assert.equal(calls[0][1].credentials,'same-origin');assert.equal(calls[0][1].redirect,'error');assert.equal(calls[0][1].cache,'no-store');
    for(const value of ['https://evil.test/api/site/session','/api/site/../../secret','/api/site/\\evil','/api/accounts/../../evil'])await assert.rejects(()=>request(value),TypeError);
    assert.equal(calls.length,1);
    globalThis.fetch=async()=>({ok:false,status:503,json:async()=>({error:'verification_resend_unavailable'})});await assert.rejects(()=>request('/api/site/verification/resend'),e=>e instanceof ApiError&&e.status===503&&/not available/u.test(e.message));
  }finally{globalThis.fetch=prior;}
});

// Minimal DOM fixtures exercise the real serializer without trusting browser HTML parsing.
class Node {
  constructor(tag,text=''){this.nodeType=tag?1:3;this.tagName=tag?.toUpperCase();this.childNodes=[];this.attributes={};this.dataset={};this._text=text;}
  append(...nodes){for(const node of nodes)this.childNodes.push(typeof node==='string'?new Node(null,node):node);}
  get children(){return this.childNodes.filter(x=>x.nodeType===1);}
  get textContent(){return this.nodeType===3?this._text:this.childNodes.map(x=>x.textContent).join('');}
  set textContent(value){this.childNodes=[];if(value)this.append(String(value));}
  setAttribute(name,value){this.attributes[name]=String(value);if(name.startsWith('data-'))this.dataset[name.slice(5).replace(/-([a-z])/gu,(_,x)=>x.toUpperCase())]=String(value);}
  getAttribute(name){return this.attributes[name]??null;}
  querySelector(tag){return this.children.find(x=>x.tagName===tag.toUpperCase())||this.children.map(x=>x.querySelector(tag)).find(Boolean)||null;}
}
const text=value=>new Node(null,value);const el=(tag,...children)=>{const n=new Node(tag);n.append(...children);return n;};
const media='a'.repeat(32);
test('rich post text/image/text caret order survives serialization into the backend AST',()=>{
  const img=el('img');img.dataset.mediaId=media;img.alt='Photo';const body=serializeBody(el('div',el('p',text('Before '),el('strong',text('bold')),img,text(' after'))));
  assert.deepEqual(body.children.map(x=>x.type),['paragraph','image','paragraph']);assert.deepEqual(body.children[0].children[1].marks,['bold']);assert.equal(body.children[2].children[0].text,' after');
  assert.equal(bodyHasContent(body),true);assert.doesNotThrow(()=>normalizeDocument(body,{[media]:{id:media}}));
});
test('serializer rejects script/embed content, remote image IDs and unsafe links',()=>{
  const link=el('a',text('unsafe'));link.setAttribute('href','javascript:alert(1)');const img=el('img');img.dataset.mediaId='https://evil.test/a';
  const body=serializeBody(el('div',el('script',text('steal')),el('iframe',text('frame')),el('p',link,img,text('visible'))));
  assert.equal(body.children.length,1);assert.equal(body.children[0].children.map(x=>x.text).join(''),'unsafevisible');assert.equal(bodyHasContent({type:'doc',children:[]}),false);assert.equal(validMediaId(media),true);assert.equal(validMediaId('../x'),false);
  assert.doesNotThrow(()=>normalizeDocument(body,{}));
});
test('lists split around inline images and only fixed media URLs are rendered',()=>{
  const img=el('img');img.dataset.mediaId=media;const body=serializeBody(el('div',el('ul',el('li',text('first'),img,text('last')))));
  assert.deepEqual(body.children.map(x=>x.type),['list','image','list']);assert.doesNotThrow(()=>normalizeDocument(body,{[media]:{id:media}}));
  const before=globalThis.document;globalThis.document={createElement:tag=>new Node(tag),createTextNode:text};try{const result=renderBody({type:'doc',children:[{type:'image',mediaId:media,alt:'<img onerror=evil>'},{type:'image',mediaId:'../secret'}]});assert.equal(result.children.length,1);const image=result.querySelector('img');assert.equal(image.getAttribute('src'),'/api/site/media/'+media);assert.equal(image.getAttribute('alt'),'<img onerror=evil>');assert.equal(image.getAttribute('onerror'),null);}finally{globalThis.document=before;}
});
test('all publishing/legal/contact categories exist without an invented Instagram link',()=>{
  for(const key of ['announcements','updates','maintenance','videos','podcasts','recommended-brands','partnerships'])assert.ok(CATEGORIES[key]);
  const before=globalThis.document;globalThis.document={createElement:tag=>new Node(tag),createTextNode:text};try{
    for(const [key,title]of [['terms','Terms of Service'],['user-agreement','User Agreement'],['copyright','Copyright & Licensing']]){const page=legalPage(key);assert.match(page.node.textContent,new RegExp(title));assert.match(page.node.textContent,/Current registration terms/u);assert.match(page.node.textContent,/Review draft/u);}
    const page=contactPage();assert.match(page.node.textContent,/support@infected-nation.com/u);assert.match(page.node.textContent,/GrimVirusOfficial or MainDirectory/u);assert.match(page.node.textContent,/Instagram · Official profile not configured/u);
    const anchors=[];const walk=node=>{if(node.tagName==='A')anchors.push(node.getAttribute('href'));for(const child of node.children)walk(child);};walk(page.node);assert.equal(anchors.some(href=>href?.includes('instagram.com')),false);
  }finally{globalThis.document=before;}
});

test('API settings open the selected product settings while normal workspace entry retains its dashboard',()=>{
  const before=globalThis.document;globalThis.document={createElement:tag=>new Node(tag),createTextNode:text};
  try{
    const context={session:{signedIn:true,owner:false}};
    for(const [type,keys,src,title]of [['host',true,'/host/settings?embed=site','Red-XAI Host API settings'],['host',false,'/host?embed=site','Red-XAI Host private workspace'],['db',true,'/v1/console/?embed=site#/keys','Red-XAI DB developer API keys'],['db',false,'/v1/console/?embed=site','Red-XAI DB private workspace']]){
      const frame=workspacePage(type,context,keys).node.querySelector('iframe');
      assert.equal(frame.getAttribute('src'),src);assert.equal(frame.getAttribute('title'),title);assert.equal(frame.getAttribute('referrerpolicy'),'same-origin');
    }
  }finally{globalThis.document=before;}
});

test('existing tool access stays available without a Hub session and uses ordinary isolated new-tab links',async()=>{
  const beforeDocument=globalThis.document,beforeFetch=globalThis.fetch;
  globalThis.document={createElement:tag=>new Node(tag),createTextNode:text};
  const anchors=root=>{const result=[];const walk=node=>{if(node.tagName==='A')result.push(node);for(const child of node.children)walk(child);};walk(root);return result;};
  try{
    assert.equal(isPrivatePath('/cloud-pc'),false);
    for(const signedIn of [false,true]){
      const page=cloudPcPage({session:{signedIn},current:()=>true});
      const access=anchors(page.node).find(a=>a.getAttribute('href')===EXISTING_TOOLS.cloudPc);
      assert.ok(access);assert.equal(access.getAttribute('target'),'_blank');assert.equal(access.getAttribute('rel'),'noopener noreferrer');assert.equal(access.getAttribute('data-route'),null);
      assert.doesNotMatch(page.node.textContent,/connection unavailable|Sign in to continue/u);
    }
    for(const current of [false,true])for(const available of [false,true]){
      globalThis.fetch=async()=>({ok:available,status:available?200:503,json:async()=>available?{version:'test-release',releaseUrl:'https://github.com/grimvirusoffical-source/red-xai-vortex/releases/latest',assets:[]}:{error:'service_unavailable'}});
      const page=await vortexPage({session:{signedIn:false},current:()=>current,preferences:{}});
      const access=anchors(page.node).find(a=>a.getAttribute('href')===EXISTING_TOOLS.vortex);
      assert.ok(access);assert.equal(access.getAttribute('target'),'_blank');assert.equal(access.getAttribute('rel'),'noopener noreferrer');assert.equal(access.getAttribute('data-route'),null);
    }
    for(const url of Object.values(EXISTING_TOOLS)){const target=new URL(url);assert.equal(target.origin,'https://45.77.105.187');assert.equal(target.search,'');assert.equal(target.hash,'');assert.equal(target.username,'');assert.equal(target.password,'');}
  }finally{globalThis.document=beforeDocument;globalThis.fetch=beforeFetch;}
});
test('frontend module graph is complete and registration authority is never recreated',async()=>{
  for(const name of ['app.js',...(await readdir(new URL('./site/',import.meta.url))).filter(x=>x.endsWith('.js')).map(x=>'site/'+x)]){
    const content=await readFile(new URL(name,import.meta.url),'utf8');
    for(const match of content.matchAll(/(?:from\s+|import\()(['"])(\.\.?\/[^'"]+)\1/gu))await readFile(new URL(match[2],new URL(name,import.meta.url)));
    assert.doesNotMatch(content,/localStorage|sessionStorage|\.innerHTML\s*=/u);
    assert.doesNotMatch(content,/primary\(['"]signup|request\(['"]\/api\/accounts\/signup/u);
  }
  const css=await readFile(new URL('./styles.css',import.meta.url),'utf8');assert.doesNotMatch(css,/\+@media/u);
});
