import { z } from "zod";
import { owner } from "../middleware/auth";
import {
  cloudflareOverview,
  cloudflarePermissionCheck,
  OPTIONAL_PERMISSIONS,
  REQUIRED_PERMISSIONS,
  TOKEN_TEMPLATE_URL,
} from "../lib/cloudflare";
import { hardenZone, zoneSecurityStatus } from "../lib/cloudflare-security";
import { rateLimitStats } from "../lib/rate-limit";
import { db } from "../database";
import { domains as domainsTable } from "../database/schema";
import { autoConnectPendingDomains, cloudflareReady } from "../lib/domain-connect";
import { getCredential, saveCredential, setVerifyResult, verifyCredential } from "../lib/credentials";
import { logActivity } from "../lib/activity";

export const cloudflare = {
  /** Live Cloudflare state: token, account, zones, tunnels. Drives the banner. */
  status: owner.handler(async () => {
    const overview = await cloudflareOverview();
    return { ...overview, ready: await cloudflareReady() };
  }),

  /**
   * Exercise the saved token permission by permission and report a checklist,
   * plus the one-click token link with all four already ticked. This is the
   * answer to "I didn't give it the right permissions" — the panel says which
   * one is missing instead of the owner guessing.
   */
  permissions: owner.handler(async () => {
    const check = await cloudflarePermissionCheck();
    return {
      ...check,
      templateUrl: TOKEN_TEMPLATE_URL,
      required: REQUIRED_PERMISSIONS.map((permission) => ({ ...permission })),
      optional: OPTIONAL_PERMISSIONS.map((permission) => ({ ...permission })),
    };
  }),

  /**
   * What is actually protecting the hosted sites right now: the panel's own
   * rate limits (always on, no Cloudflare needed) and the edge rules per zone.
   */
  security: owner.handler(async () => {
    const panel = rateLimitStats();
    const rows = await db.select().from(domainsTable);
    const zones = [
      ...new Map(
        rows.filter((row) => row.zoneId).map((row) => [row.zoneId!, row.hostname]),
      ).entries(),
    ];

    const edge = await Promise.all(
      zones.map(async ([zoneId, hostname]) => ({
        hostname,
        zoneId,
        ...(await zoneSecurityStatus(zoneId)),
      })),
    );

    return {
      panel: {
        trackedKeys: panel.trackedKeys,
        limits: [
          { scope: "Sign-in", detail: `${panel.limits.auth.max} attempts / 5 min per IP` },
          { scope: "API", detail: `${panel.limits.api.max} requests / min per IP` },
          { scope: "Node agents", detail: `${panel.limits.agent.max} requests / min per node` },
        ],
      },
      edge,
    };
  }),

  /** Re-apply the edge baseline to every zone — used after a token upgrade. */
  harden: owner.handler(async () => {
    const rows = await db.select().from(domainsTable);
    const zones = [
      ...new Map(
        rows.filter((row) => row.zoneId).map((row) => [row.zoneId!, row.hostname]),
      ).entries(),
    ];
    if (zones.length === 0) {
      return { ok: false as const, message: "No Cloudflare zones to harden yet.", zones: [] };
    }

    const results = [];
    for (const [zoneId, hostname] of zones) {
      const result = await hardenZone(zoneId);
      results.push({ hostname, ...result });
    }

    const applied = results.reduce(
      (total, row) => total + row.steps.filter((step) => step.ok).length,
      0,
    );
    const total = results.reduce((sum, row) => sum + row.steps.length, 0);
    await logActivity({
      scope: "system",
      level: applied === total ? "success" : "warn",
      message: `Edge protection re-applied: ${applied}/${total} settings across ${results.length} zone(s).`,
    });
    return {
      ok: applied > 0,
      message: `${applied}/${total} protections applied across ${results.length} zone(s).`,
      zones: results,
    };
  }),

  /**
   * "Try to connect" in one press: re-verify the saved token, discover and
   * store the account id if it is missing, then wire every domain that is not
   * live yet.
   */
  autoConnect: owner
    .input(z.object({ hostnames: z.array(z.string()).optional() }).optional())
    .handler(async ({ input }) => {
      const values = await getCredential("cloudflare");
      if (!values?.apiToken) {
        return {
          ok: false as const,
          message: "No Cloudflare API token saved yet — add one under Settings.",
          accountId: null as string | null,
          accountName: null as string | null,
          domains: [] as { hostname: string; ok: boolean; detail: string }[],
        };
      }

      const verification = await verifyCredential("cloudflare", values);
      if (verification.discovered?.accountId) {
        await saveCredential("cloudflare", {
          ...values,
          accountId: verification.discovered.accountId,
        });
      }
      await setVerifyResult("cloudflare", verification.ok, verification.message);
      if (!verification.ok) {
        return {
          ok: false as const,
          message: verification.message,
          accountId: null as string | null,
          accountName: null as string | null,
          domains: [] as { hostname: string; ok: boolean; detail: string }[],
        };
      }

      const results = await autoConnectPendingDomains(input?.hostnames);

      await logActivity({
        scope: "system",
        level: verification.ok ? "success" : "warn",
        message: `Cloudflare auto-connect: ${verification.message}${
          results.length > 0 ? ` ${results.filter((r) => r.ok).length}/${results.length} domains wired.` : ""
        }`,
      });

      return {
        ok: true as const,
        message: verification.message,
        accountId: verification.discovered?.accountId ?? values.accountId ?? null,
        accountName: verification.discovered?.accountName ?? null,
        domains: results,
      };
    }),
};
