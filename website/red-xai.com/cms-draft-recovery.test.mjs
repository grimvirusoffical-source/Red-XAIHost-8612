import test from 'node:test';
import assert from 'node:assert/strict';
import {createPostEditor,renderBody} from './site/editor.js';
import {postsPage} from './site/pages.js';
import {createDraftStore} from './site/session.js';
import {notify} from './site/dom.js';

// Exercise production editor/page lifecycles without cancelling traversal or
// manufacturing browser history. All draft content below is synthetic.
class Element {
  constructor(tag,text='') {
    this.nodeType=tag?1:3;this.tagName=tag?.toUpperCase();this.childNodes=[];
    this.attributes={};this.dataset={};this.listeners={};this.parentNode=null;
    this._text=text;this._value=undefined;this.disabled=false;this.hidden=false;
    this.checked=false;this.selected=false;this.className='';
    this.classList={add:(...names)=>this.setClasses([...this.classes(),...names]),remove:(...names)=>this.setClasses(this.classes().filter(x=>!names.includes(x))),contains:name=>this.classes().includes(name)};
  }
  classes(){return this.className.split(/\s+/u).filter(Boolean);}
  setClasses(names){this.className=[...new Set(names)].join(' ');}
  append(...nodes){for(let node of nodes){if(!node||typeof node!=='object')node=new Element(null,String(node));node.remove();node.parentNode=this;this.childNodes.push(node);}}
  replaceChildren(...nodes){for(const child of this.childNodes)child.parentNode=null;this.childNodes=[];this.append(...nodes);}
  remove(){if(this.parentNode){const children=this.parentNode.childNodes,index=children.indexOf(this);if(index>=0)children.splice(index,1);this.parentNode=null;}}
  contains(node){return node===this||this.childNodes.some(child=>child.contains(node));}
  addEventListener(type,callback){(this.listeners[type]||=[]).push(callback);}
  removeEventListener(type,callback){this.listeners[type]=(this.listeners[type]||[]).filter(fn=>fn!==callback);}
  async dispatch(type,extra={}){const event={type,target:this,defaultPrevented:false,preventDefault(){this.defaultPrevented=true;},...extra};for(const callback of this.listeners[type]||[])await callback(event);return event;}
  get children(){return this.childNodes.filter(child=>child.nodeType===1);}
  get textContent(){return this.nodeType===3?this._text:this.childNodes.map(child=>child.textContent).join('');}
  set textContent(value){this.replaceChildren(...(value?[new Element(null,String(value))]:[]));}
  get value(){if(this._value!==undefined)return this._value;if(this.tagName==='SELECT')return (this.children.find(child=>child.selected||child.getAttribute('selected')!==null)||this.children[0])?.value||'';return this.attributes.value||'';}
  set value(value){this._value=String(value);}
  get contentEditable(){return this.getAttribute('contenteditable')||'inherit';}
  set contentEditable(value){this.setAttribute('contenteditable',value);}
  get alt(){return this.getAttribute('alt')||'';}
  set alt(value){this.setAttribute('alt',value);}
  setAttribute(name,value){name=name.toLowerCase();this.attributes[name]=String(value);if(name==='class')this.className=String(value);if(name.startsWith('data-'))this.dataset[name.slice(5).replace(/-([a-z])/gu,(_,char)=>char.toUpperCase())]=String(value);}
  getAttribute(name){return name==='class'?this.className||null:this.attributes[name.toLowerCase()]??null;}
  removeAttribute(name){delete this.attributes[name.toLowerCase()];}
  matches(selector){
    if(selector.startsWith('.'))return this.classes().includes(selector.slice(1));
    const match=/^(?:([a-z][a-z0-9-]*))?(?:\[([\w-]+)(?:=["']?([^"'\]]+)["']?)?\])?$/iu.exec(selector);
    return !!match&&(!match[1]||this.tagName===match[1].toUpperCase())&&(!match[2]||(match[3]===undefined?this.getAttribute(match[2])!==null:this.getAttribute(match[2])===match[3]));
  }
  querySelectorAll(selector){const selectors=selector.split(',').map(value=>value.trim()),result=[];const walk=node=>{for(const child of node.children){if(selectors.some(value=>child.matches(value)))result.push(child);walk(child);}};walk(this);return result;}
  querySelector(selector){return this.querySelectorAll(selector)[0]||null;}
  scrollIntoView(){}
  focus(){}
  showModal(){this.open=true;}
}

const all=root=>{const nodes=[];const visit=node=>{nodes.push(node);for(const child of node.children)visit(child);};visit(root);return nodes;};
const button=(root,label)=>all(root).find(node=>node.tagName==='BUTTON'&&node.textContent===label);
const flush=async()=>{for(let index=0;index<5;index++)await Promise.resolve();};
const response=(data,status=200)=>({ok:status>=200&&status<300,status,json:async()=>data});
const deferred=()=>{let resolve,reject;const promise=new Promise((yes,no)=>{resolve=yes;reject=no;});return {promise,resolve,reject};};
const owner=(id='owner-A',expiresMs=Date.now()+60000)=>({signedIn:true,owner:true,expiresMs,account:{id,email:id+'@example.test'}});
const mediaId='a'.repeat(32);
const richBody={type:'doc',children:[{type:'paragraph',children:[{type:'text',text:'Before ',marks:['bold']},{type:'link',href:'https://example.test/docs',children:[{type:'text',text:'docs'}]}]},{type:'image',mediaId,alt:'Uploaded image',caption:'Draft caption'},{type:'paragraph',children:[{type:'text',text:'After image'}]}]};
const draftSnapshot=()=>({post:null,title:'Unsent maintenance',category:'maintenance',body:structuredClone(richBody),starts:'2026-11-04T16:35',ends:'2026-11-04T17:35'});

async function withDOM(run){
  const names=['document','window','navigator','fetch'],before=new Map(names.map(name=>[name,Object.getOwnPropertyDescriptor(globalThis,name)]));
  const notice=new Element('div');
  const document={createElement:tag=>new Element(tag),createTextNode:text=>new Element(null,String(text)),getElementById:id=>id==='notice'?notice:null,body:new Element('body'),execCommand:()=>{throw new Error('Formatting must not execute while a save is pending.');}};
  const window={getSelection:()=>({rangeCount:0,anchorNode:null}),confirm:()=>true,prompt:()=>null};
  for(const [name,value]of Object.entries({document,window,navigator:{},fetch:async()=>response({posts:[]})}))Object.defineProperty(globalThis,name,{configurable:true,writable:true,value});
  try{return await run({document,window});}finally{clearTimeout(notify.timer);delete notify.timer;for(const name of names){const descriptor=before.get(name);if(descriptor)Object.defineProperty(globalThis,name,descriptor);else delete globalThis[name];}}
}

function fields(root){return {title:root.querySelector('input[type="text"]'),category:root.querySelector('select'),body:root.querySelector('[contenteditable]'),dates:root.querySelectorAll('input[type="datetime-local"]')};}
async function edit(root,snapshot=draftSnapshot()){
  const controls=fields(root);assert.ok(controls.title);assert.ok(controls.body);
  controls.title.value=snapshot.title;await controls.title.dispatch('input');
  controls.category.value=snapshot.category;await controls.category.dispatch('change');
  controls.body.replaceChildren(...renderBody(snapshot.body,{editing:true}).childNodes);await controls.body.dispatch('input');
  controls.dates[0].value=snapshot.starts;await controls.dates[0].dispatch('change');
  controls.dates[1].value=snapshot.ends;await controls.dates[1].dispatch('change');
}
function context(store,{session=owner(),verify=async()=>true}={}){return {session,config:{capabilities:{media:true}},preferences:{reducedMotion:true},current:()=>true,drafts:category=>store.lease(category),verifyDraftSession:verify};}

function cameraFixture(document){
  const createElement=document.createElement,blobs=[],stops=[0,0];let requests=0;
  document.createElement=tag=>{const element=createElement(tag);if(tag==='canvas'){element.getContext=()=>({drawImage(){}});element.toBlob=callback=>{blobs.push(callback);};}return element;};
  navigator.mediaDevices={getUserMedia:async()=>{const index=requests++;assert.ok(index<stops.length,'unexpected extra camera request');return {getTracks:()=>[{stop:()=>{stops[index]++;}}]};}};
  const open=async root=>{await button(root,'Camera').dispatch('click');const dialog=document.body.querySelector('dialog');assert.ok(dialog);const video=dialog.querySelector('video');video.videoWidth=640;video.videoHeight=480;return dialog;};
  return {blobs,stops,open};
}

test('real editor restores AST, image IDs and local schedule values and PATCHes the existing post',async()=>withDOM(async()=>{
  const snapshot=draftSnapshot();snapshot.post={id:'existing-post-1',status:'published'};
  let captured,startSnapshot,settled,saved;
  globalThis.fetch=async(path,options)=>{captured={path,method:options.method,body:JSON.parse(options.body)};return response({post:{id:'existing-post-1',status:'draft'}});};
  const editor=createPostEditor({category:'maintenance',post:{id:'existing-post-1',status:'published'},initialDraft:snapshot,onSaved:post=>{saved=post;},onCancel:()=>{},onSaveStart:value=>{startSnapshot=value;},onSaveSettled:value=>{settled=value;}});
  assert.deepEqual(editor.getSnapshot(),snapshot);
  assert.equal(fields(editor.node).dates[0].value,snapshot.starts);assert.equal(fields(editor.node).dates[1].value,snapshot.ends);
  await button(editor.node,'Save draft').dispatch('click');
  assert.equal(captured.path,'/api/site/posts/existing-post-1');assert.equal(captured.method,'PATCH');
  assert.deepEqual(captured.body.body,snapshot.body);assert.equal(captured.body.startsAt,new Date(snapshot.starts).toISOString());assert.equal(captured.body.endsAt,new Date(snapshot.ends).toISOString());
  assert.deepEqual(startSnapshot,snapshot);assert.equal(settled,true);assert.equal(saved.id,'existing-post-1');assert.equal(editor.isSaving(),false);editor.dispose();
}));

test('an image upload is not a pending post save and its late result cannot rewrite a disposed editor',async()=>withDOM(async()=>{
  const pending=deferred();let starts=0,settles=0,writes=0;
  globalThis.fetch=async(path)=>{assert.equal(path,'/api/site/media');writes++;return pending.promise;};
  const editor=createPostEditor({category:'maintenance',onSaved:()=>{},onCancel:()=>{},onSaveStart:()=>{starts++;},onSaveSettled:()=>{settles++;}});
  await edit(editor.node);const before=editor.getSnapshot(),file=editor.node.querySelector('input[type="file"]');
  file.files=[{type:'image/png',size:1,name:'synthetic.png'}];await file.dispatch('change');await flush();
  assert.equal(writes,1);assert.equal(editor.isSaving(),false);assert.equal(editor.isDirty(),true);assert.deepEqual(editor.getSnapshot(),before);
  editor.dispose();pending.resolve(response({media:{id:'b'.repeat(32)}}));await flush();
  assert.equal(starts,0);assert.equal(settles,0);assert.equal(fields(editor.node).body.childNodes.length,0);
}));

test('a deferred camera stops when save begins and queued toolbar clicks cannot reopen it or prompt',async()=>withDOM(async({document,window})=>{
  const media=deferred(),post=deferred(),stopped=[0,0];let cameraCalls=0,prompts=0,cameraTask,saveTask,queuedCamera,queuedLink;
  const stream={getTracks:()=>stopped.map((_,index)=>({stop:()=>{stopped[index]++;}}))};
  navigator.mediaDevices={getUserMedia:()=>{cameraCalls++;return media.promise;}};
  window.prompt=()=>{prompts++;return null;};
  globalThis.fetch=async(path)=>{assert.equal(path,'/api/site/posts');return post.promise;};
  const editor=createPostEditor({category:'maintenance',onSaved:()=>{},onCancel:()=>{}});
  try{
    await edit(editor.node);cameraTask=button(editor.node,'Camera').dispatch('click');await flush();assert.equal(cameraCalls,1);
    saveTask=button(editor.node,'Save draft').dispatch('click');await flush();assert.equal(editor.isSaving(),true);
    queuedCamera=button(editor.node,'Camera').dispatch('click');queuedLink=button(editor.node,'Link').dispatch('click');await flush();
    assert.equal(cameraCalls,1,'a queued camera click must not request another stream while saving');assert.equal(prompts,0,'a queued link click must not open a prompt while saving');
    media.resolve(stream);await Promise.all([cameraTask,queuedCamera,queuedLink]);await flush();
    assert.deepEqual(stopped,[1,1],'every late camera track must stop before the post save settles');assert.equal(document.body.querySelector('dialog'),null);assert.equal(editor.isSaving(),true);
    post.resolve(response({post:{id:'camera-race-save',status:'draft'}}));await saveTask;assert.equal(editor.isSaving(),false);
  }finally{
    media.resolve(stream);post.resolve(response({post:{id:'camera-race-save',status:'draft'}}));
    await Promise.allSettled([cameraTask,saveTask,queuedCamera,queuedLink].filter(Boolean));editor.dispose();
  }
}));

test('camera permission issued before a failed save stays invalid after the editor unfreezes',async()=>withDOM(async({document})=>{
  const media=deferred(),post=deferred();let stopped=0,settled,cameraTask,saveTask;
  const stream={getTracks:()=>[{stop:()=>{stopped++;}}]};navigator.mediaDevices={getUserMedia:()=>media.promise};
  globalThis.fetch=async(path)=>{assert.equal(path,'/api/site/posts');return post.promise;};
  const editor=createPostEditor({category:'maintenance',onSaved:()=>{},onCancel:()=>{},onSaveSettled:success=>{settled=success;}});
  try{
    await edit(editor.node);cameraTask=button(editor.node,'Camera').dispatch('click');await flush();
    saveTask=button(editor.node,'Save draft').dispatch('click');await flush();assert.equal(editor.isSaving(),true);
    post.resolve(response({error:'simulated_save_failure'},503));await saveTask;assert.equal(editor.isSaving(),false);assert.equal(settled,false);
    media.resolve(stream);await cameraTask;await flush();
    assert.equal(stopped,1,'a pre-save camera permission request must remain invalid after save failure');assert.equal(document.body.querySelector('dialog'),null);
  }finally{
    media.resolve(stream);post.resolve(response({error:'simulated_save_failure'},503));await Promise.allSettled([cameraTask,saveTask].filter(Boolean));editor.dispose();
  }
}));

test('a cancelled capture and stale dialog events cannot stop a newer camera or upload the old image',async()=>withDOM(async({document})=>{
  const camera=cameraFixture(document);let uploads=0;
  globalThis.fetch=async(path)=>{assert.equal(path,'/api/site/media');uploads++;return response({media:{id:'b'.repeat(32)}});};
  const editor=createPostEditor({category:'maintenance',onSaved:()=>{},onCancel:()=>{}});
  try{
    await edit(editor.node);const oldDialog=await camera.open(editor.node);await button(oldDialog,'Use photo').dispatch('click');assert.equal(camera.blobs.length,1);
    await button(oldDialog,'Cancel').dispatch('click');assert.deepEqual(camera.stops,[1,0]);
    const currentDialog=await camera.open(editor.node);assert.notEqual(currentDialog,oldDialog);
    await oldDialog.dispatch('close');await oldDialog.dispatch('cancel');assert.deepEqual(camera.stops,[1,0],'stale dialog events must not stop the current camera');
    camera.blobs[0](new Blob(['synthetic image'],{type:'image/webp'}));await flush();
    assert.equal(uploads,0,'the cancelled canvas callback must not upload its old image');assert.deepEqual(camera.stops,[1,0]);assert.equal(document.body.querySelector('dialog'),currentDialog);
  }finally{editor.dispose();}
}));

test('a pre-save canvas callback stays invalid after save failure and cannot stop a newly opened camera',async()=>withDOM(async({document})=>{
  const camera=cameraFixture(document),pending=deferred();let uploads=0,saveTask;
  globalThis.fetch=async(path)=>{if(path==='/api/site/media'){uploads++;return response({media:{id:'b'.repeat(32)}});}assert.equal(path,'/api/site/posts');return pending.promise;};
  const editor=createPostEditor({category:'maintenance',onSaved:()=>{},onCancel:()=>{}});
  try{
    await edit(editor.node);const oldDialog=await camera.open(editor.node);await button(oldDialog,'Use photo').dispatch('click');assert.equal(camera.blobs.length,1);
    saveTask=button(editor.node,'Save draft').dispatch('click');await flush();assert.equal(editor.isSaving(),true);assert.deepEqual(camera.stops,[1,0]);
    pending.resolve(response({error:'simulated_save_failure'},503));await saveTask;assert.equal(editor.isSaving(),false);
    const currentDialog=await camera.open(editor.node);camera.blobs[0](new Blob(['synthetic old image'],{type:'image/webp'}));await flush();
    await oldDialog.dispatch('close');await oldDialog.dispatch('cancel');
    assert.equal(uploads,0,'the canvas callback invalidated by saving must never upload later');assert.deepEqual(camera.stops,[1,0]);assert.equal(document.body.querySelector('dialog'),currentDialog);
  }finally{pending.resolve(response({error:'simulated_save_failure'},503));if(saveTask)await saveTask;editor.dispose();}
}));

test('Back/Forward-style disposal retains a complete draft until explicit restore or discard',async()=>withDOM(async()=>{
  const store=createDraftStore();store.setSession(owner());const ctx=context(store);
  let page=await postsPage('maintenance',ctx);await flush();await button(page.node,'Create post').dispatch('click');await edit(page.node);
  assert.equal(page.isDirty(),true);page.dispose();
  const retained=store.lease('maintenance').peek();assert.deepEqual(retained.snapshot,draftSnapshot());assert.equal(retained.pending,false);
  page=await postsPage('maintenance',ctx);await flush();assert.ok(button(page.node,'Restore draft'));assert.equal(page.node.querySelector('.post-editor'),null);
  await button(page.node,'Restore draft').dispatch('click');await flush();assert.ok(page.node.querySelector('.post-editor'));
  assert.equal(fields(page.node).title.value,draftSnapshot().title);assert.equal(fields(page.node).dates[0].value,draftSnapshot().starts);assert.equal(page.isDirty(),true);
  await button(page.node,'Cancel').dispatch('click');assert.equal(store.lease('maintenance').peek(),null);page.dispose();
  page=await postsPage('maintenance',ctx);await flush();assert.equal(button(page.node,'Restore draft'),undefined);assert.equal(page.node.querySelector('.post-editor'),null);page.dispose();
}));

test('discarding a retained draft never resurrects it on a later page disposal',async()=>withDOM(async()=>{
  const store=createDraftStore();store.setSession(owner());const ctx=context(store);
  let page=await postsPage('maintenance',ctx);await flush();await button(page.node,'Create post').dispatch('click');await edit(page.node);page.dispose();
  page=await postsPage('maintenance',ctx);await flush();await button(page.node,'Discard draft').dispatch('click');assert.equal(store.lease('maintenance').peek(),null);page.dispose();
  page=await postsPage('maintenance',ctx);await flush();assert.equal(button(page.node,'Restore draft'),undefined);assert.equal(store.hasAny(),false);page.dispose();
}));

test('pending save freezes editing and late success clears retained state without a duplicate POST',async()=>withDOM(async()=>{
  const store=createDraftStore();store.setSession(owner());const ctx=context(store),pending=deferred();let writes=0;
  globalThis.fetch=async(path,options)=>{if(options.method==='POST'){writes++;return pending.promise;}return response({posts:[]});};
  let page=await postsPage('maintenance',ctx);await flush();await button(page.node,'Create post').dispatch('click');await edit(page.node);
  const originalEditor=page.node.querySelector('.post-editor'),save=button(originalEditor,'Save draft').dispatch('click');await flush();
  for(const control of originalEditor.querySelectorAll('input,select,button'))assert.equal(control.disabled,true,control.tagName+' should be frozen during save');
  const editable=fields(originalEditor).body;assert.equal(editable.getAttribute('contenteditable'),'false');
  await button(originalEditor,'B').dispatch('click');await editable.dispatch('paste',{clipboardData:{files:[],getData:()=> 'must not insert'}});await editable.dispatch('drop',{dataTransfer:{files:[],getData:()=> 'must not insert'}});
  assert.equal(writes,1);page.dispose();assert.equal(store.lease('maintenance').peek().pending,true);
  page=await postsPage('maintenance',ctx);await flush();const restore=button(page.node,'Restore draft');assert.ok(!restore||restore.disabled);const create=button(page.node,'Create post');if(create)await create.dispatch('click');assert.equal(page.node.querySelector('.post-editor'),null);assert.equal(writes,1);
  pending.resolve(response({post:{id:'saved-once',status:'draft'}}));await save;await flush();assert.equal(writes,1);assert.equal(store.lease('maintenance').peek(),null);assert.equal(button(page.node,'Restore draft'),undefined);page.dispose();
  page=await postsPage('maintenance',ctx);await flush();assert.equal(button(page.node,'Restore draft'),undefined);assert.equal(writes,1);page.dispose();
}));

test('late post success refreshes the reentered category after its earlier empty list request completed',async()=>withDOM(async()=>{
  const store=createDraftStore();store.setSession(owner());const ctx=context(store),pending=deferred();let writes=0,reads=0,posts=[];
  const saved={id:'late-visible-post',status:'draft',category:'maintenance',title:'Late maintenance entry',body:structuredClone(richBody),createdAt:'2026-11-04T20:00:00.000Z'};
  globalThis.fetch=async(path,options)=>{
    if(options.method==='POST'){writes++;return pending.promise;}
    assert.match(path,/^\/api\/site\/posts\?category=maintenance/u);reads++;return response({posts:structuredClone(posts)});
  };
  let page=await postsPage('maintenance',ctx);await flush();await button(page.node,'Create post').dispatch('click');await edit(page.node);
  const save=button(page.node,'Save draft').dispatch('click');await flush();page.dispose();
  page=await postsPage('maintenance',ctx);await flush();const readsBeforeCommit=reads;
  assert.equal(readsBeforeCommit,2);assert.match(page.node.textContent,/No posts yet/u);assert.equal(page.node.textContent.includes(saved.title),false);
  posts=[saved];pending.resolve(response({post:saved}));await save;await flush();
  assert.equal(writes,1);assert.ok(reads>readsBeforeCommit,'the current category must refetch after the pending POST succeeds');
  assert.ok(page.node.textContent.includes(saved.title),'the refreshed list must render the saved post');assert.match(document.getElementById('notice').textContent,/saved/iu);assert.equal(store.lease('maintenance').peek(),null);page.dispose();
}));

test('a late failed save remains restorable and never silently retries the request',async()=>withDOM(async()=>{
  const store=createDraftStore();store.setSession(owner());const ctx=context(store),pending=deferred();let writes=0;
  globalThis.fetch=async(path,options)=>{if(options.method==='POST'){writes++;return pending.promise;}return response({posts:[]});};
  let page=await postsPage('maintenance',ctx);await flush();await button(page.node,'Create post').dispatch('click');await edit(page.node);
  const save=button(page.node,'Save draft').dispatch('click');await flush();page.dispose();
  page=await postsPage('maintenance',ctx);await flush();pending.reject(new Error('simulated transport failure'));await save;await flush();
  const entry=store.lease('maintenance').peek();assert.equal(entry.pending,false);assert.equal(entry.failed,true);assert.deepEqual(entry.snapshot,draftSnapshot());assert.equal(writes,1);
  assert.match(page.node.textContent,/check|confirm|uncertain|may have|not confirmed/iu);assert.ok(button(page.node,'Restore draft'));
  await button(page.node,'Restore draft').dispatch('click');await flush();assert.equal(fields(page.node).title.value,draftSnapshot().title);assert.equal(writes,1);page.dispose();
}));

test('restore requires a fresh owner session and old generations cannot leak a draft to another account',async()=>withDOM(async()=>{
  let now=Date.now();const store=createDraftStore({now:()=>now}),session=owner('owner-A',now+1000);store.setSession(session);
  let allowed=true,checks=0;const ctx=context(store,{session,verify:async()=>{checks++;return allowed;}});
  let page=await postsPage('maintenance',ctx);await flush();await button(page.node,'Create post').dispatch('click');await edit(page.node);page.dispose();
  page=await postsPage('maintenance',ctx);await flush();const verifiedBeforeRestore=checks;assert.ok(verifiedBeforeRestore>=1);allowed=false;await button(page.node,'Restore draft').dispatch('click');await flush();assert.equal(checks,verifiedBeforeRestore+1);assert.equal(page.node.querySelector('.post-editor'),null);
  now+=1001;assert.equal(store.lease('maintenance').peek(),null);assert.equal(store.hasAny(),false);page.dispose();
  store.setSession(owner('owner-B',now+60000));page=await postsPage('maintenance',context(store,{session:owner('owner-B',now+60000)}));await flush();assert.equal(button(page.node,'Restore draft'),undefined);page.dispose();
  store.setSession({...owner('owner-A',now+60000),owner:false});page=await postsPage('maintenance',context(store,{session:{...owner('owner-A',now+60000),owner:false}}));await flush();assert.equal(button(page.node,'Restore draft'),undefined);assert.equal(button(page.node,'Create post'),undefined);page.dispose();
}));
