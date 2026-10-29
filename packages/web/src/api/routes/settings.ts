import { z } from "zod";
import { and, eq } from "drizzle-orm";
import { tenant } from "../middleware/auth";
import { db } from "../database";
import { credentials } from "../database/schema";
import {
  PROVIDERS,
  type ProviderId,
  getCredential,
  saveCredential,
  setVerifyResult,
  verifyCredential,
} from "../lib/credentials";
import { logActivity } from "../lib/activity";
import { autoConnectPendingDomains } from "../lib/domain-connect";
import { currentTenantId } from "../lib/tenant-context";

const providerEnum = z.enum(["openai", "cloudflare", "godaddy", "namecheap", "github", "expo"]);

export const settings = {
  /** Provider catalogue + connection state. Secret values never leave the server. */
  providers: tenant.handler(async () => {
    const tenantId = currentTenantId();
    const rows = await db.select().from(credentials).where(eq(credentials.tenantId, tenantId));
    return (Object.keys(PROVIDERS) as ProviderId[]).map((id) => {
      const key = tenantId === "owner" ? id : `${tenantId}:${id}`;
      const row = rows.find((candidate) => candidate.id === key);
      return {
        id,
        label: PROVIDERS[id].label,
        docs: PROVIDERS[id].docs,
        fields: PROVIDERS[id].fields,
        connected: Boolean(row),
        hint: row?.hint ?? null,
        verifyStatus: row?.verifyStatus ?? "unknown",
        verifyMessage: row?.verifyMessage ?? null,
        updatedAt: row?.updatedAt ?? null,
      };
    });
  }),

  saveProvider: tenant
    .input(z.object({ provider: providerEnum, values: z.record(z.string(), z.string()) }))
    .handler(async ({ input }) => {
      const clean = Object.fromEntries(
        Object.entries(input.values)
          .map(([key, value]) => [key, value.trim()])
          .filter(([, value]) => value.length > 0),
      ) as Record<string, string>;

      // Keep fields the owner left blank (e.g. only rotating one of two keys).
      const previous = (await getCredential(input.provider)) ?? {};
      const merged = { ...previous, ...clean };

      await saveCredential(input.provider, merged);
      const verification = await verifyCredential(input.provider, merged);

      // Providers that can tell us their own ids (Cloudflare's account id) get
      // written back, so the owner never has to go find them.
      if (verification.discovered?.accountId) {
        await saveCredential(input.provider, {
          ...merged,
          accountId: verification.discovered.accountId,
        });
      }

      await setVerifyResult(input.provider, verification.ok, verification.message);
      await logActivity({
        scope: "system",
        level: verification.ok ? "success" : "warn",
        message: `${PROVIDERS[input.provider].label} credentials saved — ${verification.message}`,
      });

      // A working Cloudflare token is all that is missing for most domains:
      // wire whatever is waiting, immediately.
      let autoConnected: { hostname: string; ok: boolean; detail: string }[] = [];
      if (input.provider === "cloudflare" && verification.ok) {
        autoConnected = await autoConnectPendingDomains();
      }

      return { ...verification, autoConnected };
    }),

  verifyProvider: tenant
    .input(z.object({ provider: providerEnum }))
    .handler(async ({ input }) => {
      const values = await getCredential(input.provider);
      if (!values) return { ok: false, message: "Nothing saved for this provider yet." };
      const verification = await verifyCredential(input.provider, values);
      await setVerifyResult(input.provider, verification.ok, verification.message);
      return verification;
    }),

  removeProvider: tenant.input(z.object({ provider: providerEnum })).handler(async ({ input }) => {
    const tenantId = currentTenantId();
    const key = tenantId === "owner" ? input.provider : `${tenantId}:${input.provider}`;
    await db.delete(credentials).where(and(eq(credentials.tenantId, tenantId), eq(credentials.id, key)));
    await logActivity({
      scope: "system",
      level: "warn",
      message: `${PROVIDERS[input.provider].label} credentials removed.`,
    });
    return { ok: true };
  }),

  /** Signed-in workspace identity. Owner status is explicit; the allow-list is never exposed. */
  ownerInfo: tenant.handler(({ context }) => ({
    email: context.user.email,
    name: context.user.name,
    image: context.user.image,
    isOwner: context.isOwner === true,
  })),
};
