import {h,button,link,field,alert,safeNext} from './dom.js';
import {primary,loginOutcome} from './api.js';

export function createSignIn({current,completed,verification}){
  let token='',disposed=false,viewGeneration=0;const root=h('section',{class:'auth-wrap wrap'}),card=h('div',{class:'card auth-card'});root.append(card);
  const alive=()=>!disposed&&current();
  const message=h('div',{'aria-live':'polite'});
  const error=e=>{if(alive())message.replaceChildren(alert(e.message||String(e),'warning'));};
  const call=async(path,json={})=>{const result=await primary(path,json,token);if(result.status!==200)throw new Error(result.json.message||'The account service could not complete this step.');return result.json;};
  const finish=async()=>{token='';await completed();};
  const submit=(form,handler)=>form.addEventListener('submit',async e=>{e.preventDefault();const buttons=Array.from(form.querySelectorAll('button'));buttons.forEach(x=>x.disabled=true);message.replaceChildren();try{await handler();}catch(e){error(e);}finally{if(alive())buttons.forEach(x=>x.disabled=false);}});
  const otp=()=>h('input',{type:'text',inputmode:'numeric',autocomplete:'one-time-code',pattern:'[0-9 ]{6,7}',maxlength:7,required:true,placeholder:'000 000'});
  const start=()=>{
    viewGeneration++;token='';const login=h('input',{type:'text',autocomplete:'username',required:true,maxlength:320}),password=h('input',{type:'password',autocomplete:'current-password',required:true});
    const form=h('form',{'aria-label':'Sign in'},h('h1',null,'Sign in to Red-XAI'),h('p',null,'Use your existing username or email and password.'),field('Username or email',login),field('Password',password),message,h('button',{type:'submit',class:'button'},'Sign in'),h('div',{class:'auth-footer'},'New to Red-XAI? ',link('Create an account','/signup')));
    submit(form,async()=>{const result=await primary('login',{login:login.value.trim(),password:password.value});password.value='';if(!alive())return;
      const outcome=loginOutcome(result.status,result.json);
      if(outcome==='verification'){verification(login.value.trim());return;}
      if(outcome==='complete'){await finish();return;}
      if(outcome==='challenge'||outcome==='setup'){token=result.json.token;if(outcome==='challenge')challenge(result.json.methods||{});else setup(result.json.methods||{});return;}
      throw new Error(result.json.message||'Sign-in could not be completed. Check your details and retry.');
    });card.replaceChildren(form);
  };
  const challenge=methods=>{
    viewGeneration++;
    const allowed=['totp','call'].filter(x=>methods[x]);if(!allowed.length){error(new Error('The account service did not provide an available verification method.'));return;}
    const method=h('select',null,...allowed.map(x=>h('option',{value:x},x==='totp'?'Authenticator app':'Phone call'))),code=otp(),callStatus=h('div',{'aria-live':'polite'});
    const callButton=button('Call me with a code',async()=>{callButton.disabled=true;try{const data=await call('login/call');if(alive())callStatus.replaceChildren(alert('A call was requested'+(data.phone?' to '+data.phone:'')+'.','success'));}catch(e){error(e);}finally{callButton.disabled=false;}},true);
    const show=()=>{callButton.hidden=method.value!=='call';};method.addEventListener('change',show);show();
    const form=h('form',{'aria-label':'Verify sign-in'},h('h1',null,'Verify your sign-in'),h('p',null,'Complete the security check required by your account.'),field('Verification method',method),callButton,callStatus,field('Six-digit code',code),message,h('button',{type:'submit',class:'button'},'Sign in'),button('Back',start,true));
    submit(form,async()=>{await call('login/2fa',{method:method.value,code:code.value});code.value='';if(alive())await finish();});card.replaceChildren(form);
  };
  const setup=methods=>{
    const view=++viewGeneration;
    const mode=h('select',null,h('option',{value:'totp'},'Authenticator app'),...(methods.call?[h('option',{value:'phone'},'Phone call')]:[])),content=h('div');
    const heading=h('div',null,h('h1',null,'Secure your account'),h('p',null,'The account service requires a security setup for this account. Existing account policy controls this step.'),field('Method',mode),content,message,button('Back to sign in',start,true));card.replaceChildren(heading);
    let setupGeneration=0;
    const show=async()=>{
      const generation=++setupGeneration;message.replaceChildren();content.replaceChildren(alert('Loading security setup…'));
      if(mode.value==='totp')try{const data=await call('2fa/totp/start');if(!alive()||generation!==setupGeneration||view!==viewGeneration)return;
        const qr=qrCode(data.qr),code=otp();
        const form=h('form',null,h('p',null,'Scan the QR code with your authenticator app, or enter the setup key. Use time-based six-digit codes.'),qr,h('p',null,'Setup key'),h('pre',{'data-private':'true'},typeof data.secret==='string'?data.secret:'Unavailable'),field('Code from the app',code),h('button',{type:'submit',class:'button'},'Confirm and sign in'));
        submit(form,async()=>{await call('2fa/totp/confirm',{code:code.value});code.value='';if(alive())await finish();});content.replaceChildren(form);
      }catch(e){if(alive()&&view===viewGeneration&&generation===setupGeneration){error(e);content.replaceChildren();}}
      else{
        const phone=h('input',{type:'tel',autocomplete:'tel',required:true,placeholder:'+1 555 123 4567'}),code=otp(),status=h('div',{'aria-live':'polite'});
        const callButton=button('Call me with a code',async()=>{callButton.disabled=true;try{const data=await call('2fa/phone/start',{phone:phone.value});if(alive())status.replaceChildren(alert('A call was requested'+(data.phone?' to '+data.phone:'')+'.','success'));}catch(e){error(e);}finally{callButton.disabled=false;}},true);
        const form=h('form',null,field('Phone number with country code',phone),callButton,status,field('Code from the call',code),h('button',{type:'submit',class:'button'},'Confirm and sign in'));
        submit(form,async()=>{await call('2fa/phone/confirm',{code:code.value});code.value='';if(alive())await finish();});content.replaceChildren(form);
      }
    };mode.addEventListener('change',show);show();
  };
  start();return {node:root,dispose:()=>{disposed=true;token='';card.replaceChildren();}};
}
export function qrCode(rows){
  if(!Array.isArray(rows)||!rows.length||rows.length>300||rows.some(row=>typeof row!=='string'||row.length!==rows.length||/[^01]/u.test(row)))return alert('QR code unavailable. Use the setup key.','warning');
  const ns='http://www.w3.org/2000/svg',svg=document.createElementNS(ns,'svg');svg.setAttribute('viewBox',`-4 -4 ${rows.length+8} ${rows.length+8}`);svg.setAttribute('role','img');svg.setAttribute('aria-label','Authenticator setup QR code');svg.setAttribute('class','auth-qr');svg.setAttribute('shape-rendering','crispEdges');
  const bg=document.createElementNS(ns,'rect');for(const [key,value] of Object.entries({x:-4,y:-4,width:rows.length+8,height:rows.length+8,fill:'#fff'}))bg.setAttribute(key,value);svg.append(bg);
  const path=document.createElementNS(ns,'path');let d='';rows.forEach((row,y)=>{for(let x=0;x<row.length;x++)if(row[x]==='1')d+=`M${x} ${y}h1v1h-1z`;});path.setAttribute('d',d);path.setAttribute('fill','#000');svg.append(path);return svg;
}
export function verificationPage({identifier,capabilities,navigate}){
  const result=h('div',{'aria-live':'polite'}),resend=button('Resend verification email',async()=>{resend.disabled=true;try{if(!capabilities.resendVerification){result.replaceChildren(alert('Verification resend is currently unavailable. Contact support@infected-nation.com so your account can be checked.','warning'));return;}await requestResend();}catch(e){result.replaceChildren(alert(e.message,'warning'));}finally{resend.disabled=false;}},true);
  const requestResend=async()=>{const {request}=await import('./api.js');await request('/api/site/verification/resend',{method:'POST',json:{login:identifier}});result.replaceChildren(alert('The verification request was accepted. Check your email.','success'));};
  return {node:h('section',{class:'auth-wrap wrap'},h('div',{class:'card auth-card'},h('h1',null,'Verify your email'),h('p',null,'Your account needs email activation. Open the verification link sent to your email, then sign in again.'),resend,result,h('p',null,link('Return to sign in','/signin'),' · ',link('Contact support','/contact'))))};
}
