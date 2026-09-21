import { getCredential } from "./credentials";

const API = "https://api.cloudflare.com/client/v4";

interface CfResponse<T> {
  success: boolean;
  result: T;
  errors?: { code: number; message: string }[];
}

async function cf<T>(
  path: string,
  init: RequestInit & { token: string },
): Promise<{ ok: boolean; result?: T; error?: string }> {
  const { token, ...rest } = init;
  const res = await fetch(`${API}${path}`, {
    ...rest,
    headers: {
      Authorization: `Bearer ${token}`,
      "Content-Type": "application/json",
      ...rest.headers,
    },
  });
  const body = (await res.json().catch(() => null)) as CfResponse<T> | null;
  if (!res.ok || !body?.success) {
    return { ok: false, error: body?.errors?.[0]?.message ?? `Cloudflare returned ${res.status}` };
  }
  return { ok: true, result: body.result };
}

export async function cloudflareConfig(): Promise<{ token: string; accountId: string } | null> {
  const creds = await getCredential("cloudflare");
  if (!creds?.apiToken || !creds?.accountId) return null;
  return { token: creds.apiToken, accountId: creds.accountId };
}

/** Root domain of a hostname: app.example.co.uk -> example.co.uk (best-effort). */
export function rootDomain(hostname: string): string {
  const parts = hostname.toLowerCase().split(".").filter(Boolean);
  if (parts.length <= 2) return parts.join(".");
  const twoLevelTlds = new Set(["co.uk", "com.au", "co.nz", "co.in", "com.br", "co.za"]);
  const lastTwo = parts.slice(-2).join(".");
  return twoLevelTlds.has(lastTwo) ? parts.slice(-3).join(".") : lastTwo;
}

/** Find the zone for a domain, or create it — returns the nameservers to set at the registrar. */
export async function ensureZone(hostname: string): Promise<{
  ok: boolean;
  zoneId?: string;
  zoneName?: string;
  nameServers?: string[];
  activated?: boolean;
  error?: string;
}> {
  const config = await cloudflareConfig();
  if (!config) return { ok: false, error: "Cloudflare credentials not configured." };
  const zoneName = rootDomain(hostname);

  const existing = await cf<{ id: string; name: string; name_servers?: string[]; status: string }[]>(
    `/zones?name=${encodeURIComponent(zoneName)}`,
    { token: config.token, method: "GET" },
  );
  if (existing.ok && existing.result && existing.result.length > 0) {
    const zone = existing.result[0]!;
    return {
      ok: true,
      zoneId: zone.id,
      zoneName: zone.name,
      nameServers: zone.name_servers ?? [],
      activated: zone.status === "active",
    };
  }

  const created = await cf<{ id: string; name: string; name_servers?: string[]; status: string }>(
    "/zones",
    {
      token: config.token,
      method: "POST",
      body: JSON.stringify({
        name: zoneName,
        account: { id: config.accountId },
        type: "full",
      }),
    },
  );
  if (!created.ok || !created.result) return { ok: false, error: created.error };
  return {
    ok: true,
    zoneId: created.result.id,
    zoneName: created.result.name,
    nameServers: created.result.name_servers ?? [],
    activated: created.result.status === "active",
  };
}

/** Create (or reuse) a named Cloudflare Tunnel and return its id + connector token. */
export async function ensureTunnel(name: string): Promise<{
  ok: boolean;
  tunnelId?: string;
  connectorToken?: string;
  error?: string;
}> {
  const config = await cloudflareConfig();
  if (!config) return { ok: false, error: "Cloudflare credentials not configured." };

  const list = await cf<{ id: string; name: string; deleted_at: string | null }[]>(
    `/accounts/${config.accountId}/cfd_tunnel?name=${encodeURIComponent(name)}&is_deleted=false`,
    { token: config.token, method: "GET" },
  );
  let tunnelId = list.ok ? list.result?.find((t) => !t.deleted_at)?.id : undefined;

  if (!tunnelId) {
    const created = await cf<{ id: string }>(`/accounts/${config.accountId}/cfd_tunnel`, {
      token: config.token,
      method: "POST",
      body: JSON.stringify({ name, config_src: "cloudflare" }),
    });
    if (!created.ok || !created.result) return { ok: false, error: created.error };
    tunnelId = created.result.id;
  }

  const tokenRes = await cf<string>(
    `/accounts/${config.accountId}/cfd_tunnel/${tunnelId}/token`,
    { token: config.token, method: "GET" },
  );
  return {
    ok: true,
    tunnelId,
    connectorToken: tokenRes.ok ? tokenRes.result : undefined,
    error: tokenRes.ok ? undefined : tokenRes.error,
  };
}

/** Point a tunnel's ingress rules at the local service a node is running. */
export async function setTunnelIngress(
  tunnelId: string,
  rules: { hostname: string; service: string }[],
): Promise<{ ok: boolean; error?: string }> {
  const config = await cloudflareConfig();
  if (!config) return { ok: false, error: "Cloudflare credentials not configured." };
  const ingress = [
    ...rules.map((rule) => ({ hostname: rule.hostname, service: rule.service })),
    { service: "http_status:404" },
  ];
  const res = await cf(`/accounts/${config.accountId}/cfd_tunnel/${tunnelId}/configurations`, {
    token: config.token,
    method: "PUT",
    body: JSON.stringify({ config: { ingress } }),
  });
  return { ok: res.ok, error: res.error };
}

/** Upsert the proxied CNAME that sends the hostname into the tunnel. */
export async function upsertTunnelCname(
  zoneId: string,
  hostname: string,
  tunnelId: string,
): Promise<{ ok: boolean; target?: string; error?: string }> {
  const config = await cloudflareConfig();
  if (!config) return { ok: false, error: "Cloudflare credentials not configured." };
  const target = `${tunnelId}.cfargotunnel.com`;

  const existing = await cf<{ id: string }[]>(
    `/zones/${zoneId}/dns_records?type=CNAME&name=${encodeURIComponent(hostname)}`,
    { token: config.token, method: "GET" },
  );
  const payload = JSON.stringify({
    type: "CNAME",
    name: hostname,
    content: target,
    proxied: true,
    ttl: 1,
  });
  const recordId = existing.ok ? existing.result?.[0]?.id : undefined;
  const res = recordId
    ? await cf(`/zones/${zoneId}/dns_records/${recordId}`, {
        token: config.token,
        method: "PUT",
        body: payload,
      })
    : await cf(`/zones/${zoneId}/dns_records`, {
        token: config.token,
        method: "POST",
        body: payload,
      });
  return { ok: res.ok, target, error: res.error };
}

export async function zoneStatus(
  zoneId: string,
): Promise<{ ok: boolean; status?: string; error?: string }> {
  const config = await cloudflareConfig();
  if (!config) return { ok: false, error: "Cloudflare credentials not configured." };
  const res = await cf<{ status: string }>(`/zones/${zoneId}`, {
    token: config.token,
    method: "GET",
  });
  return { ok: res.ok, status: res.result?.status, error: res.error };
}
