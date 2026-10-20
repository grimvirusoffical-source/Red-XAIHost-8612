import { isSealed, makeColseal, SealError } from "../../../../../shared/colseal-core";
import { credentialSecret } from "./crypto";

/** Panel column sealing (P13): shared/colseal-core.ts keyed from CREDENTIAL_SECRET, domain "redxaihost". */
const cs = makeColseal("redxaihost", () => credentialSecret());

export { isSealed, SealError };
export const seal = cs.seal;
export const open = cs.open;
export const sealDet = cs.sealDet;
export const openDet = cs.openDet;
export const blindIndex = cs.blindIndex;
export const resetColsealKeys = cs.reset;

/** Node agent token lookup value: keyed blind index (replaces the unkeyed SHA-256 of earlier builds). */
export function nodeTokenIndex(token: string): string {
  return blindIndex("nodes.token_hash", token);
}
