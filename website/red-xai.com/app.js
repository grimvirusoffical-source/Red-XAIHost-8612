import {h,replace,button,link,external,alert,pageHead,empty,safeNext,notify,formatDate} from './site/dom.js';
import {request,primary,assetJSON} from './site/api.js';
import {normalizeSession,identityKey,isPrivatePath,usesPrimaryPage} from './site/session.js';
import {STORES,renderStore,renderProduct,productCard} from './site/catalog.js';
import {createSignIn,verificationPage} from './site/auth.js';
import {postsPage,workspacePage,settingsPage,keysPage,subscriptionsPage,legalPage,contactPage,communityPage,vortexPage,cloudPcPage,apiDocsPage,signedOut} from './site/pages.js';

const main=document.getElementById('main'),header=document.getElementById('site-header'),footer=document.getElementById('site-footer');
const defaults={theme:'system',reducedMotion:false,emailNotifications:false,browserNotifications:false,timeZone:Intl.DateTimeFormat().resolvedOptions().timeZone,locale:navigator.language||'en'};
const state={session:null,sessionError:null,config:{capabilities:{}},catalog:null,catalogError:null,preferences:{...defaults},verificationIdentifier:''};
let page=null,epoch=0,sessionSequence=0,sessionInFlight=null,expiryTimer=null;
const loading=()=>h('div',{class:'loading',role:'status'},'Opening Red-XAI…');
const disposePage=()=>{epoch++;page?.dispose?.();page=null;for(const frame of main.querySelectorAll('iframe')){frame.src='about:blank';frame.remove();}main.replaceChildren();};
const applyPreferences=p=>{state.preferences={...defaults,...p};const theme=p.theme==='system'?(matchMedia('(prefers-color-scheme: light)').matches?'light':'dark'):p.theme;document.documentElement.dataset.theme=['light','dark'].includes(theme)?theme:'dark';document.documentElement.dataset.reducedMotion=String(p.reducedMotion===true);};
async function refreshSession({renderOnChange=true}={}){
  if(sessionInFlight)return sessionInFlight;
  const sequence=++sessionSequence;
  sessionInFlight=(async()=>{
    try{
      const session=normalizeSession(await request('/api/site/session'));if(sequence!==sessionSequence)return;
      const changed=identityKey(state.session)!==identityKey(session)||state.session===null||!!state.sessionError;
      if(changed){disposePage();state.verificationIdentifier='';state.preferences={...defaults};applyPreferences(state.preferences);}
      state.session=session;state.sessionError=null;state.config.capabilities={...state.config.capabilities,...session.capabilities};
      clearTimeout(expiryTimer);if(session.signedIn)expiryTimer=setTimeout(()=>refreshSession(),Math.max(1,Math.min(2147483647,session.expiresMs-Date.now()+20)));
      if(changed&&session.signedIn){const key=identityKey(session);request('/api/site/preferences').then(data=>{if(identityKey(state.session)===key&&!state.sessionError){applyPreferences(data.preferences);}}).catch(()=>{});}
      if(changed&&renderOnChange){renderHeader();renderRoute();}
      return session;
    }catch(error){if(sequence!==sessionSequence)return;disposePage();state.sessionError=error;clearTimeout(expiryTimer);if(renderOnChange){renderHeader();renderRoute();}return null;}
    finally{sessionInFlight=null;}
  })();return sessionInFlight;
}
function closeMenus(){for(const menu of header.querySelectorAll('details[open]'))menu.open=false;header.querySelector('.nav-links')?.classList.remove('mobile-open');header.querySelector('.mobile-toggle')?.setAttribute('aria-expanded','false');}
function menu(label,entries,gear=false){return h('details',{class:'nav-menu'+(gear?' gear-menu':'')},h('summary',{'aria-label':gear?'Account and settings':label},label,gear?null:h('span',{'aria-hidden':'true'},'⌄')),h('nav',{class:'dropdown','aria-label':gear?'Account options':label},...entries.map(([text,path])=>link(text,path))));}
function renderHeader(){
  const path=location.pathname,nav=h('nav',{class:'nav-links','aria-label':'Main navigation'},link('Hub','/','nav-link'),menu('Products',STORES.map(([key,title])=>[title,'/products/'+key])),menu('News',[['Announcements','/news/announcements'],['Updates','/news/updates'],['Maintenance','/news/maintenance']]),menu('Media',[['Upcoming videos','/media/videos'],['Upcoming podcasts','/media/podcasts']]),menu('Brands',[['Brands we recommend','/brands/recommended-brands'],['Partnerships','/brands/partnerships']]),menu('Host / DB',[['Red-XAI Host','/host'],['Red-XAI DB','/db']]),link('Community','/community','nav-link'),menu('Red-XAI API',[['Quick Login / Signup','/api/quick-login']]),menu('Legal Docs',[['Terms of Service','/legal/terms'],['User Agreement','/legal/user-agreement'],['Copyright','/legal/copyright']]),link('Contact','/contact','nav-link'));
  for(const a of nav.querySelectorAll('a'))if(a.getAttribute('href')===path)a.setAttribute('aria-current','page');
  const toggle=h('button',{type:'button',class:'icon-button mobile-toggle','aria-label':'Toggle navigation','aria-expanded':'false',onClick:()=>{const open=nav.classList.toggle('mobile-open');toggle.setAttribute('aria-expanded',String(open));}},'☰');
  const account=h('div',{class:'account-actions'});
  if(state.session?.signedIn&&!state.sessionError)account.append(h('span',{class:'account-name'},state.session.account.displayName||state.session.account.username),menu('⚙',[['Settings','/settings'],['API keys','/api-keys']],true));
  else account.append(link('Sign in','/signin','button small secondary'));
  replace(header,h('div',{class:'nav-wrap'},link('Red-XAI','/','brand'),toggle,nav,account));
  for(const detail of header.querySelectorAll('details'))detail.addEventListener('toggle',()=>{if(detail.open)for(const other of header.querySelectorAll('details'))if(other!==detail)other.open=false;});
}
function renderFooter(){replace(footer,h('div',{class:'wrap footer-content'},h('span',null,'© '+new Date().getFullYear()+' Red-XAI'),h('nav',{'aria-label':'Footer'},link('Contact','/contact'),link('Legal documents','/legal/terms'),link('Current registration terms','/tos'),external('X','https://x.com/RedXAIofficial'),external('TikTok','https://www.tiktok.com/@maindirectory'))));}
async function logout(){
  disposePage();main.append(loading());
  try{const result=await primary('logout',{});if(result.status!==200&&result.status!==204)throw new Error(result.json.message||'Logout could not be confirmed.');
    state.verificationIdentifier='';state.preferences={...defaults};applyPreferences(state.preferences);await refreshSession({renderOnChange:false});
    if(state.session?.signedIn&&!state.sessionError)throw new Error('The account service still reports an active session. Please retry.');
    navigate('/signin',true);
  }catch(error){notify(error.message);await refreshSession({renderOnChange:false});renderHeader();renderRoute();}
}
async function navigate(url,replaceHistory=false){
  const target=new URL(url,location.origin);if(target.origin!==location.origin)return;
  if(page?.isDirty?.()&&!window.confirm('Discard unsaved changes?'))return;
  closeMenus();
  if(usesPrimaryPage(target.pathname)){disposePage();location.assign(target.href);return;}
  disposePage();if(replaceHistory)history.replaceState(null,'',target.pathname+target.search+target.hash);else history.pushState(null,'',target.pathname+target.search+target.hash);
  if(isPrivatePath(target.pathname))await refreshSession({renderOnChange:false});
  renderHeader();await renderRoute();window.scrollTo({top:0,behavior:'instant'});
}
function context(mine){return {session:!state.sessionError&&state.session||{signedIn:false,owner:false},config:state.config,preferences:state.preferences,current:()=>mine===epoch&&!state.sessionError,navigate,logout,savedPreferences:applyPreferences};}
async function hubPage(ctx){
  const signed=ctx.session.signedIn,name=ctx.session.account?.displayName||ctx.session.account?.username;
  const workspace=(icon,title,description,path)=>link('',path,'workspace-item');
  const items=[['H','Red-XAI Host','Projects, devices and hosting','/host'],['DB','Red-XAI DB','Your native RedX database workspace','/db'],['>_','REDXAI CLI','Install and explore the command line','/products/cli/redxai-cli']].map(([icon,title,description,path])=>h('a',{href:path,'data-route':true,class:'workspace-item'},h('span',{class:'workspace-icon','aria-hidden':'true'},icon),h('div',null,h('strong',null,title),h('p',null,description)),h('span',{class:'arrow','aria-hidden':'true'},'↗')));
  const news=h('div',{class:'grid two'},h('div',{class:'card'},h('h3',null,'Official announcements'),h('p',null,'Product news and updates from Red-XAI.'),link('Read announcements','/news/announcements')));
  const software=(state.catalog?.products||[]).filter(p=>p.category==='software').slice(0,3);
  const node=h('div',null,h('section',{class:'hero'},h('div',{class:'wrap hero-grid'},h('div',{class:'hero-copy'},h('div',{class:'eyebrow'},h('span',{class:'status-dot'}),'Red-XAI Hub'),h('h1',null,signed?'Your workspace.':'Build. Connect.',h('br'),h('span',{class:'accent-text'},signed?'All in one place.':'Make it yours.')),h('p',null,signed?'Welcome back'+(name?', '+name:'')+'. Open your tools, explore what’s coming and keep up with Red-XAI.':'One account for the Red-XAI ecosystem. Explore products, read official updates and open your workspace.'),h('div',{class:'row'},signed?link('Open your databases','/db','button'):link('Sign in','/signin','button'),link('Explore products','/products/software','button secondary'))),h('div',{class:'workspace-panel'},h('header',null,h('span',null,'YOUR WORKSPACE'),h('span',null,signed?'SIGNED IN':'ACCOUNT REQUIRED')),items))),h('section',{class:'wrap section'},h('div',{class:'section-heading'},h('div',null,h('h2',null,'Built for the Red-XAI ecosystem'),h('p',null,'Software coming soon to the store. Documentation is ready to explore.')),link('All software →','/products/software')),software.length?h('div',{class:'grid three'},...software.map(productCard)):alert(state.catalogError||'Product documentation is loading.')),h('section',{class:'wrap section'},h('div',{class:'section-heading'},h('div',null,h('h2',null,'From Red-XAI'),h('p',null,'Announcements, updates and upcoming maintenance.')),link('All announcements →','/news/announcements')),news));
  request('/api/site/posts?category=announcements').then(data=>{if(!ctx.current())return;const posts=(data.posts||[]).slice(0,2);if(posts.length)news.replaceChildren(...posts.map(post=>h('article',{class:'card'},h('p',{class:'post-meta'},formatDate(post.publishedAt,ctx.preferences)),h('h3',null,post.title),link('Read announcement →','/news/announcements'))));}).catch(()=>{});
  node.insertBefore(h('section',{class:'wrap section grid two'},h('article',{class:'card'},h('h2',null,'Vortex'),h('p',null,'Explore current desktop releases and supported downloads.'),link('Open Vortex','/vortex','button secondary')),h('article',{class:'card'},h('h2',null,'Cloud PC'),h('p',null,'View availability for your private cloud desktop workspace.'),link('Open Cloud PC','/cloud-pc','button secondary'))),node.children[1]);
  return {node};
}
async function renderRoute(){
  const mine=++epoch;page?.dispose?.();page=null;main.replaceChildren(loading());const ctx=context(mine),path=location.pathname.replace(/\/$/u,'')||'/';let result;
  try{
    if(state.sessionError&&isPrivatePath(path))result={node:h('section',{class:'wrap section'},empty('Account service unavailable','Your session could not be checked. Retry to reopen your private workspace.',button('Retry',()=>refreshSession(),true)))};
    else if(path==='/signin'){
      if(state.sessionError)result={node:h('section',{class:'wrap section'},alert(state.sessionError.message,'warning'),button('Retry account service',()=>refreshSession(),true))};
      else if(ctx.session.signedIn){const next=safeNext(new URL(location.href).searchParams.get('next'));history.replaceState(null,'',next);return renderRoute();}
      else result=createSignIn({current:ctx.current,completed:async()=>{const session=await refreshSession({renderOnChange:false});if(!session?.signedIn)throw new Error(state.sessionError?.message||'Sign-in was accepted, but no website session was confirmed. Retry sign-in.');await navigate(safeNext(new URL(location.href).searchParams.get('next')),true);},verification:identifier=>{state.verificationIdentifier=identifier;navigate('/verify-email',true);}});
    }else if(path==='/verify-email')result=verificationPage({identifier:state.verificationIdentifier,capabilities:state.config.capabilities||{},navigate});
    else if(path==='/')result=await hubPage(ctx);
    else if(path==='/db'||path==='/host')result=workspacePage(path.slice(1),ctx);
    else if(path==='/vortex')result=await vortexPage(ctx);
    else if(path==='/cloud-pc')result=cloudPcPage(ctx);
    else if(path==='/settings')result=await settingsPage(ctx);
    else if(path==='/api-keys')result=keysPage(ctx);
    else if(path==='/products/subscriptions')result=await subscriptionsPage(ctx);
    else if(path==='/products')result={node:h('div',null,pageHead('Products','Explore Red-XAI','Software, developer tools and subscriptions.'),h('section',{class:'wrap section grid three'},...STORES.map(([key,title])=>h('article',{class:'card'},h('h2',null,title),link('Browse →','/products/'+key)))))};
    else if(/^\/products\/[a-z-]+(?:\/[a-z0-9-]+)?$/u.test(path)){
      if(!state.catalog)throw new Error(state.catalogError||'Product documentation could not be loaded.');const [, ,category,slug]=path.split('/');
      if(slug){const product=state.catalog.products.find(p=>p.category===category&&p.slug===slug);result=product?await renderProduct(product,ctx):null;}
      else if(STORES.some(([key])=>key===category))result={node:renderStore(category,state.catalog)};
    }else if(/^\/(news|media|brands)\/[a-z-]+$/u.test(path)){
      const category=path.split('/')[2];if(['announcements','updates','maintenance','videos','podcasts','videos-podcasts','recommended-brands','partnerships'].includes(category))result=await postsPage(category,ctx);
    }else if(path==='/contact')result=contactPage();
    else if(path==='/community')result=communityPage();
    else if(path==='/api/quick-login')result=apiDocsPage();
    else if(path.startsWith('/legal/'))result=legalPage(path.slice(7));
    if(!result)result={node:h('section',{class:'wrap section'},empty('Page not found','Choose a page from the navigation or return to the Hub.',link('Back to Hub','/','button')))};
  }catch(error){result={node:h('section',{class:'wrap section'},alert(error.message,'warning'),button('Retry',renderRoute,true))};}
  if(mine!==epoch){result?.dispose?.();return;}page=result;main.replaceChildren(result.node);const heading=main.querySelector('h1');document.title=(heading?.textContent||'Hub')+' · Red-XAI';if(heading){heading.setAttribute('tabindex','-1');heading.classList.add('route-heading');heading.focus({preventScroll:true});}
}
document.addEventListener('click',event=>{const a=event.target.closest?.('a[data-route]');if(a&&!event.defaultPrevented&&event.button===0&&!event.metaKey&&!event.ctrlKey&&!event.shiftKey&&!event.altKey){event.preventDefault();navigate(a.href);}else if(!header.contains(event.target))closeMenus();});
document.addEventListener('keydown',event=>{if(event.key==='Escape')closeMenus();});
window.addEventListener('popstate',async()=>{disposePage();if(isPrivatePath(location.pathname))await refreshSession({renderOnChange:false});renderHeader();renderRoute();});
window.addEventListener('focus',()=>refreshSession());
window.addEventListener('pageshow',async event=>{if(event.persisted){disposePage();await refreshSession({renderOnChange:false});renderHeader();renderRoute();}});
document.addEventListener('visibilitychange',()=>{if(document.visibilityState==='visible')refreshSession();});
window.addEventListener('pagehide',()=>disposePage());
window.addEventListener('beforeunload',event=>{if(page?.isDirty?.()){event.preventDefault();event.returnValue='';}});
matchMedia('(prefers-color-scheme: light)').addEventListener('change',()=>{if(state.preferences.theme==='system')applyPreferences(state.preferences);});
setInterval(()=>{if(document.visibilityState==='visible')refreshSession();},60000);
async function boot(){
  const results=await Promise.allSettled([request('/api/site/config'),assetJSON('catalog.json'),refreshSession({renderOnChange:false})]);
  if(results[0].status==='fulfilled')state.config=results[0].value;
  if(results[1].status==='fulfilled'&&Array.isArray(results[1].value.products))state.catalog=results[1].value;else state.catalogError='Product documentation is temporarily unavailable. Please retry.';
  state.config.capabilities={...state.config.capabilities,...state.session?.capabilities};renderHeader();renderFooter();await renderRoute();
}boot();
