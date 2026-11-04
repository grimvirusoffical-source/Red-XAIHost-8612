import {h,button,field,alert,safeHref,notify} from './dom.js';
import {request} from './api.js';

export const CATEGORIES=Object.freeze({announcements:'Announcements',updates:'Updates',maintenance:'Maintenance',videos:'Upcoming videos',podcasts:'Upcoming podcasts','videos-podcasts':'Videos & podcasts','recommended-brands':'Brands we recommend',partnerships:'Partnerships'});
export const validMediaId=id=>typeof id==='string'&&/^[a-f0-9]{32}$/u.test(id);
const marks={STRONG:'bold',B:'bold',EM:'italic',I:'italic',U:'underline',S:'strike',STRIKE:'strike',DEL:'strike',CODE:'code'};
const ignored=new Set(['SCRIPT','STYLE','IFRAME','OBJECT','EMBED','SVG','MATH','TEMPLATE']);
function inlineNodes(nodes,inherited=[]){
  const output=[];
  for(const node of nodes){
    if(node.nodeType===3){if(node.textContent)output.push({type:'text',text:node.textContent,...(inherited.length?{marks:[...inherited]}:{})});continue;}
    if(node.nodeType!==1||ignored.has(node.tagName)||node.tagName==='IMG')continue;
    if(node.tagName==='BR'){output.push({type:'text',text:'\n',...(inherited.length?{marks:[...inherited]}:{})});continue;}
    const next=[...new Set([...inherited,...(marks[node.tagName]?[marks[node.tagName]]:[])])];
    const children=inlineNodes(node.childNodes,next);
    if(node.tagName==='A'&&safeHref(node.getAttribute('href'))!=='#'){
      // Nested links are flattened before entering the strict AST.
      output.push({type:'link',href:safeHref(node.getAttribute('href')),children:children.flatMap(x=>x.type==='link'?x.children:[x])});
    }else output.push(...children);
  }
  return output;
}
function imageBlock(node){const mediaId=node.dataset.mediaId;return validMediaId(mediaId)?{type:'image',mediaId,alt:node.alt||'',...(node.dataset.caption?{caption:node.dataset.caption}:{})}:null;}
// Split at images, even when the browser put one inside a paragraph or list item.
function splitInline(nodes){
  const parts=[];let buffer=[];
  const flush=()=>{if(buffer.length){parts.push({inline:inlineNodes(buffer)});buffer=[];}};
  for(const node of nodes){
    if(node.nodeType===1&&node.tagName==='IMG'){const image=imageBlock(node);if(image){flush();parts.push({image});}}
    else if(node.nodeType===1&&node.querySelector('img')){
      flush();for(const part of splitInline(node.childNodes)){
        if(part.inline&&marks[node.tagName])part.inline=part.inline.map(x=>x.type==='text'?{...x,marks:[...new Set([...(x.marks||[]),marks[node.tagName]])]}:x);
        parts.push(part);
      }
    }else buffer.push(node);
  }
  flush();return parts;
}
export function serializeBody(root){
  const children=[];let loose=[];
  const add=(parts,type='paragraph',level)=>{for(const part of parts){if(part.image)children.push(part.image);else if(part.inline.length)children.push({type,children:part.inline,...(level?{level}:{})});}};
  const flush=()=>{add(splitInline(loose));loose=[];};
  const walk=nodes=>{for(const node of nodes){
    if(node.nodeType!==1){loose.push(node);continue;}if(ignored.has(node.tagName))continue;
    if(['P','DIV','H1','H2','H3','H4','H5','H6','BLOCKQUOTE','PRE','UL','OL','IMG'].includes(node.tagName)){
      flush();
      if(node.tagName==='IMG'){const image=imageBlock(node);if(image)children.push(image);}
      else if(node.tagName==='PRE')children.push({type:'codeBlock',text:node.textContent||''});
      else if(['UL','OL'].includes(node.tagName)){
        let items=[];const emit=()=>{if(items.length){children.push({type:'list',ordered:node.tagName==='OL',items});items=[];}};
        for(const li of node.children){for(const part of splitInline(li.childNodes)){if(part.image){emit();children.push(part.image);}else if(part.inline.length)items.push(part.inline);}}emit();
      }else if(node.tagName==='DIV'&&Array.from(node.children).some(x=>['P','DIV','UL','OL'].includes(x.tagName)))walk(node.childNodes);
      else add(splitInline(node.childNodes),node.tagName==='BLOCKQUOTE'?'blockquote':/^H[1-6]$/u.test(node.tagName)?'heading':'paragraph',/^H[1-6]$/u.test(node.tagName)?Math.max(2,Math.min(4,Number(node.tagName[1]))):undefined);
    }else loose.push(node);
  }};walk(root.childNodes);flush();return {type:'doc',children};
}
export function bodyHasContent(body){return body?.children?.some(x=>x.type==='image'||x.type==='codeBlock'&&x.text.trim()||x.type==='list'&&x.items.some(y=>y.some(z=>z.text?.trim()||z.children?.some(t=>t.text?.trim())))||x.children?.some(y=>y.text?.trim()||y.children?.some(z=>z.text?.trim())));}
function renderInline(items){return (items||[]).map(item=>{
  if(item.type==='link')return h('a',{href:safeHref(item.href),rel:'noopener noreferrer'},...renderInline((item.children||[]).filter(x=>x.type==='text')));
  let node=document.createTextNode(String(item.text||''));
  for(const mark of item.marks||[]){const tag={bold:'strong',italic:'em',underline:'u',strike:'s',code:'code'}[mark];if(tag)node=h(tag,null,node);}return node;
});}
export function renderBody(body,{editing=false}={}){
  const root=h('div',{class:'post-body'});
  for(const block of body?.type==='doc'&&Array.isArray(body.children)?body.children:[]){
    let node;
    if(block.type==='image'&&validMediaId(block.mediaId)){
      const image=h('img',{src:'/api/site/media/'+block.mediaId,alt:block.alt||'',loading:editing?'eager':'lazy','data-media-id':block.mediaId,'data-caption':block.caption||''});
      node=editing?image:h('figure',null,image,block.caption?h('figcaption',null,block.caption):null);
    }else if(block.type==='codeBlock')node=h('pre',null,h('code',null,block.text||''));
    else if(block.type==='list')node=h(block.ordered?'ol':'ul',null,...(block.items||[]).map(items=>h('li',null,...renderInline(items))));
    else if(['paragraph','heading','blockquote'].includes(block.type))node=h(block.type==='heading'?'h'+Math.max(2,Math.min(4,Number(block.level)||2)):block.type==='blockquote'?'blockquote':'p',null,...renderInline(block.children));
    if(node)root.append(node);
  }
  return root;
}
const localDate=value=>value?new Date(Date.parse(value)-new Date(value).getTimezoneOffset()*60000).toISOString().slice(0,16):'';
export function createPostEditor({category,post,onSaved,onCancel,active=()=>true,mediaEnabled=true}){
  let disposed=false,range=null,stream=null,cameraDialog=null,busy=false,dirty=false;
  const alive=()=>!disposed&&active();
  const status=h('div',{'aria-live':'polite'}),title=h('input',{type:'text',required:true,maxlength:180,value:post?.title||'',placeholder:'Give this post a title'});
  const categoryInput=h('select',null,...Object.entries(CATEGORIES).map(([key,label])=>h('option',{value:key,selected:key===(post?.category||category)},label)));
  const editor=h('div',{class:'editor',contenteditable:'true',role:'textbox','aria-label':'Post body','aria-multiline':'true','data-placeholder':'Write your post…'});
  editor.replaceChildren(...renderBody(post?.body,{editing:true}).childNodes);
  const remember=()=>{const selection=window.getSelection();if(selection?.rangeCount&&editor.contains(selection.anchorNode))range=selection.getRangeAt(0).cloneRange();};
  const restore=(target=range)=>{editor.focus();const s=window.getSelection();s.removeAllRanges();if(target&&editor.contains(target.commonAncestorContainer))s.addRange(target);else{const r=document.createRange();r.selectNodeContents(editor);r.collapse(false);s.addRange(r);}};
  const changed=()=>{dirty=true;remember();};
  for(const event of ['keyup','mouseup','touchend','blur'])editor.addEventListener(event,remember);
  editor.addEventListener('input',changed);title.addEventListener('input',changed);categoryInput.addEventListener('change',changed);
  const toolbar=h('div',{class:'editor-toolbar',role:'toolbar','aria-label':'Post formatting'});
  const tool=(label,action,description=label)=>{const b=h('button',{type:'button',title:description,'aria-label':description,onMousedown:e=>{e.preventDefault();remember();},onClick:action},label);toolbar.append(b);return b;};
  const command=(name,value)=>{restore();document.execCommand('styleWithCSS',false,false);document.execCommand(name,false,value);changed();};
  for(const [label,name,desc] of [['B','bold','Bold'],['I','italic','Italic'],['U','underline','Underline'],['S','strikeThrough','Strikethrough'],['↶','undo','Undo'],['↷','redo','Redo']])tool(label,()=>command(name),desc);
  for(const [label,tag] of [['Paragraph','p'],['H2','h2'],['H3','h3'],['H4','h4'],['Quote','blockquote'],['Code','pre']])tool(label,()=>command('formatBlock',tag));
  tool('• List',()=>command('insertUnorderedList'),'Bulleted list');tool('1. List',()=>command('insertOrderedList'),'Numbered list');
  tool('Link',()=>{const value=window.prompt('Link address (https://… or a page on this site)');if(value===null)return;if(safeHref(value)==='#'){notify('Enter a valid web link.');return;}command('createLink',safeHref(value));});
  tool('Unlink',()=>command('unlink'),'Remove link');tool('Clear',()=>command('removeFormat'),'Clear text formatting');
  const insertText=text=>{restore();const s=window.getSelection(),r=s.getRangeAt(0);r.deleteContents();const node=document.createTextNode(text);r.insertNode(node);r.setStartAfter(node);r.collapse(true);s.removeAllRanges();s.addRange(r);changed();};
  const upload=async file=>{
    if(!alive())return;
    if(busy){notify('Wait for the current image to finish uploading.');return;}
    if(!mediaEnabled){notify('Image uploads are temporarily unavailable.');return;}
    if(!['image/jpeg','image/png','image/webp','image/gif'].includes(file.type)){notify('Choose a JPEG, PNG, WebP or GIF.');return;}
    if(file.size>10*1024*1024){notify('Choose an image smaller than 10 MB.');return;}
    const uploadRange=range?.cloneRange();busy=true;status.replaceChildren(alert('Uploading and checking image…'));
    try{const data=await request('/api/site/media',{method:'POST',body:file,headers:{'content-type':file.type}});if(!alive())return;
      if(!validMediaId(data.media?.id))throw new Error('The service did not return a valid image.');
      restore(uploadRange);const s=window.getSelection(),r=s.getRangeAt(0);r.deleteContents();const img=h('img',{src:'/api/site/media/'+data.media.id,alt:file.name&&file.name!=='camera.webp'?file.name:'Photo','data-media-id':data.media.id});r.insertNode(img);r.setStartAfter(img);r.collapse(true);s.removeAllRanges();s.addRange(r);changed();status.replaceChildren(alert('Image inserted at your cursor.','success'));
    }catch(e){if(alive())status.replaceChildren(alert(e.message,'warning'));}finally{busy=false;}
  };
  const file=h('input',{type:'file',accept:'image/jpeg,image/png,image/webp,image/gif',class:'inline-file','aria-label':'Upload a post image',onChange:()=>{if(file.files[0])upload(file.files[0]);file.value='';}});
  tool('Image',()=>{remember();file.click();},'Upload image at cursor');
  const stopCamera=()=>{if(stream){stream.getTracks().forEach(track=>track.stop());stream=null;}if(cameraDialog){cameraDialog.remove();cameraDialog=null;}};
  tool('Camera',async()=>{
    remember();if(!mediaEnabled){notify('Image uploads are temporarily unavailable.');return;}if(!navigator.mediaDevices?.getUserMedia){notify('Camera access is unavailable. Use Image to upload a photo.');return;}
    try{stream=await navigator.mediaDevices.getUserMedia({video:{facingMode:'environment'},audio:false});if(!alive()){stopCamera();return;}
      const video=h('video',{autoplay:true,playsinline:true,muted:true});video.srcObject=stream;
      const capture=button('Use photo',()=>{if(!video.videoWidth)return;const canvas=document.createElement('canvas');canvas.width=video.videoWidth;canvas.height=video.videoHeight;canvas.getContext('2d').drawImage(video,0,0);canvas.toBlob(blob=>{stopCamera();if(blob&&alive())upload(new File([blob],'camera.webp',{type:'image/webp'}));},'image/webp',.9);});
      cameraDialog=h('dialog',{'aria-label':'Take a post photo',onClose:stopCamera,onCancel:stopCamera},h('h2',null,'Take a photo'),video,h('div',{class:'row'},capture,button('Cancel',stopCamera,true)));document.body.append(cameraDialog);cameraDialog.showModal();
    }catch(e){stopCamera();if(alive())notify(e.name==='NotAllowedError'?'Camera permission was declined. You can upload a photo instead.':'The camera could not be opened. You can upload a photo instead.');}
  },'Take photo at cursor');
  editor.addEventListener('paste',e=>{e.preventDefault();remember();const images=Array.from(e.clipboardData?.files||[]).filter(x=>x.type.startsWith('image/'));if(images.length){upload(images[0]);if(images.length>1)notify('Insert additional images one at a time.');}else insertText(e.clipboardData?.getData('text/plain')||'');});
  editor.addEventListener('dragover',e=>e.preventDefault());editor.addEventListener('drop',e=>{e.preventDefault();remember();if(e.dataTransfer?.files[0])upload(e.dataTransfer.files[0]);else insertText(e.dataTransfer?.getData('text/plain')||'');});
  const starts=h('input',{type:'datetime-local',value:localDate(post?.startsAt)}),ends=h('input',{type:'datetime-local',value:localDate(post?.endsAt)});
  const deviceZone=Intl.DateTimeFormat().resolvedOptions().timeZone||'Your device time zone',schedulePreview=h('p',{class:'muted',role:'status'});
  const previewSchedule=()=>{dirty=true;schedulePreview.textContent=[starts.value?'Starts: '+new Date(starts.value).toISOString():null,ends.value?'Ends: '+new Date(ends.value).toISOString():null].filter(Boolean).join(' · ');};
  starts.addEventListener('change',previewSchedule);ends.addEventListener('change',previewSchedule);
  const schedule=h('div',null,h('div',{class:'form-grid'},field('Starts',starts),field('Ends (optional)',ends),field('Entry time zone',h('input',{type:'text',readonly:true,value:deviceZone}))),h('p',null,'Enter times in '+deviceZone+'. They are saved as absolute UTC timestamps; readers see their selected time zone.'),schedulePreview);
  const scheduleVisibility=()=>{schedule.hidden=categoryInput.value!=='maintenance';};categoryInput.addEventListener('change',scheduleVisibility);scheduleVisibility();
  const save=async state=>{
    if(busy)return;const body=serializeBody(editor);if(!title.value.trim()||!bodyHasContent(body)){status.replaceChildren(alert('Add a title and a body before saving.','warning'));return;}
    if(state==='published'&&categoryInput.value==='maintenance'&&!starts.value){status.replaceChildren(alert('Set a start date and time before publishing maintenance.','warning'));return;}
    if(categoryInput.value==='maintenance'&&starts.value&&ends.value&&Date.parse(ends.value)<=Date.parse(starts.value)){status.replaceChildren(alert('The end must be after the start.','warning'));return;}
    busy=true;for(const b of actions.querySelectorAll('button'))b.disabled=true;status.replaceChildren(alert('Saving…'));
    try{const data=await request('/api/site/posts'+(post?'/'+encodeURIComponent(post.id):''),{method:post?'PATCH':'POST',json:{title:title.value.trim(),category:categoryInput.value,body,status:state,...(categoryInput.value==='maintenance'?{startsAt:starts.value?new Date(starts.value).toISOString():null,endsAt:ends.value?new Date(ends.value).toISOString():null}:{})}});if(alive()){dirty=false;onSaved(data.post);}}
    catch(e){if(alive())status.replaceChildren(alert(e.message,'warning'));}finally{busy=false;for(const b of actions.querySelectorAll('button'))b.disabled=false;}
  };
  const actions=h('div',{class:'row owner-tools'},button('Save draft',()=>save('draft'),true),button(post?.status==='published'?'Update published post':'Publish',()=>save('published')),button('Cancel',()=>{if(!dirty||window.confirm('Discard unsaved changes?'))onCancel();},true));
  const node=h('section',{class:'card post-editor'},h('h2',null,post?'Edit post':'Create a post'),h('p',null,'Only your authenticated owner account can publish. Image uploads are checked before they are stored.'),field('Title',title),field('Category',categoryInput),schedule,toolbar,editor,file,actions,status);
  return {node,isDirty:()=>dirty,dispose:()=>{disposed=true;stopCamera();range=null;editor.replaceChildren();}};
}
