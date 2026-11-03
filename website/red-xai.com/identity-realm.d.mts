export const LEGACY_ISSUER: 'urn:redxai:legacy-site';
export const FRESH_ISSUER: 'urn:redxai:fresh-normal-prototype:v1';
export const FRESH_ID_PREFIX: 'rxf1_';
export const REALM_AUTHORITY: 'http://127.0.0.1:14330/__prototype/identity';
export const FRESH_COOKIE: 'redx_fresh_normal_prototype_session';
export function realmIdentity(issuer: unknown, subject: unknown): Readonly<{issuer:string;subject:string;id:string;legacy:boolean}>;
export function envelopeIdentity(json: any): ReturnType<typeof realmIdentity>;
export function realmCookies(headers: Headers | Record<string,string|undefined>): string;
export function legacyNativeIdentityAllowed(identity: {issuer?:unknown;id?:unknown} | null | undefined): boolean;
