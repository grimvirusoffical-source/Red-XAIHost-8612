// These checks consume the canonical server session; they never infer identity from a UI field.
export function normalizeSession(value){
  if(!value||typeof value!=='object'||typeof value.signedIn!=='boolean')throw new Error('The account service returned an invalid session.');
  if(!value.signedIn)return {signedIn:false,owner:false,capabilities:value.capabilities||{}};
  if(!value.account||typeof value.account.id!=='string'||!value.account.id||typeof value.account.email!=='string'||!Number.isSafeInteger(value.expiresMs)||value.expiresMs<=Date.now())throw new Error('Your account session could not be verified. Sign in again.');
  return {...value,owner:value.owner===true};
}
export const identityKey=session=>session?.signedIn?session.account.id+'\0'+String(session.owner===true):'';
export const isPrivatePath=path=>['/db','/host','/settings','/api-keys'].includes(path);
export const usesPrimaryPage=path=>['/signup','/activate','/tos','/redxai-connect','/redxai-connectx'].some(p=>path===p||path.startsWith(p+'/'));

// Private editor content lives only in this document's memory. Leases belong
// to an observed owner/session generation, not just an account ID.
export function createDraftStore({now=Date.now}={}){
  const entries=new Map(),listeners=new Set();let scope='',expires=0,generation=0;
  const notify=event=>{for(const listener of [...listeners])listener(event);};
  const clear=()=>{generation++;scope='';expires=0;entries.clear();notify();};
  const valid=()=>{
    if(scope&&expires>now())return true;
    if(scope||entries.size)clear();
    return false;
  };
  const setSession=session=>{
    const previous=generation;
    valid();
    const eligible=session?.signedIn===true&&session.owner===true&&typeof session.account?.id==='string'&&session.account.id&&Number.isSafeInteger(session.expiresMs)&&session.expiresMs>now();
    const next=eligible?identityKey(session):'';
    if(next!==scope){clear();scope=next;}
    expires=eligible?session.expiresMs:0;
    return generation!==previous;
  };
  const lease=category=>{
    const mine=generation,owner=scope;
    const available=()=>valid()&&mine===generation&&owner===scope;
    const peek=()=>{if(!available())return null;const entry=entries.get(category);return entry?{...entry,snapshot:structuredClone(entry.snapshot)}:null;};
    const discard=token=>{
      if(!available())return false;const entry=entries.get(category);
      if(!entry||(token!==undefined&&entry.token!==token))return false;
      entries.delete(category);notify();return true;
    };
    return {
      valid:available,peek,
      remember(token,snapshot,{pending=false}={}){
        if(!available())return false;const old=entries.get(category);
        if(old&&old.token!==token)return false;
        entries.set(category,{token,snapshot:structuredClone(snapshot),pending,failed:old?.failed===true});notify();return true;
      },
      discard,
      settle(token,success){
        if(!available())return;const entry=entries.get(category);if(!entry||entry.token!==token)return;
        if(success)entries.delete(category);else{entry.pending=false;entry.failed=true;}
        notify({type:success?'saved':'failed',category,token});
      },
      take(token){const entry=peek();if(!entry||entry.token!==token||entry.pending)return null;discard(token);return entry.snapshot;},
      subscribe(listener){listeners.add(listener);return()=>listeners.delete(listener);}
    };
  };
  return {setSession,clear,lease,hasAny:()=>valid()&&entries.size>0};
}

// Traversal already selected its destination: preserve/dispose, then render
// that destination without adding, replacing, or canceling browser history.
export function createTraversalHandler({dispose,shouldRefresh,refresh,renderHeader,renderRoute}){
  return async()=>{dispose();if(shouldRefresh())await refresh();renderHeader();await renderRoute();};
}
