'use strict';
const test=require('node:test'),assert=require('node:assert/strict'),vm=require('node:vm');
const fs=require('node:fs'),path=require('node:path');
const source=fs.readFileSync(path.join(__dirname,'../web/app.js'),'utf8');
const html=fs.readFileSync(path.join(__dirname,'../web/index.html'),'utf8');
const request='r'.repeat(43);
const tick=()=>new Promise(resolve=>setImmediate(resolve));
const drain=async()=>{await tick();await tick();};
const deferred=()=>{let resolve,reject;const promise=new Promise((yes,no)=>{resolve=yes;reject=no});return{promise,resolve,reject};};
const context=(overrides={})=>({signedIn:true,user:{sub:'account_A',username:'Alice'},code:'A1B2C3D4',clientName:'Red-XAI Bridge',scope:'profile.read',csrf:'a'.repeat(43),...overrides});
const response=(body,status=200)=>({ok:status>=200&&status<300,status,json:async()=>body});
class Element{
 constructor(id){this.id=id;this.hidden=['approve','signin','device-trust','custom-days-label'].includes(id);this.disabled=id==='approve';this.textContent='';this.value=id==='custom-days'?'14':'';this.events={};}
 addEventListener(name,callback){(this.events[name]||=[]).push(callback);}
 trigger(name){return Promise.all((this.events[name]||[]).map(callback=>callback()));}
}
function site({handler,storage=new Map(),hash='#request='+request,visibility='visible'}={}){
 const elements=new Map([...html.matchAll(/\bid="([^"]+)"/g)].map(([,id])=>[id,new Element(id)]));
 const calls=[],redirects=[],historyCalls=[],winEvents={},docEvents={};
 const radios=['no','5days','20days','30days','indefinitely','custom'].map(value=>{const e=new Element(value);e.value=value;return e;});
 let selected=radios[3],closeCalls=0;
 const sessionStorage={getItem:key=>storage.get(key)??null,setItem:(key,value)=>storage.set(key,String(value)),removeItem:key=>storage.delete(key)};
 const location={pathname:'/redxai-connect/',hash,search:'',assign:url=>redirects.push(url)};
 const history={replaceState:(state,title,url)=>{historyCalls.push(url);location.hash='';location.search='';}};
 const document={visibilityState:visibility,getElementById:id=>{assert.ok(elements.has(id),'unknown element '+id);return elements.get(id);},querySelectorAll:selector=>{assert.equal(selector,'input[name="rxql-trust"]');return radios;},querySelector:selector=>{assert.equal(selector,'input[name="rxql-trust"]:checked');return selected;},addEventListener:(name,callback)=>(docEvents[name]||=[]).push(callback)};
 const window={closed:false,close:()=>closeCalls++,opener:null,addEventListener:(name,callback)=>(winEvents[name]||=[]).push(callback)};
 const fetch=async(url,options)=>{
  const item={url,options,body:JSON.parse(options.body)};calls.push(item);
  assert.match(url,/^\/redxai-connect\/(context|approve)$/);assert.equal(options.method,'POST');assert.equal(options.credentials,'same-origin');assert.equal(options.redirect,'error');assert.equal(options.headers['content-type'],'application/json');assert.deepEqual(Object.keys(options.headers),['content-type']);assert.equal(url.includes(request),false);
  return handler?handler(item,calls):response(url.endsWith('/context')?context():{approved:true});
 };
 vm.runInNewContext(source,{document,window,location,history,sessionStorage,fetch,URLSearchParams,AbortSignal},{timeout:3000});
 return{elements,calls,storage,redirects,historyCalls,window,document,
  element:id=>elements.get(id),click:()=>elements.get('approve').trigger('click'),
  event:(name)=>{for(const callback of winEvents[name]||[])callback();},
  visibility:state=>{document.visibilityState=state;for(const callback of docEvents.visibilitychange||[])callback();},
  selectTrust:value=>{selected=radios.find(e=>e.value===value);assert.ok(selected);},
  get closeCalls(){return closeCalls;}};
}
const approvals=s=>s.calls.filter(c=>c.url.endsWith('/approve'));
const contexts=s=>s.calls.filter(c=>c.url.endsWith('/context'));

test('approval page has one Sign in button and no check/developer navigation',()=>{
 const buttons=[...html.matchAll(/<button\b([^>]*)>([^<]*)<\/button>/g)];
 assert.equal(buttons.length,1);assert.match(buttons[0][1],/id="approve"/);assert.match(buttons[0][1],/hidden disabled/);assert.equal(buttons[0][2],'Sign in');assert.equal(html.includes('Check sign-in'),false);assert.equal(html.includes('/redxai-connect/developer'),false);assert.equal(html.includes('Developer portal'),false);assert.match(html,/code here matches the code in your app/);
});

test('automatic context does not approve; explicit Sign in rechecks and uses fresh CSRF',async()=>{
 let n=0;const s=site({handler:({url})=>response(url.endsWith('/context')?context({csrf:String.fromCharCode(97+n++).repeat(43)}):{approved:true})});
 await drain();assert.equal(contexts(s).length,1);assert.equal(approvals(s).length,0);assert.equal(s.element('approve').hidden,false);assert.equal(s.element('code').textContent,'A1B2C3D4');
 await s.click();assert.equal(contexts(s).length,2);assert.equal(approvals(s).length,1);assert.deepEqual(approvals(s)[0].body,{request,csrf:'b'.repeat(43),trust:{mode:'30days'}});assert.equal(s.closeCalls,1);assert.equal(s.element('approve').hidden,true);assert.equal(s.storage.has('rxql.pendingApproval'),false);
 s.event('focus');s.visibility('visible');await drain();assert.equal(s.calls.length,3);
});

test('focus during pending context coalesces one serialized refresh and never exposes old state',async()=>{
 const first=deferred(),next=deferred();let n=0;
 const s=site({handler:({url})=>url.endsWith('/approve')?response({approved:true}):++n===1?first.promise:n===2?next.promise:response(context({csrf:'c'.repeat(43)}))});
 s.event('focus');s.visibility('visible');assert.equal(contexts(s).length,1);assert.equal(s.element('approve').hidden,true);
 first.resolve(response(context()));await drain();assert.equal(contexts(s).length,2);assert.equal(s.element('approve').hidden,true);assert.equal(s.element('code').textContent,'');await s.click();assert.equal(approvals(s).length,0);
 next.resolve(response(context({csrf:'b'.repeat(43)})));await drain();assert.equal(s.element('approve').hidden,false);await s.click();assert.equal(approvals(s)[0].body.csrf,'c'.repeat(43));
});

test('focus during the click precheck cancels consent and requires a new click',async()=>{
 const precheck=deferred();let n=0;
 const s=site({handler:({url})=>url.endsWith('/approve')?response({approved:true}):++n===2?precheck.promise:response(context({csrf:n>2?'b'.repeat(43):'a'.repeat(43)}))});
 await drain();const click=s.click();s.event('focus');s.visibility('visible');assert.equal(contexts(s).length,2);
 precheck.resolve(response(context()));await click;await drain();assert.equal(contexts(s).length,3);assert.equal(approvals(s).length,0);assert.equal(s.element('approve').hidden,false);
 await s.click();assert.equal(approvals(s).length,1);
});

test('focus during approve does not rotate CSRF; double click cannot duplicate approval',async()=>{
 const approved=deferred();const s=site({handler:({url})=>url.endsWith('/approve')?approved.promise:response(context())});
 await drain();const first=s.click();await drain();assert.equal(approvals(s).length,1);const second=s.click();s.event('focus');s.visibility('visible');await drain();assert.equal(contexts(s).length,2);assert.equal(approvals(s).length,1);
 approved.resolve(response({approved:true}));await Promise.all([first,second]);s.event('focus');await drain();assert.equal(contexts(s).length,2);assert.equal(s.element('approve').hidden,true);
});

test('expired request after a valid view clears all approval details and cannot revive',async()=>{
 let n=0;const s=site({handler:()=>++n===1?response(context()):response({error:'request_expired'},410)});
 await drain();s.event('focus');assert.equal(s.element('approve').hidden,true);await drain();assert.equal(s.element('code').textContent,'');assert.equal(s.element('identity').textContent,'');assert.equal(s.element('permissions').textContent,'');assert.match(s.element('error').textContent,/expired/);
 await s.click();s.event('focus');await drain();assert.equal(approvals(s).length,0);assert.equal(contexts(s).length,2);assert.equal(s.storage.has('rxql.pendingApproval'),false);
});

test('unsigned login uses the normal return path and automatic focus checks without a check button',async()=>{
 let signedIn=false;const s=site({handler:({url})=>response(url.endsWith('/approve')?{approved:true}:signedIn?context():context({signedIn:false,user:undefined,csrf:undefined}))});
 await drain();assert.deepEqual(s.redirects,['/signin?next=%2Fredxai-connect%2F']);assert.equal(s.storage.get('rxql.pendingApproval'),request);assert.deepEqual(s.historyCalls,['/redxai-connect/']);assert.equal(s.redirects[0].includes(request),false);assert.equal(s.element('approve').hidden,true);
 signedIn=true;s.event('focus');await drain();assert.equal(s.element('signin').hidden,true);assert.equal(s.element('approve').hidden,false);assert.equal(approvals(s).length,0);await s.click();assert.equal(approvals(s).length,1);
});

test('returning from normal login restores the request from tab storage, not a URL query',async()=>{
 const storage=new Map([['rxql.pendingApproval',request],['rxql.redirected.'+request,'yes']]);
 const s=site({storage,hash:''});await drain();assert.equal(contexts(s)[0].body.request,request);assert.equal(s.element('approve').hidden,false);assert.deepEqual(s.redirects,[]);assert.deepEqual(s.historyCalls,['/redxai-connect/']);
});

test('an account, app, code or scope change at the explicit precheck requires another click',async t=>{
 for(const changed of [{user:{sub:'account_B',username:'Bob'}},{clientName:'Another app'},{code:'01020304'},{scope:'another.scope'}]){
  await t.test(JSON.stringify(changed),async()=>{
   let n=0;const s=site({handler:({url})=>response(url.endsWith('/approve')?{approved:true}:context(++n===1?{}:changed))});
   await drain();await s.click();assert.equal(approvals(s).length,0);assert.equal(s.element('approve').hidden,false);assert.match(s.element('error').textContent,/changed/);
   await s.click();assert.equal(approvals(s).length,1);
  });
 }
});

test('server account-change rejection blocks approval and never retries it automatically',async()=>{
 const s=site({handler:({url})=>url.endsWith('/approve')?response({error:'account_changed_reload'},409):response(context())});
 await drain();await s.click();assert.equal(approvals(s).length,1);assert.equal(s.element('approve').hidden,true);assert.match(s.element('error').textContent,/account changed/i);await s.click();assert.equal(approvals(s).length,1);
 s.event('focus');await drain();assert.equal(s.element('approve').hidden,false);assert.equal(approvals(s).length,1);
});

test('malformed context and verification/MFA denial cannot enable a Sign in action',async t=>{
 for(const invalid of [context({signedIn:'true'}),context({csrf:''}),context({code:'not-a-code'}),context({user:{}}),context({clientName:''})]){
  await t.test('malformed context',async()=>{const s=site({handler:()=>response(invalid)});await drain();assert.equal(s.element('approve').hidden,true);await s.click();assert.equal(approvals(s).length,0);});
 }
 for(const code of ['email_verification_required_or_unknown','identity_contract_unverified']){
  await t.test(code,async()=>{const s=site({handler:()=>response({error:code},403)});await drain();assert.equal(s.element('approve').hidden,true);assert.notEqual(s.element('error').textContent,'');await s.click();assert.equal(approvals(s).length,0);});
 }
});

test('trust duration remains the existing body contract and invalid days send no request',async()=>{
 const s=site();await drain();s.selectTrust('custom');s.element('custom-days').value='0';await s.click();assert.equal(contexts(s).length,1);assert.equal(approvals(s).length,0);assert.match(s.element('error').textContent,/1 to 3650/);
 s.element('custom-days').value='3650';await s.click();assert.deepEqual(approvals(s)[0].body,{request,csrf:'a'.repeat(43),trust:{mode:'custom',days:3650}});
});

test('hidden tabs cannot approve and refresh once after becoming visible',async()=>{
 const s=site({visibility:'hidden'});assert.equal(s.calls.length,0);await s.click();assert.equal(approvals(s).length,0);
 s.visibility('visible');await drain();assert.equal(contexts(s).length,1);s.visibility('hidden');assert.equal(s.element('approve').hidden,true);await s.click();assert.equal(approvals(s).length,0);s.visibility('visible');await drain();assert.equal(contexts(s).length,2);
});

test('an unconfirmed success response does not claim the app is signed in',async()=>{
 const s=site({handler:({url})=>response(url.endsWith('/approve')?{approved:false}:context())});await drain();await s.click();assert.equal(s.closeCalls,0);assert.equal(s.element('approve').hidden,true);assert.match(s.element('error').textContent,/did not confirm/);
});
