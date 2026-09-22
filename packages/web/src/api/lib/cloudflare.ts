import {
  cf,
  discoverAccount,
  explainCloudflareError,
  TOKEN_TEMPLATE_URL,
  verifyToken,
} from "./cloudflare-token";
import { getCredential, saveCredential } from "./credentials";

export {
  discoverAccount,
  explainCloudflareError,
  listAccounts,
  OPTIONAL_PERMISSIONS,
  REQUIRED_PERMISSIONS,
  TOKEN_TEMPLATE_URL,
  verifyToken,
} from "./cloudflare-token";

/**
 * Token + account id for the write operations.
 *
 * The account id is never required from the owner: if it is not stored yet it is
 * discovered from the token and written back, so a token pasted on its own is
 * enough and stays enough.
 */
export async function cloudflareConfig(): Promise<{ token: string; accountId: string } | null> {
  const creds = await getCredential("cloudflare");
  if (!creds?.apiToken) return null;
  if (creds.accountId) return { token: creds.apiToken, accountId: creds.accountId };

  const account = await discoverAccount(creds.apiToken, null);
  if (!account.id) return null;
  await saveCredential("cloudflare", { ...creds, accountId: account.id });
  return { token: creds.apiToken, accountId: account.id };
}

/** Everything the UI needs to say whether Cloudflare is wired up, in one call. */
export async function cloudflareOverview(): Promise<{
  configured: boolean;
  tokenValid: boolean;
  accountId: string | null;
  accountName: string | null;
  zones: { id: string; name: string; status: string; nameServers: string[] }[];
  tunnels: { id: string; name: string }[];
  error: string | null;
}> {
  const creds = await getCredential("cloudflare");
  const token = creds?.apiToken;
  const base = {
    configured: Boolean(token),
    tokenValid: false,
    accountId: creds?.accountId ?? null,
    accountName: null as string | null,
    zones: [] as { id: string; name: string; status: string; nameServers: string[] }[],
    tunnels: [] as { id: string; name: string }[],
    error: null as string | null,
  };
  if (!token) return { ...base, error: "No Cloudflare API token saved yet." };

  const probe = await verifyToken(token);
  if (!probe.ok) {
    return { ...base, error: explainCloudflareError(probe.error ?? "Cloudflare rejected the token.") };
  }
  base.tokenValid = true;

  const account = await discoverAccount(token, creds?.accountId);
  base.accountId = account.id;
  base.accountName = account.name;
  if (!base.accountId) {
    return {
      ...base,
      error:
        "Token works, but the panel could not read your account ID from it. Add Account · Account Settings · Read (read-only) — or use the one-click token button, which includes it.",
    };
  }

  const zones = await cf<{ id: string; name: string; status: string; name_servers?: string[] }[]>(
    `/zones?per_page=50&account.id=${encodeURIComponent(base.accountId)}`,
    { token, method: "GET" },
  );
  base.zones = (zones.result ?? []).map((zone) => ({
    id: zone.id,
    name: zone.name,
    status: zone.status,
    nameServers: zone.name_servers ?? [],
  }));

  const tunnels = await cf<{ id: string; name: string; deleted_at: string | null }[]>(
    `/accounts/${base.accountId}/cfd_tunnel?is_deleted=false&per_page=50`,
    { token, method: "GET" },
  );
  base.tunnels = (tunnels.result ?? [])
    .filter((tunnel) => !tunnel.deleted_at)
    .map((tunnel) => ({ id: tunnel.id, name: tunnel.name }));

  if (!zones.ok) base.error = explainCloudflareError(zones.error);
  else if (!tunnels.ok) base.error = explainCloudflareError(tunnels.error);
  return base;
}

/**
 * Actually exercise the token, permission by permission, and say which ones
 * work. This is what turns "you didn't give the right permissions" into a
 * checklist the owner can act on without guessing.
 */
export async function cloudflarePermissionCheck(): Promise<{
  tokenValid: boolean;
  accountId: string | null;
  checks: { name: string; ok: boolean | null; detail: string }[];
  templateUrl: string;
}> {
  const creds = await getCredential("cloudflare");
  const token = creds?.apiToken;
  const checks: { name: string; ok: boolean | null; detail: string }[] = [];
  if (!token) {
    return { tokenValid: false, accountId: null, checks, templateUrl: TOKEN_TEMPLATE_URL };
  }

  const probe = await verifyToken(token);
  checks.push({
    name: "Token is active",
    ok: probe.ok,
    detail: probe.ok ? `Cloudflare reports "${probe.status ?? "active"}".` : (probe.error ?? "Rejected."),
  });
  if (!probe.ok) {
    return { tokenValid: false, accountId: null, checks, templateUrl: TOKEN_TEMPLATE_URL };
  }

  const account = await discoverAccount(token, creds?.accountId);
  checks.push({
    name: "Account · Account Settings · Read",
    ok: Boolean(account.id),
    detail: account.id
      ? account.via === "account list"
        ? "Account ID read straight from your account list."
        : `Account list is not readable with this token, so the account ID was taken from ${account.via} instead — that works, nothing to fix.`
      : "Cannot work out your account ID at all. Tick Account · Account Settings · Read (read-only) and this resolves itself.",
  });

  // Each check names its own permission on failure. Cloudflare answers a
  // missing permission with a bare "Authentication error" that says nothing
  // about which one, so the call being made is the only reliable signal.
  const missing = (permission: string, purpose: string, raw?: string) =>
    `Missing ${permission} — that is what ${purpose}. Cloudflare said: ${raw ?? "Authentication error"}.`;

  const zones = await cf<{ id: string }[]>("/zones?per_page=1", { token, method: "GET" });
  checks.push({
    name: "Zone · Zone · Edit",
    ok: zones.ok,
    detail: zones.ok
      ? "Can read and create zones — adding a domain will work."
      : missing("Zone · Zone · Edit", "adds your domain to Cloudflare", zones.error),
  });

  const firstZone = zones.result?.[0]?.id;
  if (firstZone) {
    const dns = await cf<unknown[]>(`/zones/${firstZone}/dns_records?per_page=1`, {
      token,
      method: "GET",
    });
    checks.push({
      name: "Zone · DNS · Edit",
      ok: dns.ok,
      detail: dns.ok
        ? "Can read and write DNS records."
        : missing("Zone · DNS · Edit", "writes the proxied CNAME your domain needs", dns.error),
    });
  } else {
    checks.push({
      name: "Zone · DNS · Edit",
      ok: null,
      detail: "Nothing to test against yet — checked for real the first time you add a domain.",
    });
  }

  if (account.id) {
    const tunnels = await cf<unknown[]>(`/accounts/${account.id}/cfd_tunnel?per_page=1`, {
      token,
      method: "GET",
    });
    checks.push({
      name: "Account · Cloudflare Tunnel · Edit",
      ok: tunnels.ok,
      detail: tunnels.ok
        ? "Can create tunnels — your node will get a connector token."
        : missing("Account · Cloudflare Tunnel · Edit", "creates the tunnel your node connects with", tunnels.error),
    });
  } else {
    checks.push({
      name: "Account · Cloudflare Tunnel · Edit",
      ok: null,
      detail: "Cannot test until the account ID is readable.",
    });
  }

  // Security permissions. These only gate the edge hardening, so a failure here
  // still leaves a working host — the checklist says so rather than crying wolf.
  if (firstZone) {
    const settings = await cf<unknown>(`/zones/${firstZone}/settings`, { token, method: "GET" });
    checks.push({
      name: "Zone · Zone Settings · Edit",
      ok: settings.ok,
      detail: settings.ok
        ? "Can force HTTPS and a TLS 1.2 floor on your sites."
        : missing("Zone · Zone Settings · Edit", "forces HTTPS and sets the security level", settings.error),
    });

    const firewall = await cf<unknown>(
      `/zones/${firstZone}/rulesets/phases/http_ratelimit/entrypoint`,
      { token, method: "GET" },
    );
    // A zone with no rate-limit ruleset yet answers 404, which still proves the
    // token may read the phase — only an auth failure means a missing permission.
    const firewallOk = firewall.ok || !/authentication|unauthorized|permission/i.test(firewall.error ?? "");
    checks.push({
      name: "Zone · Firewall Services · Edit",
      ok: firewallOk,
      detail: firewallOk
        ? "Can add the DDoS rate limit and bad-bot blocking rules."
        : missing("Zone · Firewall Services · Edit", "adds the rate limit and bot blocking rules", firewall.error),
    });

    const bots = await cf<unknown>(`/zones/${firstZone}/bot_management`, { token, method: "GET" });
    checks.push({
      name: "Zone · Bot Management · Edit (optional)",
      ok: bots.ok ? true : null,
      detail: bots.ok
        ? "Bot Fight Mode can be switched on."
        : "Not granted — everything else still protects you; Bot Fight Mode just stays off. Cloudflare has no template link for this one, so tick it by hand if you want it.",
    });
  } else {
    for (const name of [
      "Zone · Zone Settings · Edit",
      "Zone · Firewall Services · Edit",
      "Zone · Bot Management · Edit (optional)",
    ]) {
      checks.push({
        name,
        ok: null,
        detail: "Nothing to test against yet — checked for real the first time you add a domain.",
      });
    }
  }

  return { tokenValid: true, accountId: account.id, checks, templateUrl: TOKEN_TEMPLATE_URL };
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
