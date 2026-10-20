import { rsx } from "../vendor/rsx-sdk/rsx";

/**
 * Column sealing with Red-SecretsXAI (P13), shared by the panel and InfectedNation; each passes its own
 * master secret and domain. No SQLite page layer, no libSQL encryption, no AES.
 *
 *   root      = Argon2id(secret, "<domain>.colseal.v1", t=2, m=19456 KiB, p=1)
 *   encKey(c) = HKDF-SHA256(root, "<domain> colseal v1", "enc|" + c)   one key per column context c
 *   idxKey(c) = HKDF-SHA256(root, "<domain> colseal v1", "idx|" + c)   separate key, lookups only
 *
 * "s1." randomised: 12-byte random nonce, AD = "s1|" + c + "|" + rowId.
 * "d1." deterministic, for equality-lookup columns: nonce = first 12 bytes of
 *       HMAC-SHA256(idxKey(c), c || 0x00 || plaintext) (a synthetic IV, i.e. a keyed blind index under a
 *       key separate from the encryption key). Equal plaintexts give equal blobs; only equality leaks.
 *       AD = "d1|" + c. Opening re-derives the IV from the plaintext and refuses a mismatch.
 * "b1." blindIndex(c, v): keyed HMAC-SHA256 under idxKey(c), for values that are only ever compared
 *       (token, key and secret hashes).
 * c is "<table>.<column>".
 */
export class SealError extends Error {}

export function isSealed(v: unknown): boolean {
  return typeof v === "string" && (v.startsWith("s1.") || v.startsWith("d1."));
}

export function makeColseal(domain: string, getSecret: () => string) {
  const salt = Buffer.from(`${domain} colseal v1`);
  let rootCache: { secret: string; key: Buffer } | null = null;
  const keyCache = new Map<string, Buffer>();
  const root = (): Buffer => {
    const secret = getSecret();
    if (!rootCache || rootCache.secret !== secret) {
      rootCache = {
        secret,
        key: rsx.argon2Ext(Buffer.from(secret, "utf8"), Buffer.from(`${domain}.colseal.v1`), Buffer.alloc(0), Buffer.alloc(0), 2, 19456, 1, 32),
      };
      keyCache.clear();
    }
    return rootCache.key;
  };
  const subkey = (kind: "enc" | "idx", ctx: string): Buffer => {
    const r = root();
    const id = `${kind}|${ctx}`;
    let k = keyCache.get(id);
    if (!k) keyCache.set(id, (k = rsx.hkdf(r, salt, Buffer.from(id, "utf8"), 32)));
    return k;
  };
  const b64 = (b: Uint8Array) => Buffer.from(b).toString("base64url");
  const unpack = (blob: string, prefix: string) => {
    if (typeof blob !== "string" || !blob.startsWith(prefix)) throw new SealError("not a sealed value");
    const raw = Buffer.from(blob.slice(prefix.length), "base64url");
    if (raw.length < 28) throw new SealError("sealed value too short");
    return { nonce: raw.subarray(0, 12), ct: raw.subarray(12, raw.length - 16), tag: raw.subarray(raw.length - 16) };
  };
  const sivNonce = (ctx: string, pt: Buffer) =>
    rsx.hmac(subkey("idx", ctx), Buffer.concat([Buffer.from(ctx, "utf8"), Buffer.from([0]), pt])).subarray(0, 12);

  return {
    seal(ctx: string, rowId: string, plaintext: string): string {
      const nonce = rsx.random(12);
      const { ct, tag } = rsx.aeadEncrypt(subkey("enc", ctx), nonce, Buffer.from(`s1|${ctx}|${rowId}`, "utf8"), Buffer.from(plaintext, "utf8"));
      return "s1." + b64(Buffer.concat([nonce, ct, tag]));
    },
    open(ctx: string, rowId: string, blob: string): string {
      const { nonce, ct, tag } = unpack(blob, "s1.");
      try {
        return Buffer.from(rsx.aeadDecrypt(subkey("enc", ctx), nonce, Buffer.from(`s1|${ctx}|${rowId}`, "utf8"), ct, tag)).toString("utf8");
      } catch {
        throw new SealError(`sealed value for ${ctx} failed authentication`);
      }
    },
    sealDet(ctx: string, plaintext: string): string {
      const pt = Buffer.from(plaintext, "utf8");
      const nonce = sivNonce(ctx, pt);
      const { ct, tag } = rsx.aeadEncrypt(subkey("enc", ctx), nonce, Buffer.from(`d1|${ctx}`, "utf8"), pt);
      return "d1." + b64(Buffer.concat([nonce, ct, tag]));
    },
    openDet(ctx: string, blob: string): string {
      const { nonce, ct, tag } = unpack(blob, "d1.");
      let pt: Buffer;
      try {
        pt = Buffer.from(rsx.aeadDecrypt(subkey("enc", ctx), nonce, Buffer.from(`d1|${ctx}`, "utf8"), ct, tag));
      } catch {
        throw new SealError(`sealed value for ${ctx} failed authentication`);
      }
      if (!rsx.ctEq(sivNonce(ctx, pt), nonce)) throw new SealError(`sealed value for ${ctx} has a wrong synthetic IV`);
      return pt.toString("utf8");
    },
    blindIndex(ctx: string, value: string): string {
      return "b1." + rsx.hmac(subkey("idx", ctx), Buffer.from(value, "utf8")).toString("hex");
    },
    reset(): void {
      rootCache = null;
      keyCache.clear();
    },
  };
}
