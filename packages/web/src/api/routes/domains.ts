import { z } from "zod";
import { desc, eq } from "drizzle-orm";
import { ORPCError } from "@orpc/server";
import { owner } from "../middleware/auth";
import { db } from "../database";
import { domains as domainsTable, projects as projectsTable } from "../database/schema";
import { newId, nowSeconds } from "../lib/ids";
import { logActivity } from "../lib/activity";
import {
  ensureTunnel,
  ensureZone,
  rootDomain,
  setTunnelIngress,
  upsertTunnelCname,
  zoneStatus,
} from "../lib/cloudflare";
import { listRegistrarDomains, setNameservers } from "../lib/registrar";

const hostnameSchema = z
  .string()
  .min(3)
  .max(253)
  .regex(/^[a-z0-9.-]+\.[a-z]{2,}$/i, "Enter a hostname like app.example.com");

export const domains = {
  list: owner.handler(async () => {
    const rows = await db.select().from(domainsTable).orderBy(desc(domainsTable.createdAt));
    const projectRows = await db.select().from(projectsTable);
    return rows.map((domain) => ({
      ...domain,
      projectName: projectRows.find((project) => project.id === domain.projectId)?.name ?? null,
    }));
  }),

  add: owner
    .input(
      z.object({
        hostname: hostnameSchema,
        projectId: z.string().optional(),
        registrar: z.enum(["godaddy", "namecheap", "cloudflare", "manual"]).default("manual"),
      }),
    )
    .handler(async ({ input }) => {
      const hostname = input.hostname.toLowerCase();
      const id = newId("dom");
      await db.insert(domainsTable).values({
        id,
        hostname,
        projectId: input.projectId ?? null,
        registrar: input.registrar,
        status: "pending",
      });
      await logActivity({
        scope: "domain",
        refId: id,
        message: `Domain ${hostname} added (${input.registrar}).`,
      });
      return { id };
    }),

  update: owner
    .input(
      z.object({
        id: z.string(),
        projectId: z.string().nullable().optional(),
        registrar: z.enum(["godaddy", "namecheap", "cloudflare", "manual"]).optional(),
      }),
    )
    .handler(async ({ input }) => {
      const { id, ...patch } = input;
      await db.update(domainsTable).set(patch).where(eq(domainsTable.id, id));
      return { ok: true };
    }),

  remove: owner.input(z.object({ id: z.string() })).handler(async ({ input }) => {
    await db.delete(domainsTable).where(eq(domainsTable.id, input.id));
    return { ok: true };
  }),

  /** Domains the connected registrar account actually owns. */
  registrarDomains: owner
    .input(z.object({ registrar: z.enum(["godaddy", "namecheap"]) }))
    .handler(async ({ input }) => listRegistrarDomains(input.registrar)),

  /**
   * The whole connect flow, in order:
   * 1. ensure a Cloudflare zone for the root domain,
   * 2. push Cloudflare's nameservers to the registrar over its official API,
   * 3. ensure a named tunnel and point its ingress at the project on the node,
   * 4. upsert the proxied CNAME into the tunnel.
   */
  connect: owner
    .input(z.object({ id: z.string(), setNameserversAtRegistrar: z.boolean().default(true) }))
    .handler(async ({ input }) => {
      const [domain] = await db.select().from(domainsTable).where(eq(domainsTable.id, input.id));
      if (!domain) throw new ORPCError("NOT_FOUND");

      const steps: { step: string; ok: boolean; detail: string }[] = [];

      const zone = await ensureZone(domain.hostname);
      steps.push({
        step: "Cloudflare zone",
        ok: zone.ok,
        detail: zone.ok
          ? `${rootDomain(domain.hostname)} ${zone.activated ? "active" : "pending nameserver change"}`
          : (zone.error ?? "failed"),
      });
      if (!zone.ok || !zone.zoneId) {
        await db
          .update(domainsTable)
          .set({ status: "error", lastError: zone.error ?? null, lastCheckedAt: nowSeconds() })
          .where(eq(domainsTable.id, domain.id));
        return { ok: false as const, steps, nameServers: [] as string[] };
      }

      if (
        input.setNameserversAtRegistrar &&
        (domain.registrar === "godaddy" || domain.registrar === "namecheap") &&
        (zone.nameServers?.length ?? 0) > 0
      ) {
        const ns = await setNameservers(domain.registrar, domain.hostname, zone.nameServers!);
        steps.push({ step: `${domain.registrar} nameservers`, ok: ns.ok, detail: ns.message });
      } else if ((zone.nameServers?.length ?? 0) > 0 && !zone.activated) {
        steps.push({
          step: "Registrar nameservers",
          ok: true,
          detail: `Set these at your registrar: ${zone.nameServers!.join(", ")}`,
        });
      }

      const tunnelName = `redxaihost-${rootDomain(domain.hostname).replace(/\./g, "-")}`;
      const tunnel = await ensureTunnel(tunnelName);
      steps.push({
        step: "Cloudflare tunnel",
        ok: tunnel.ok,
        detail: tunnel.ok ? tunnelName : (tunnel.error ?? "failed"),
      });
      if (!tunnel.ok || !tunnel.tunnelId) {
        await db
          .update(domainsTable)
          .set({ status: "error", lastError: tunnel.error ?? null, lastCheckedAt: nowSeconds() })
          .where(eq(domainsTable.id, domain.id));
        return { ok: false as const, steps, nameServers: zone.nameServers ?? [] };
      }

      // Ingress needs the project's port on the node; default to the static port.
      const [project] = domain.projectId
        ? await db.select().from(projectsTable).where(eq(projectsTable.id, domain.projectId))
        : [];
      const port = project?.port ?? 8080;
      const ingress = await setTunnelIngress(tunnel.tunnelId, [
        { hostname: domain.hostname, service: `http://localhost:${port}` },
      ]);
      steps.push({
        step: "Tunnel ingress",
        ok: ingress.ok,
        detail: ingress.ok
          ? `${domain.hostname} → http://localhost:${port} on the node`
          : (ingress.error ?? "failed"),
      });

      const cname = await upsertTunnelCname(zone.zoneId, domain.hostname, tunnel.tunnelId);
      steps.push({
        step: "DNS record",
        ok: cname.ok,
        detail: cname.ok ? `CNAME → ${cname.target}` : (cname.error ?? "failed"),
      });

      const allOk = steps.every((step) => step.ok);
      await db
        .update(domainsTable)
        .set({
          zoneId: zone.zoneId,
          tunnelId: tunnel.tunnelId,
          tunnelName,
          cnameTarget: cname.target ?? null,
          status: allOk ? (zone.activated ? "live" : "dns_set") : "error",
          lastError: allOk ? null : steps.find((step) => !step.ok)?.detail ?? null,
          lastCheckedAt: nowSeconds(),
        })
        .where(eq(domainsTable.id, domain.id));

      await logActivity({
        scope: "domain",
        refId: domain.id,
        level: allOk ? "success" : "error",
        message: allOk
          ? `${domain.hostname} wired through Cloudflare Tunnel.`
          : `${domain.hostname} connect failed: ${steps.find((step) => !step.ok)?.detail}`,
        meta: steps,
      });

      return {
        ok: allOk,
        steps,
        nameServers: zone.nameServers ?? [],
        connectorToken: tunnel.connectorToken ?? null,
      };
    }),

  recheck: owner.input(z.object({ id: z.string() })).handler(async ({ input }) => {
    const [domain] = await db.select().from(domainsTable).where(eq(domainsTable.id, input.id));
    if (!domain) throw new ORPCError("NOT_FOUND");
    if (!domain.zoneId) return { ok: false, status: domain.status, detail: "Not connected yet." };
    const status = await zoneStatus(domain.zoneId);
    const live = status.status === "active";
    await db
      .update(domainsTable)
      .set({ status: live ? "live" : "dns_set", lastCheckedAt: nowSeconds() })
      .where(eq(domainsTable.id, domain.id));
    return {
      ok: status.ok,
      status: live ? "live" : "dns_set",
      detail: live
        ? "Zone is active on Cloudflare."
        : "Waiting for the nameserver change to propagate (can take a few hours).",
    };
  }),
};
