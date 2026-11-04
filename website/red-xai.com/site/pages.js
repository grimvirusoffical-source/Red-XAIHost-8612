import {h,button,link,external,field,alert,pageHead,empty,formatDate,notify,safeHref} from './dom.js';
import {request} from './api.js';
import {CATEGORIES,renderBody,createPostEditor} from './editor.js';
import {LEGAL,LEGAL_REFERENCES} from './legal.js';

// Existing services keep their own sign-in and security boundaries.
export const EXISTING_TOOLS=Object.freeze({vortex:'https://45.77.105.187/vortex/',cloudPc:'https://45.77.105.187/vortex/cloudpc/'});

export async function postsPage(category,context){
  const title=CATEGORIES[category]||'News',body=h('div',{class:'wrap section'}),controls=h('div',{class:'row'}),list=h('div',{class:'post-grid'}),editorSlot=h('div'),recovery=h('div',{'aria-live':'polite'});let editor=null,editorToken=null,disposed=false,sequence=0,recoveryVerified=false,checkingRecovery=false;
  const manage=context.session.owner===true;
  const drafts=manage?context.drafts?.(category):null;
  const active=()=>!disposed&&context.current()&&(!drafts||drafts.valid());
  const ownerScope=h('input',{type:'checkbox',checked:manage});
  const status=h('div',{'aria-live':'polite'});
  const closeEditor=({discard=false}={})=>{
    if(editor){if(discard)drafts?.discard(editorToken);else if(editor.isDirty())drafts?.remember(editorToken,editor.getSnapshot(),{pending:editor.isSaving()});editor.dispose();}
    editor=null;editorToken=null;editorSlot.replaceChildren();renderRecovery();
  };
  const openEditor=(post,initialDraft)=>{
    if(!active())return;
    if(editor?.isSaving()){notify('Wait for the current save to finish.');return;}
    if(!editor&&drafts?.peek()){notify('Restore or discard the retained draft before opening another editor.');return;}
    if(editor?.isDirty()&&!window.confirm('Discard unsaved changes?'))return;
    closeEditor({discard:true});const token={};editorToken=token;
    editor=createPostEditor({category,post,initialDraft,active,mediaEnabled:context.config.capabilities?.media===true,onSaveStart:snapshot=>drafts?.remember(token,snapshot,{pending:true}),onSaveSettled:success=>drafts?.settle(token,success),onCancel:()=>closeEditor({discard:true}),onSaved:()=>{closeEditor({discard:true});notify('Post saved.');load();}});
    recovery.replaceChildren();editorSlot.replaceChildren(editor.node);editorSlot.scrollIntoView({behavior:context.preferences.reducedMotion?'instant':'smooth',block:'start'});
  };
  const verifyRecovery=async()=>{
    if(checkingRecovery||!active())return false;checkingRecovery=true;renderRecovery();
    try{recoveryVerified=await context.verifyDraftSession?.()===true&&active();return recoveryVerified;}
    catch{recoveryVerified=false;return false;}
    finally{checkingRecovery=false;renderRecovery();}
  };
  function renderRecovery(){
    recovery.replaceChildren();if(editor||!active())return;const entry=drafts?.peek();if(!entry)return;
    if(!recoveryVerified||checkingRecovery){recovery.append(alert('Checking your account before offering a retained draft…'));return;}
    const restore=button('Restore draft',async()=>{
      if(entry.pending||!await verifyRecovery())return;const current=drafts.peek();
      if(!current||current.token!==entry.token||current.pending)return;
      const snapshot=drafts.take(entry.token);if(snapshot)openEditor(undefined,snapshot);
    });
    const discard=button('Discard draft',()=>{if(!entry.pending)drafts.discard(entry.token);},true);
    restore.disabled=discard.disabled=entry.pending;
    recovery.append(h('section',{class:'card'},h('h2',null,entry.pending?'Saving your post…':'Unsaved changes available'),h('p',null,entry.pending?'Wait for the save result before restoring or starting another post.':entry.failed?'The previous save could not be confirmed. Review the latest posts before restoring and retrying.':'Your unsaved changes are kept only in this tab while your owner session remains valid. Restore them or discard them.'),h('div',{class:'row'},restore,discard,entry.failed?button('Refresh posts',load,true):null)));
  }
  const stopWatching=drafts?.subscribe(event=>{renderRecovery();if(event?.type==='saved'&&event.category===category&&active()&&!editor){notify('Post saved.');load();}});
  const load=async()=>{const mine=++sequence;status.replaceChildren(alert('Loading posts…'));try{const data=await request('/api/site/posts?category='+encodeURIComponent(category)+(manage&&ownerScope.checked?'&scope=owner':''));if(!active()||mine!==sequence)return;status.replaceChildren();list.replaceChildren();
    for(const post of data.posts||[]){
      const tools=manage?h('div',{class:'owner-tools'},button('Edit',()=>openEditor(post),true),button('Delete',async()=>{if(!window.confirm('Remove this post from public view?'))return;try{await request('/api/site/posts/'+encodeURIComponent(post.id),{method:'DELETE'});if(active()){notify('Post removed from public view.');load();}}catch(e){if(active())notify(e.message);}},true)):null;
      list.append(h('article',{class:'card post'},h('div',{class:'post-meta'},h('span',null,formatDate(post.publishedAt||post.createdAt,context.preferences)),manage?h('span',{class:'tag'},post.status):null),h('h2',null,post.title),post.startsAt||post.endsAt?h('div',{class:'maintenance-time'},post.startsAt?h('span',null,'Starts: '+formatDate(post.startsAt,context.preferences)):null,post.endsAt?h('span',null,'Ends: '+formatDate(post.endsAt,context.preferences)):null):null,renderBody(post.body),tools));
    }if(!list.children.length)list.append(empty('No posts yet','Published '+title.toLowerCase()+' will appear here.'));
  }catch(e){if(active()&&mine===sequence){status.replaceChildren(alert(e.message,'warning'),button('Retry',load,true));list.replaceChildren();}}};
  if(manage)controls.append(button('Create post',()=>openEditor()),h('label',{class:'inline-check'},ownerScope,'Include my drafts'));ownerScope.addEventListener('change',load);
  body.append(controls,recovery,editorSlot,status,list);load();if(drafts?.peek())void verifyRecovery();
  return {node:h('div',null,pageHead(category==='recommended-brands'||category==='partnerships'?'Brands':category==='videos'||category==='podcasts'||category==='videos-podcasts'?'Media':'News',title,'Official posts from Red-XAI.'),body),isDirty:()=>editor?.isDirty()||false,discardDraft:()=>closeEditor({discard:true}),dispose:()=>{disposed=true;sequence++;stopWatching?.();closeEditor();}};
}
export function workspacePage(type,context,keys=false){
  const db=type==='db',title=db?'Red-XAI DB':'Red-XAI Host';
  if(!context.session.signedIn)return signedOut(title,db?'/db':'/host');
  const frame=h('iframe',{class:'workspace-frame',src:db?'/v1/console/?embed=site'+(keys?'#/keys':''):keys?'/host/settings?embed=site':'/host?embed=site',title:db?(keys?'Red-XAI DB developer API keys':'Red-XAI DB private workspace'):keys?'Red-XAI Host API settings':'Red-XAI Host private workspace',referrerpolicy:'same-origin',allow:'clipboard-write'});
  const note=db?'Your RedX databases, native BoxQL tools, files and project settings. Developer API keys are optional for API clients.':'Manage your hosting projects, nodes, subscriptions and resources with your website account.';
  return {node:h('div',{class:'wrap workspace-page'},h('header',{class:'page-head'},h('div',{class:'eyebrow'},'Workspace'),h('div',{class:'row between'},h('h1',null,title),db?link('Developer API keys','/api-keys','button quiet small'):link('Subscriptions','/products/subscriptions','button quiet small')),h('p',null,note)),h('p',{class:'frame-note'},context.session.owner===true&&db?'Owner account: the DB console labels its all-user overview explicitly and audits privileged access.':'Workspace access is checked by the service for your signed-in account.'),frame),dispose:()=>{frame.src='about:blank';frame.remove();}};
}
export function signedOut(title,next){return {node:h('div',null,pageHead('Your account',title,'Sign in with your Red-XAI account to open this workspace.'),h('div',{class:'wrap section'},empty('Sign in to continue','Your website session opens this workspace directly.',link('Sign in','/signin?next='+encodeURIComponent(next),'button'))))};}
export async function settingsPage(context){
  if(!context.session.signedIn)return signedOut('Account settings','/settings');
  const status=h('div',{'aria-live':'polite'}),prefs=context.preferences;
  const theme=h('select',null,...['system','dark','light'].map(x=>h('option',{value:x,selected:x===prefs.theme},x[0].toUpperCase()+x.slice(1))));
  const motion=h('input',{type:'checkbox',checked:prefs.reducedMotion}),email=h('input',{type:'checkbox',checked:prefs.emailNotifications}),browser=h('input',{type:'checkbox',checked:prefs.browserNotifications});
  const zone=h('input',{type:'text',value:prefs.timeZone||Intl.DateTimeFormat().resolvedOptions().timeZone,required:true,maxlength:80}),locale=h('input',{type:'text',value:prefs.locale||navigator.language||'en',required:true,maxlength:40});
  const notificationStatus=h('div',{'aria-live':'polite'});
  const permission=button('Allow browser notifications',async()=>{if(!('Notification'in window)){notificationStatus.replaceChildren(alert('This browser does not support notifications.','warning'));return;}const result=await Notification.requestPermission();notificationStatus.replaceChildren(alert(result==='granted'?'Browser permission granted. Save your preferences below.':'Browser permission was not granted.','warning'));},true);
  const save=h('button',{type:'submit',class:'button'},'Save preferences');
  const form=h('form',{class:'card'},h('h2',null,'Preferences'),h('div',{class:'form-grid'},field('Appearance',theme),field('Time zone (IANA name)',zone),field('Language / region',locale)),h('label',{class:'inline-check'},motion,'Reduce interface motion'),h('label',{class:'inline-check'},email,'Receive product and news email notifications when available'),h('label',{class:'inline-check'},browser,'Enable browser notifications when available'),h('p',null,'These preferences apply to the Hub. Product-specific options remain in Host and DB.'),permission,notificationStatus,h('div',{class:'separator'}),save,status);
  form.addEventListener('submit',async e=>{e.preventDefault();save.disabled=true;status.replaceChildren();try{new Intl.DateTimeFormat(locale.value,{timeZone:zone.value});const data=await request('/api/site/preferences',{method:'PATCH',json:{theme:theme.value,reducedMotion:motion.checked,emailNotifications:email.checked,browserNotifications:browser.checked,timeZone:zone.value.trim(),locale:locale.value.trim()}});if(context.current()){context.savedPreferences(data.preferences);status.replaceChildren(alert('Preferences saved.','success'));}}catch(e){if(context.current())status.replaceChildren(alert(e instanceof RangeError?'Enter a valid locale and time zone.':e.message,'warning'));}finally{save.disabled=false;}});
  const deletionStatus=h('div',{'aria-live':'polite'}),deletion=button('Delete account',()=>{deletionStatus.replaceChildren(alert('Automatic deletion is not available through the account service yet. Email support@infected-nation.com from your account email to request deletion. Your account has not been deleted.','warning'),h('a',{href:'mailto:support@infected-nation.com?subject=Red-XAI%20account%20deletion',class:'button secondary'},'Contact support'));},true);deletion.classList.add('danger');
  const account=context.session.account;
  return {node:h('div',null,pageHead('Your account','Settings','Manage your Hub preferences and account.'),h('div',{class:'wrap section settings-layout'},h('nav',{class:'settings-tabs','aria-label':'Settings'},link('General settings','/settings'),link('API keys','/api-keys')),h('div',{class:'grid'},h('section',{class:'card'},h('h2',null,'Signed in account'),h('p',null,account.displayName||account.username),h('p',null,account.email),button('Log out',context.logout,true)),form,h('section',{class:'card danger-zone'},h('h2',null,'Delete account'),h('p',null,'Account deletion can affect access to your products, subscriptions and data. Contact support for the currently supported recovery and deletion process.'),deletion,deletionStatus))))};
}
export function keysPage(context){
  if(!context.session.signedIn)return signedOut('API keys','/api-keys');
  const slot=h('div'),tabs=h('div',{class:'tabs'});let workspace=null;
  const select=type=>{workspace?.dispose();for(const b of tabs.children)b.setAttribute('aria-selected',String(b.dataset.type===type));workspace=workspacePage(type,context,true);slot.replaceChildren(workspace.node);};
  for(const [type,title]of [['db','DB developer keys'],['host','Host API settings']])tabs.append(h('button',{type:'button',class:'tab',role:'tab','data-type':type,onClick:()=>select(type)},title));tabs.setAttribute('role','tablist');tabs.setAttribute('aria-label','Product API settings');select('db');
  return {node:h('div',null,pageHead('Advanced settings','API keys','Optional developer access. Your normal website session does not require a DB API key.'),h('section',{class:'wrap'},alert('Create and manage keys inside the owning product. Project-level DB credentials may authorize a Host integration only within their explicit project scope.'),tabs,slot)),dispose:()=>workspace?.dispose()};
}
export async function subscriptionsPage(context){
  const box=h('div',{class:'wrap section'});try{const data=await request('/api/site/subscriptions');if(!context.current())return {node:h('div')};
    if(!data.subscriptions?.length)box.append(empty('Plans coming soon','Subscriptions will appear here when pricing and checkout are configured.'));
    else box.append(h('div',{class:'grid three'},...data.subscriptions.map(plan=>{
      const amount=new Intl.NumberFormat(undefined,{style:'currency',currency:plan.price.currency}).format(plan.price.amount/100),status=h('div',{'aria-live':'polite'}),buy=button('Continue to checkout',async()=>{if(!context.session.signedIn){context.navigate('/signin?next=%2Fproducts%2Fsubscriptions');return;}buy.disabled=true;try{const result=await request('/api/site/checkout',{method:'POST',json:{productId:plan.id}});if(!context.current())return;const url=new URL(result.url);if(url.protocol!=='https:'||url.username||url.password)throw new Error('The checkout address could not be verified.');location.assign(url.href);}catch(e){if(context.current())status.replaceChildren(alert(e.message,'warning'));}finally{buy.disabled=false;}});
      buy.disabled=plan.available!==true;
      return h('article',{class:'card'},h('h2',null,plan.name),h('p',null,plan.description||''),h('p',null,amount+(plan.price.interval==='one-time'?' one-time':' / '+plan.price.interval)),plan.available!==true?h('p',null,'This plan is not currently available.'):null,buy,status);
    })));
  }catch(e){if(context.current())box.append(empty('Subscriptions coming soon','Central checkout is not available yet. Existing product subscriptions remain managed in their product workspace.',link('Open Host','/host','button secondary')),alert(e.message,'warning'));}
  return {node:h('div',null,pageHead('Products','Subscriptions','Browse the configured plans across Red-XAI. Price, billing interval and terms are shown before purchase.'),box)};
}
export function legalPage(key){const item=LEGAL[key];if(!item)return null;return {node:h('div',null,pageHead('Legal documents',item.title,'Review draft · Red-XAI'),h('article',{class:'wrap section legal-body'},alert('Review draft. These new pages do not replace the existing registration terms. Read the current accepted terms at the registration terms page.','warning'),link('Current registration terms','/tos','button secondary'),...item.sections.map(([title,body])=>h('section',null,h('h2',null,title),h('p',null,body))),h('section',null,h('h2',null,'Reference resources'),h('ul',null,...LEGAL_REFERENCES.map(([title,url])=>h('li',null,external(title,url)))))))};}
export function contactPage(){return {node:h('div',null,pageHead('Contact','Let’s get you connected','Support, social channels and upcoming community spaces.'),h('div',{class:'wrap section grid two'},h('section',{class:'card contact-list'},h('div',null,h('h2',null,'Email support'),h('a',{href:'mailto:support@infected-nation.com'},'support@infected-nation.com')),h('div',null,h('h2',null,'Discord'),h('p',null,'Contact GrimVirusOfficial or MainDirectory.')),h('div',null,h('h2',null,'Live support'),h('p',null,'Coming soon.')),h('div',null,h('h2',null,'Community Discord'),h('p',null,'Coming soon.'))),h('section',{class:'card contact-list'},h('div',null,h('h2',null,'Follow Red-XAI'),h('p',null,external('X · @RedXAIofficial','https://x.com/RedXAIofficial')),h('p',null,external('TikTok · @maindirectory','https://www.tiktok.com/@maindirectory')),h('p',{class:'social-placeholder'},'Instagram · Official profile not configured.')))))};}
export function communityPage(){return {node:h('div',null,pageHead('Community','Red-XAI Community Hub','A dedicated space for the Red-XAI community.'),h('section',{class:'wrap section'},empty('Coming soon','The community portal and community Discord are in development. We will publish the official links here.',link('Read announcements','/news/announcements','button secondary'))))};}
export async function vortexPage(context){
  const content=h('section',{class:'wrap section'});
  content.append(h('div',{class:'card'},h('h2',null,'Open Vortex'),h('p',null,'Continue to the existing Vortex service in a new tab. Sign in there if requested.'),external('Open Vortex',EXISTING_TOOLS.vortex,'button')));
  try{
    const release=await request('/api/site/releases/vortex');
    if(context.current())content.append(h('div',{class:'card'},h('h2',null,'Current release · '+release.version),release.publishedAt?h('p',null,formatDate(release.publishedAt,context.preferences)):null,h('p',null,'Choose the package matching your operating system and processor. Review the release notes before installing.'),external('Release notes and checksums',release.releaseUrl)),h('div',{class:'grid two'},...release.assets.map(asset=>h('article',{class:'card'},h('h3',null,asset.name),h('p',null,new Intl.NumberFormat(undefined,{maximumFractionDigits:1}).format(asset.size/1024/1024)+' MB'),external('Download',asset.url)))));
  }catch{
    if(context.current())content.append(empty('Downloads temporarily unavailable','Try again shortly or view the official release page.',external('Official Vortex releases','https://github.com/grimvirusoffical-source/red-xai-vortex/releases/latest')));
  }
  return {node:h('div',null,pageHead('Your tools','Vortex','Explore the published Vortex desktop application and supported downloads.'),content,h('section',{class:'wrap section'},h('h2',null,'Cloud PC'),h('p',null,'Cloud PC availability and access are separate from downloading Vortex.'),link('View Cloud PC','/cloud-pc','button secondary')))};
}
export function cloudPcPage(){
  return {node:h('div',null,pageHead('Your tools','Cloud PC','Open your existing cloud desktop service.'),h('section',{class:'wrap section'},h('div',{class:'card'},h('h2',null,'Open Cloud PC'),h('p',null,'Continue to Cloud PC in a new tab. The service handles its own sign-in and desktop permissions.'),external('Open Cloud PC',EXISTING_TOOLS.cloudPc,'button'))))};
}
export function apiDocsPage(){return {node:h('div',null,pageHead('Red-XAI API','Red-XAI Quick Login / Signup','Account integration documentation · Coming soon'),h('article',{class:'wrap section legal-body'},alert('Developer enrollment and API key generation are coming soon. Existing approved applications keep their current integration.'),h('h2',null,'One Red-XAI account across your apps'),h('p',null,'Quick Login lets an approved application request a sign-in through the Red-XAI website. The user signs in on the trusted account page, checks that the displayed numbers match the application, then chooses Sign in.'),h('h2',null,'User approval'),h('ol',null,h('li',null,'The application starts its existing approved authentication flow.'),h('li',null,'The user opens the Red-XAI approval page and authenticates.'),h('li',null,'The user compares the displayed numbers with the calling application.'),h('li',null,'The user selects Sign in only when the request matches.')),h('h2',null,'Integration boundaries'),h('p',null,'Use the registered application, return locations and scopes supported by the account service. Keep secrets on the application backend, handle expiration and cancellation, and request only the permissions the application needs.'),h('p',null,'This documentation page does not create new clients or developer keys. Detailed SDK onboarding and enrollment will be published when available.'),link('Contact integration support','/contact','button secondary')))};}
