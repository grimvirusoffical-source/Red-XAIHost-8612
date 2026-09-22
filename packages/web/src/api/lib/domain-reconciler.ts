import { eq, ne } from "drizzle-orm";
import { db } from "../database";
import { domains as domainsTable } from "../database/schema";
import { zoneStatus } from "./cloudflare";
import { cloudflareReady, connectDomain } from "./domain-connect";
import { nowSeconds } from "./ids";
import { logActivity } from "./activity";

const INTERVAL_MS = 10 * 60 * 1000;
const FIRST_RUN_MS = 20 * 1000;

/**
 * Keeps trying, quietly. A domain sits at "pending" or "dns_set" until the
 * registrar's nameserver change propagates, which can take hours — so instead
 * of asking the owner to come back and press Connect, the panel re-attempts the
 * whole flow on a timer and flips the domain to live the moment Cloudflare
 * reports the zone active.
 */
export async function reconcileDomains(): Promise<{ checked: number; changed: number }> {
  if (!(await cloudflareReady())) {
    console.log("[domains] reconcile skipped — no working Cloudflare token yet");
    return { checked: 0, changed: 0 };
  }

  const rows = await db.select().from(domainsTable).where(ne(domainsTable.status, "live"));
  let changed = 0;

  for (const domain of rows) {
    try {
      if (!domain.zoneId || !domain.tunnelId) {
        const attempt = await connectDomain(domain.id, { trigger: "auto" });
        if (attempt.ok) changed += 1;
        continue;
      }
      const status = await zoneStatus(domain.zoneId);
      if (status.status === "active") {
        await db
          .update(domainsTable)
          .set({ status: "live", lastError: null, lastCheckedAt: nowSeconds() })
          .where(eq(domainsTable.id, domain.id));
        changed += 1;
        await logActivity({
          scope: "domain",
          refId: domain.id,
          level: "success",
          message: `${domain.hostname} is live — Cloudflare activated the zone.`,
        });
      } else {
        await db
          .update(domainsTable)
          .set({ lastCheckedAt: nowSeconds() })
          .where(eq(domainsTable.id, domain.id));
      }
    } catch {
      // A failing reconcile pass must never take the API process down.
    }
  }

  console.log(`[domains] reconcile pass — ${rows.length} not live, ${changed} advanced`);
  return { checked: rows.length, changed };
}

/** Starts the timer once per process, surviving dev-server module reloads. */
export function startDomainReconciler(): void {
  const flag = "__rxhDomainReconciler";
  const globals = globalThis as Record<string, unknown>;
  if (globals[flag]) return;
  globals[flag] = true;

  console.log("[domains] reconciler on — pending domains retried every 10 minutes");
  setTimeout(() => void reconcileDomains(), FIRST_RUN_MS).unref?.();
  setInterval(() => void reconcileDomains(), INTERVAL_MS).unref?.();
}
