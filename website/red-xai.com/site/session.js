// These checks consume the canonical server session; they never infer identity from a UI field.
export function normalizeSession(value){
  if(!value||typeof value!=='object'||typeof value.signedIn!=='boolean')throw new Error('The account service returned an invalid session.');
  if(!value.signedIn)return {signedIn:false,owner:false,capabilities:value.capabilities||{}};
  if(!value.account||typeof value.account.id!=='string'||!value.account.id||typeof value.account.email!=='string'||!Number.isSafeInteger(value.expiresMs)||value.expiresMs<=Date.now())throw new Error('Your account session could not be verified. Sign in again.');
  return {...value,owner:value.owner===true};
}
export const identityKey=session=>session?.signedIn?session.account.id+'\0'+String(session.owner===true):'';
export const isPrivatePath=path=>['/db','/host','/cloud-pc','/settings','/api-keys'].includes(path);
export const usesPrimaryPage=path=>['/signup','/activate','/tos','/redxai-connect','/redxai-connectx'].some(p=>path===p||path.startsWith(p+'/'));
