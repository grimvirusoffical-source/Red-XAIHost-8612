import { eq } from "drizzle-orm";
import { db } from "../database";
import {
  deployments,
  domains as domainsTable,
  projects as projectsTable,
} from "../database/schema";
import { newId, nowSeconds } from "./ids";
import { logActivity } from "./activity";
import {
  cloudflareConfig,
  ensureTunnel,
  explainCloudflareError,
  ensureZone,
  rootDomain,
  setTunnelIngress,
  upsertTunnelCname,
} from "./cloudflare";
import { hardenZone } from "./cloudflare-security";
import { pickNode } from "./scheduler";
import { setNameservers } from "./registrar";

export interface ConnectStep {
  step: string;
  ok: boolean;
  detail: string;
}

export interface ConnectResult {
  ok: boolean;
  steps: ConnectStep[];
  nameServers: string[];
  cnameTarget: string | null;
  tunnelName: string | null;
  connectorToken: string | null;
  zoneActive: boolean;
  skipped?: string;
}

/** True when there is a Cloudflare token + account saved, so auto-connect can run. */
export async function cloudflareReady(): Promise<boolean> {
  return (await cloudflareConfig()) !== null;
}

/**
 * Wire every domain that is not live yet (or just the ones named). Used after a
 * Cloudflare token is saved, from the Connect Cloudflare button, and by the
 * background reconciler.
 */
export async function autoConnectPendingDomains(
  hostnames?: string[],
): Promise<{ hostname: string; ok: boolean; detail: string }[]> {
  if (!(await cloudflareReady())) return [];
  const rows = await db.select().from(domainsTable);
  const wanted = hostnames?.length
    ? rows.filter((row) => hostnames.includes(row.hostname))
    : rows.filter((row) => row.status !== "live");

  const results: { hostname: string; ok: boolean; detail: string }[] = [];
  for (const row of wanted) {
    const attempt = await connectDomain(row.id, { trigger: "auto" });
    results.push({
      hostname: row.hostname,
      ok: attempt.ok,
      detail: attempt.ok
        ? attempt.zoneActive
          ? "Live through Cloudflare Tunnel."
          : `Waiting on nameservers: ${attempt.nameServers.join(", ")}`
        : (attempt.steps.find((step) => !step.ok)?.detail ?? attempt.skipped ?? "Connect failed."),
    });
  }
  return results;
}

/**
 * Point an already-wired domain at the project it now serves, and bring the
 * connector up for it.
 *
 * This is the step that used to be missing. A domain can be wired through
 * Cloudflare (zone, tunnel, DNS all live) and still serve Error 1033, because
 * two things only ever happened during a deploy: the tunnel's ingress learning
 * which local port to forward to, and the node learning it should run
 * cloudflared at all — the connector token is handed out with the deploy job,
 * and that job only carries tunnels for domains already attached to the
 * project. Attaching a domain afterwards therefore changed nothing until the
 * next unrelated deploy. So attaching now re-points the ingress and queues a
 * restart itself.
 */
export async function bindDomainToProject(
  domainId: string,
): Promise<{ ok: boolean; detail: string }> {
  const [domain] = await db.select().from(domainsTable).where(eq(domainsTable.id, domainId));
  if (!domain) return { ok: false, detail: "Domain not found." };
  if (!domain.tunnelId) {
    return { ok: true, detail: "Domain is not wired through Cloudflare yet — connect it first." };
  }

  const [project] = domain.projectId
    ? await db.select().from(projectsTable).where(eq(projectsTable.id, domain.projectId))
    : [];

  // Detached: send the hostname nowhere rather than leaving it pointed at a
  // port that now belongs to something else.
  if (!project) {
    const cleared = await setTunnelIngress(domain.tunnelId, []);
    return {
      ok: cleared.ok,
      detail: cleared.ok
        ? `${domain.hostname} is no longer pointed at a project.`
        : explainCloudflareError(cleared.error ?? "failed", "tunnel"),
    };
  }

  const port = project.port ?? 8080;
  const ingress = await setTunnelIngress(domain.tunnelId, [
    { hostname: domain.hostname, service: `http://localhost:${port}` },
  ]);
  if (!ingress.ok) {
    return { ok: false, detail: explainCloudflareError(ingress.error ?? "failed", "tunnel") };
  }

  // The restart is what actually hands the connector token to the node, so it
  // is queued whether or not the project is currently marked running — a
  // stopped project with a domain attached is exactly the case that needs it.
  //
  // If no node is online right now, queue it against the node the project last
  // ran on anyway. Agents poll for their own queued jobs, so the restart is
  // waiting for it the moment it reconnects — a machine that is asleep or
  // rebooting should not silently drop the one job that brings the site up.
  const nodeId = (await pickNode(project.nodeId)) ?? project.nodeId;
  if (!nodeId) {
    return {
      ok: true,
      detail: `${domain.hostname} → :${port}. No node has run this project yet — the tunnel starts on its first deploy.`,
    };
  }
  const waiting = !(await pickNode(project.nodeId));

  await db.insert(deployments).values({
    id: newId("dep"),
    projectId: project.id,
    nodeId,
    action: "restart",
    status: "queued",
    trigger: "manual",
  });
  await db
    .update(projectsTable)
    .set({ nodeId, status: "deploying", updatedAt: nowSeconds() })
    .where(eq(projectsTable.id, project.id));

  await logActivity({
    scope: "domain",
    refId: domain.id,
    level: "success",
    message: `${domain.hostname} attached to ${project.name} → localhost:${port}; connector restart queued.`,
  });

  return {
    ok: true,
    detail: waiting
      ? `${domain.hostname} → ${project.name} on :${port}. The node is offline — the connector starts the moment its agent reconnects.`
      : `${domain.hostname} → ${project.name} on :${port}. Starting the connector on the node now.`,
  };
}

/**
 * The whole connect flow, in order:
 * 1. ensure a Cloudflare zone for the root domain,
 * 2. push Cloudflare's nameservers to the registrar over its official API,
 * 3. ensure a named tunnel and point its ingress at the project on the node,
 * 4. upsert the proxied CNAME that sends the hostname into the tunnel,
 * 5. restart the project so its node picks the tunnel token up straight away.
 *
 * Safe to call repeatedly — every step is an upsert.
 */
export async function connectDomain(
  domainId: string,
  options: { setNameserversAtRegistrar?: boolean; trigger?: "manual" | "auto" } = {},
): Promise<ConnectResult> {
  const setNs = options.setNameserversAtRegistrar ?? true;
  const trigger = options.trigger ?? "manual";
  const steps: ConnectStep[] = [];
  const fail = (result: Partial<ConnectResult> = {}): ConnectResult => ({
    ok: false,
    steps,
    nameServers: [],
    cnameTarget: null,
    tunnelName: null,
    connectorToken: null,
    zoneActive: false,
    ...result,
  });

  const [domain] = await db.select().from(domainsTable).where(eq(domainsTable.id, domainId));
  if (!domain) return fail({ skipped: "Domain no longer exists." });

  if (!(await cloudflareReady())) {
    return fail({
      skipped:
        "Cloudflare is not connected yet — save an API token under Settings and this runs itself.",
    });
  }

  const zone = await ensureZone(domain.hostname);
  steps.push({
    step: "Cloudflare zone",
    ok: zone.ok,
    detail: zone.ok
      ? `${rootDomain(domain.hostname)} ${zone.activated ? "active" : "pending nameserver change"}`
      : explainCloudflareError(zone.error ?? "failed", "zone"),
  });
  if (!zone.ok || !zone.zoneId) {
    await db
      .update(domainsTable)
      .set({
        status: "error",
        lastError: zone.error ? explainCloudflareError(zone.error, "zone") : null,
        lastCheckedAt: nowSeconds(),
      })
      .where(eq(domainsTable.id, domain.id));
    return fail();
  }

  const nameServers = zone.nameServers ?? [];
  if (
    setNs &&
    (domain.registrar === "godaddy" || domain.registrar === "namecheap") &&
    nameServers.length > 0 &&
    !zone.activated
  ) {
    const ns = await setNameservers(domain.registrar, domain.hostname, nameServers);
    steps.push({ step: `${domain.registrar} nameservers`, ok: ns.ok, detail: ns.message });
  } else if (nameServers.length > 0 && !zone.activated) {
    steps.push({
      step: "Registrar nameservers",
      ok: true,
      detail: `Set these two at your registrar: ${nameServers.join(", ")}`,
    });
  }

  const tunnelName = `redxaihost-${rootDomain(domain.hostname).replace(/\./g, "-")}`;
  const tunnel = await ensureTunnel(tunnelName);
  steps.push({
    step: "Cloudflare tunnel",
    ok: tunnel.ok,
    detail: tunnel.ok ? tunnelName : explainCloudflareError(tunnel.error ?? "failed", "tunnel"),
  });
  if (!tunnel.ok || !tunnel.tunnelId) {
    await db
      .update(domainsTable)
      .set({
        status: "error",
        lastError: tunnel.error ? explainCloudflareError(tunnel.error, "tunnel") : null,
        lastCheckedAt: nowSeconds(),
      })
      .where(eq(domainsTable.id, domain.id));
    return fail({ nameServers });
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
      : explainCloudflareError(ingress.error ?? "failed", "tunnel"),
  });

  const cname = await upsertTunnelCname(zone.zoneId, domain.hostname, tunnel.tunnelId);
  steps.push({
    step: "DNS record",
    ok: cname.ok,
    detail: cname.ok
      ? `CNAME → ${cname.target}`
      : explainCloudflareError(cname.error ?? "failed", "dns"),
  });

  // Whether the domain is wired up is decided before hardening runs: the edge
  // protection is applied on top, and a zone that cannot be hardened is still a
  // working domain, so a missing security permission must not mark it broken.
  const allOk = steps.every((step) => step.ok);

  const hardening = await hardenZone(zone.zoneId);
  const applied = hardening.steps.filter((step) => step.ok).length;
  const blocked = hardening.steps.filter((step) => step.needsPermission);
  steps.push({
    step: "Edge protection",
    ok: hardening.ok,
    detail: hardening.ok
      ? `${applied}/${hardening.steps.length} applied — DDoS rate limit, bad-bot and probe blocking${
          blocked.length > 0
            ? `. Add ${[...new Set(blocked.map((step) => step.detail))].join("; ")} for the rest.`
            : "."
        }`
      : `Could not apply edge protection: ${hardening.steps.find((step) => !step.ok)?.detail ?? "unknown"}`,
  });

  await db
    .update(domainsTable)
    .set({
      zoneId: zone.zoneId,
      tunnelId: tunnel.tunnelId,
      tunnelName,
      cnameTarget: cname.target ?? null,
      status: allOk ? (zone.activated ? "live" : "dns_set") : "error",
      lastError: allOk ? null : (steps.find((step) => !step.ok)?.detail ?? null),
      lastCheckedAt: nowSeconds(),
    })
    .where(eq(domainsTable.id, domain.id));

  // Bring the connector up on the machine that runs the project, without the
  // owner having to touch cloudflared themselves.
  if (allOk && project && project.status === "running") {
    const nodeId = await pickNode(project.nodeId);
    if (nodeId) {
      const deploymentId = newId("dep");
      await db.insert(deployments).values({
        id: deploymentId,
        projectId: project.id,
        nodeId,
        action: "restart",
        status: "queued",
        trigger: trigger === "auto" ? "auto" : "manual",
      });
      await db
        .update(projectsTable)
        .set({ nodeId, status: "deploying", updatedAt: nowSeconds() })
        .where(eq(projectsTable.id, project.id));
      steps.push({
        step: "Node connector",
        ok: true,
        detail: `Restart queued on the node so cloudflared comes up for ${domain.hostname}.`,
      });
    } else {
      steps.push({
        step: "Node connector",
        ok: true,
        detail: "No node online — the tunnel starts on the next deploy.",
      });
    }
  }

  await logActivity({
    scope: "domain",
    refId: domain.id,
    level: allOk ? "success" : "error",
    message: allOk
      ? `${domain.hostname} wired through Cloudflare Tunnel${trigger === "auto" ? " automatically" : ""}.`
      : `${domain.hostname} connect failed: ${steps.find((step) => !step.ok)?.detail}`,
    meta: steps,
  });

  return {
    ok: allOk,
    steps,
    nameServers,
    cnameTarget: cname.target ?? null,
    tunnelName,
    connectorToken: tunnel.connectorToken ?? null,
    zoneActive: Boolean(zone.activated),
  };
}
