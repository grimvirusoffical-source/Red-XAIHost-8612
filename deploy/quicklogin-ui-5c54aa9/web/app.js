'use strict';
const el=id=>document.getElementById(id);
const requestFromLink=new URLSearchParams(location.hash.slice(1)).get('request');
let request=requestFromLink;
try {
 if(/^[A-Za-z0-9_-]{43}$/.test(requestFromLink||''))sessionStorage.setItem('rxql.pendingApproval',requestFromLink);
 else request=sessionStorage.getItem('rxql.pendingApproval');
} catch {}
history.replaceState(null,'',location.pathname);

let phase='idle',refreshPending=false,finished=false,ready=null;
const retryMessage='The request could not be checked. Return to this tab or reload to try again.';
const messages={sign_in_required:'Sign in on the existing Red-XAI website first.',email_verification_required_or_unknown:'Verify your Red-XAI email before signing in to this app.',identity_contract_unverified:'The site could not verify your account.',request_expired:'This connection expired. Start Quick Login in your application again.',request_in_another_browser:'Open the request in the same browser.',account_changed_reload:'Your account changed. Check the account and code again before signing in.',csrf_invalid:'The sign-in check changed. Return to this tab or reload to check the request again.'};
const visible=()=>document.visibilityState!=='hidden';
function blockApproval(){
 ready=null;
 el('approve').hidden=true;el('approve').disabled=true;
 el('device-trust').hidden=true;
 el('code').textContent='';el('identity').textContent='';el('permissions').textContent='';
}
function forgetRequest(){
 try{
  if(sessionStorage.getItem('rxql.pendingApproval')===request)sessionStorage.removeItem('rxql.pendingApproval');
  sessionStorage.removeItem('rxql.redirected.'+request);
 }catch{}
}
function fail(error){
 blockApproval();el('status').textContent='Sign-in needs attention.';
 el('error').textContent=error.message||retryMessage;
 if(['request_expired','request_in_another_browser'].includes(error.code)){finished=true;forgetRequest();}
}
async function post(route,data){
 const signal=typeof AbortSignal!=='undefined'&&typeof AbortSignal.timeout==='function'?AbortSignal.timeout(15000):undefined;
 let r,d;
 try{
  r=await fetch('/redxai-connect/'+route,{method:'POST',credentials:'same-origin',redirect:'error',headers:{'content-type':'application/json'},body:JSON.stringify(data),signal});
  d=await r.json();
 }catch{throw Error(retryMessage);}
 if(!r.ok){const code=typeof d?.error==='string'?d.error:'';const error=Error(messages[code]||retryMessage);error.code=code;throw error;}
 return d;
}
function checkedContext(d){
 if(!d||typeof d!=='object'||typeof d.signedIn!=='boolean'||!(/^[A-F0-9]{8}$/.test(d.code||''))||typeof d.clientName!=='string'||!d.clientName||d.clientName.length>256||d.scope!==undefined&&(typeof d.scope!=='string'||d.scope.length>2048))throw Error('The site could not verify this sign-in request.');
 const view={signedIn:d.signedIn,code:d.code,clientName:d.clientName,scope:d.scope||'profile.read'};
 if(d.signedIn){
  if(!d.user||typeof d.user.sub!=='string'||!d.user.sub||d.user.sub.length>256||!(/^[A-Za-z0-9_-]{43}$/.test(d.csrf||'')))throw Error('The site could not verify your account.');
  view.subject=d.user.sub;
  view.display=typeof d.user.username==='string'&&d.user.username||typeof d.user.email==='string'&&d.user.email||d.user.sub;
  view.csrf=d.csrf;
 }
 return view;
}
function renderContext(view){
 blockApproval();
 el('code').textContent=view.code;
 el('permissions').textContent='Application: '+view.clientName+' · Permission: '+view.scope;
 el('identity').textContent=view.signedIn?'Account: '+view.display:'';
 el('signin').hidden=view.signedIn;
 el('status').textContent=view.signedIn?'Compare this code with your app. Choose Sign in only when the codes match.':'Sign in or register on Red-XAI first. Return to this tab when you are done.';
 if(!view.signedIn){
  // The normal website login receives its existing return path, never a
  // request token in a query. Keep the request only in tab-scoped storage.
  let redirect=true;
  try{
   const key='rxql.redirected.'+request;
   if(sessionStorage.getItem(key)==='yes')redirect=false;
   else sessionStorage.setItem(key,'yes');
  }catch{redirect=false;}
  if(redirect)location.assign('/signin?next='+encodeURIComponent('/redxai-connect/'));
  return;
 }
 ready=view;el('approve').hidden=false;el('approve').disabled=false;
 el('device-trust').hidden=false;
}
async function refresh(){
 if(finished)return;
 refreshPending=true;blockApproval();
 if(phase!=='idle'||!visible())return;
 phase='context';
 try{
  // /context rotates the server's CSRF value. Serialize these requests rather
  // than letting a late focus response leave a stale approval clickable.
  do{
   refreshPending=false;el('error').textContent='';el('status').textContent='Checking your Red-XAI sign-in…';
   try{
    if(!/^[A-Za-z0-9_-]{43}$/.test(request||''))throw Error('Open this page from Quick Login in a connected app.');
    const view=checkedContext(await post('context',{request}));
    if(!refreshPending&&visible())renderContext(view);
   }catch(error){if(!refreshPending)fail(error);}
  }while(refreshPending&&!finished&&visible());
 }finally{phase='idle';}
}
function sameRequestAccount(a,b){
 return b.signedIn&&['subject','display','code','clientName','scope'].every(key=>a[key]===b[key]);
}
document.querySelectorAll('input[name="rxql-trust"]').forEach(input=>{
 input.addEventListener('change',()=>{
  el('custom-days-label').hidden=document.querySelector('input[name="rxql-trust"]:checked')?.value!=='custom';
 });
});
function selectedTrust(){
 const mode=document.querySelector('input[name="rxql-trust"]:checked')?.value||'no';
 if(mode!=='custom')return{mode};
 const days=Number(el('custom-days').value);
 if(!Number.isSafeInteger(days)||days<1||days>3650)throw Error('Choose 1 to 3650 custom days.');
 return{mode,days};
}
el('approve').addEventListener('click',async()=>{
 if(phase!=='idle'||finished||!ready||!visible())return;
 let trust;try{trust=selectedTrust();}catch(error){el('error').textContent=error.message;return;}
 const compared=ready;
 phase='approval';refreshPending=false;blockApproval();el('error').textContent='';
 try{
  // Recheck expiry and identity even if no focus event arrived. A changed
  // account/request is displayed and always needs another explicit click.
  const current=checkedContext(await post('context',{request}));
  if(refreshPending||!visible()){refreshPending=true;return;}
  if(!sameRequestAccount(compared,current)){
   renderContext(current);el('error').textContent='Your account or request changed. Compare the displayed details again, then choose Sign in.';return;
  }
  const result=await post('approve',{request,csrf:current.csrf,trust});
  if(result?.approved!==true)throw Error('The site did not confirm sign-in. Check your app before trying again.');
  finished=true;forgetRequest();blockApproval();el('signin').hidden=true;
  el('status').textContent='Signed in! Returning to your app…';
  try{window.opener?.focus();window.close();}catch{}
  if(!window.closed)el('status').textContent='Signed in! Return to your app; this browser tab can be closed.';
 }catch(error){fail(error);}
 finally{phase='idle';if(refreshPending&&!finished)void refresh();}
});
window.addEventListener('focus',()=>void refresh());
window.addEventListener('blur',()=>{if(!finished){refreshPending=true;blockApproval();}});
document.addEventListener('visibilitychange',()=>{if(visible())void refresh();else if(!finished){refreshPending=true;blockApproval();}});
void refresh();
