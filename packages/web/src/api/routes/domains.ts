import { z } from "zod";
import { desc, eq } from "drizzle-orm";
import { ORPCError } from "@orpc/server";
import { owner } from "../middleware/auth";
import { db } from "../database";
import { domains as domainsTable, projects as projectsTable } from "../database/schema";
import { newId, nowSeconds } from "../lib/ids";
import { logActivity } from "../lib/activity";
import { ensureZone, rootDomain, zoneStatus } from "../lib/cloudflare";
import { listRegistrarDomains } from "../lib/registrar";
import { bindDomainToProject, cloudflareReady, connectDomain } from "../lib/domain-connect";

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
        autoConnect: z.boolean().default(true),
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

      // Don't make the owner press Connect: if Cloudflare is wired up, do it now.
      const auto =
        input.autoConnect && (await cloudflareReady())
          ? await connectDomain(id, { trigger: "auto" })
          : null;
      return { id, auto };
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

      // Attaching a domain to a project is not just a row change: the tunnel
      // has to be re-pointed at that project's port and the node has to be
      // told to run cloudflared. Without this the hostname resolves to a
      // tunnel with no connector behind it — Cloudflare Error 1033.
      if (patch.projectId !== undefined) {
        const bind = await bindDomainToProject(id);
        return { ok: bind.ok, detail: bind.detail };
      }
      return { ok: true, detail: null as string | null };
    }),

  remove: owner.input(z.object({ id: z.string() })).handler(async ({ input }) => {
    await db.delete(domainsTable).where(eq(domainsTable.id, input.id));
    return { ok: true };
  }),

  /** Domains the connected registrar account actually owns. */
  registrarDomains: owner
    .input(z.object({ registrar: z.enum(["godaddy", "namecheap"]) }))
    .handler(async ({ input }) => listRegistrarDomains(input.registrar)),

  /** Wire the domain through Cloudflare (zone, nameservers, tunnel, DNS). */
  connect: owner
    .input(z.object({ id: z.string(), setNameserversAtRegistrar: z.boolean().default(true) }))
    .handler(async ({ input }) => {
      const [domain] = await db.select().from(domainsTable).where(eq(domainsTable.id, input.id));
      if (!domain) throw new ORPCError("NOT_FOUND");
      return connectDomain(domain.id, {
        setNameserversAtRegistrar: input.setNameserversAtRegistrar,
        trigger: "manual",
      });
    }),

  /**
   * Everything needed to do it by hand in the Cloudflare and registrar
   * dashboards: the exact nameservers, the exact CNAME row, the exact
   * cloudflared commands for the node.
   */
  manualSetup: owner.input(z.object({ id: z.string() })).handler(async ({ input }) => {
    const [domain] = await db.select().from(domainsTable).where(eq(domainsTable.id, input.id));
    if (!domain) throw new ORPCError("NOT_FOUND");
    const [project] = domain.projectId
      ? await db.select().from(projectsTable).where(eq(projectsTable.id, domain.projectId))
      : [];
    const zone = (await cloudflareReady()) ? await ensureZone(domain.hostname) : null;
    const root = rootDomain(domain.hostname);
    const tunnelName = domain.tunnelName ?? `redxaihost-${root.replace(/\./g, "-")}`;
    const port = project?.port ?? 8080;
    const recordName = domain.hostname === root ? "@" : domain.hostname.slice(0, -(root.length + 1));
    return {
      hostname: domain.hostname,
      rootDomain: root,
      recordName,
      nameServers: zone?.nameServers ?? [],
      zoneActive: Boolean(zone?.activated),
      cnameTarget: domain.cnameTarget ?? (domain.tunnelId ? `${domain.tunnelId}.cfargotunnel.com` : null),
      tunnelName,
      port,
      projectName: project?.name ?? null,
      cloudflareConnected: await cloudflareReady(),
    };
  }),

  recheck: owner.input(z.object({ id: z.string() })).handler(async ({ input }) => {
    const [domain] = await db.select().from(domainsTable).where(eq(domainsTable.id, input.id));
    if (!domain) throw new ORPCError("NOT_FOUND");
    if (!domain.zoneId) {
      // Never connected: try it now rather than telling the owner off.
      if (!(await cloudflareReady())) {
        return {
          ok: false,
          status: domain.status,
          detail: "Cloudflare is not connected yet — save an API token under Settings.",
        };
      }
      const attempt = await connectDomain(domain.id, { trigger: "auto" });
      return {
        ok: attempt.ok,
        status: attempt.ok ? (attempt.zoneActive ? "live" : "dns_set") : "error",
        detail: attempt.ok
          ? attempt.zoneActive
            ? "Connected through Cloudflare Tunnel."
            : `Connected. Waiting on the nameserver change: ${attempt.nameServers.join(", ")}`
          : (attempt.steps.find((step) => !step.ok)?.detail ?? attempt.skipped ?? "Connect failed."),
      };
    }
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
