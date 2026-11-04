// These checks consume the canonical server session; they never infer identity from a UI field.
export function normalizeSession(value){
  if(!value||typeof value!=='object'||typeof value.signedIn!=='boolean')throw new Error('The account service returned an invalid session.');
  if(!value.signedIn)return {signedIn:false,owner:false,capabilities:value.capabilities||{}};
  if(!value.account||typeof value.account.id!=='string'||!value.account.id||typeof value.account.email!=='string'||!Number.isSafeInteger(value.expiresMs)||value.expiresMs<=Date.now())throw new Error('Your account session could not be verified. Sign in again.');
  if(value.account.identityKey!==undefined&&(typeof value.account.identityKey!=='string'||!value.account.identityKey||value.account.identityKey.length>1024||/[\u0000-\u0020\u007f]/u.test(value.account.identityKey)))throw new Error('Your account session could not be verified. Sign in again.');
  return {...value,owner:value.owner===true};
}
// The server supplies this cache identity from its trusted issuer/subject.
// Public account DTO IDs can overlap across realms. Legacy fallback preserves
// standalone test fixtures and deployments predating the new session field.
export const identityKey=session=>session?.signedIn?(session.account.identityKey??'website:'+session.account.id)+'\0'+String(session.owner===true):'';
export const isPrivatePath=path=>['/db','/host','/settings','/api-keys'].includes(path);
export const usesPrimaryPage=path=>['/signup','/activate','/tos','/redxai-connect','/redxai-connectx'].some(p=>path===p||path.startsWith(p+'/'));
