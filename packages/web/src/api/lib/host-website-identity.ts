import {storageClient} from '../database/__client';
import {HostRealmOwnership} from './realm-ownership';
import {websiteIdentity} from './website-session';

export const hostRealmOwnership=new HostRealmOwnership(storageClient);

export async function hostWebsiteIdentity(headers:Headers) {
  const identity=await websiteIdentity(headers);
  if(!identity)return null;
  try {return {identity,tenantId:await hostRealmOwnership.resolve(identity)};}
  catch {return null;}
}
