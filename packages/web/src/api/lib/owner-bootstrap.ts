/** Existing local accounts still sign in; creating one is an operator setup step. */
export function ownerBootstrapEnabled(env: Record<string, string | undefined> = process.env): boolean {
  return env.REDX_ALLOW_OWNER_BOOTSTRAP === "true";
}
