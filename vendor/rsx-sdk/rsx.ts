// Red-SecretsXAI primitives for the panel and InfectedNation.
// Native when the addon and librsx load (RSX_BACKEND=native), otherwise the single-threaded
// WebAssembly build, which runs under Bun and on every platform with the same output.
// @ts-ignore: CommonJS module without types
import sdk from "./index.js";

type B = Uint8Array;
export interface Rsx {
  backend: string;
  sha256(m: B): Buffer;
  hmac(k: B, m: B): Buffer;
  hmacSha1(k: B, m: B): Buffer;
  hkdf(ikm: B, salt: B, info: B, len: number): Buffer;
  aeadEncrypt(key: B, nonce: B, aad: B, pt: B): { ct: Buffer; tag: Buffer };
  aeadDecrypt(key: B, nonce: B, aad: B, ct: B, tag: B): Buffer;
  argon2Ext(p: B, salt: B, secret: B, ad: B, t: number, m: number, lanes: number, outLen: number): Buffer;
  random(n: number): Buffer;
  ctEq(a: B, b: B): boolean;
  hotp(k: B, counter: number, digits: number): number;
  totp(k: B, t: number, digits: number, step: number, t0: number): number;
  totpVerify(k: B, code: string, t: number, digits: number, step: number, t0: number, skew: number): boolean;
  phcHashPassword(password: string, t?: number, m?: number, p?: number): string;
  phcVerifyPassword(password: string, phc: string): boolean;
  phcParse(phc: string): unknown;
}

const native = process.env.RSX_BACKEND === "native" && sdk.backend === "native";
export const rsx: Rsx = native ? { ...sdk, backend: "native" } : { ...(await sdk.wasm({ threads: false })), backend: "wasm-st" };

/** Constant-time string comparison; false on any length difference. */
export function ctEqStr(a: string, b: string): boolean {
  const x = Buffer.from(a), y = Buffer.from(b);
  return x.length === y.length && rsx.ctEq(x, y);
}
