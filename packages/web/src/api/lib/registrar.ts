import { detectPublicIp, getCredential } from "./credentials";
import { rootDomain } from "./cloudflare";

/**
 * Registrar automation runs on each registrar's official API — GoDaddy's
 * sso-key API and Namecheap's XML API. We deliberately do not drive the
 * registrar's web login with a headless browser: it violates their terms,
 * breaks on every 2FA prompt or layout change, and risks the domain account
 * being locked. The only job here is pointing a domain's nameservers at
 * Cloudflare, which both APIs support directly.
 */

export type Registrar = "godaddy" | "namecheap";

function splitDomain(domain: string): { sld: string; tld: string } {
  const root = rootDomain(domain);
  const idx = root.indexOf(".");
  return { sld: root.slice(0, idx), tld: root.slice(idx + 1) };
}

export async function listRegistrarDomains(
  registrar: Registrar,
): Promise<{ ok: boolean; domains: string[]; error?: string }> {
  try {
    if (registrar === "godaddy") {
      const creds = await getCredential("godaddy");
      if (!creds) return { ok: false, domains: [], error: "GoDaddy credentials not configured." };
      const res = await fetch("https://api.godaddy.com/v1/domains?limit=500", {
        headers: { Authorization: `sso-key ${creds.apiKey}:${creds.apiSecret}` },
      });
      if (!res.ok) return { ok: false, domains: [], error: `GoDaddy returned ${res.status}` };
      const body = (await res.json()) as { domain: string; status: string }[];
      return { ok: true, domains: body.map((d) => d.domain) };
    }

    const creds = await getCredential("namecheap");
    if (!creds) return { ok: false, domains: [], error: "Namecheap credentials not configured." };
    const url = new URL("https://api.namecheap.com/xml.response");
    url.searchParams.set("ApiUser", creds.apiUser);
    url.searchParams.set("ApiKey", creds.apiKey);
    url.searchParams.set("UserName", creds.apiUser);
    url.searchParams.set("ClientIp", creds.clientIp || (await detectPublicIp()));
    url.searchParams.set("Command", "namecheap.domains.getList");
    url.searchParams.set("PageSize", "100");
    const res = await fetch(url);
    const xml = await res.text();
    if (!xml.includes('Status="OK"')) {
      const err = /<Error[^>]*>([^<]+)<\/Error>/.exec(xml)?.[1];
      return { ok: false, domains: [], error: err ?? "Namecheap rejected the request." };
    }
    const names = [...xml.matchAll(/Name="([^"]+)"/g)]
      .map((m) => m[1]!)
      .filter((name) => name.includes("."));
    return { ok: true, domains: [...new Set(names)] };
  } catch (error) {
    return {
      ok: false,
      domains: [],
      error: error instanceof Error ? error.message : "Registrar lookup failed.",
    };
  }
}

/** Point a domain's nameservers at Cloudflare so tunnels and proxied DNS work. */
export async function setNameservers(
  registrar: Registrar,
  domain: string,
  nameServers: string[],
): Promise<{ ok: boolean; message: string }> {
  try {
    const root = rootDomain(domain);

    if (registrar === "godaddy") {
      const creds = await getCredential("godaddy");
      if (!creds) return { ok: false, message: "GoDaddy credentials not configured." };
      const res = await fetch(`https://api.godaddy.com/v1/domains/${encodeURIComponent(root)}`, {
        method: "PATCH",
        headers: {
          Authorization: `sso-key ${creds.apiKey}:${creds.apiSecret}`,
          "Content-Type": "application/json",
        },
        body: JSON.stringify({ nameServers }),
      });
      if (res.ok || res.status === 204) {
        return { ok: true, message: `GoDaddy nameservers updated for ${root}.` };
      }
      const text = await res.text();
      return { ok: false, message: `GoDaddy refused the update (${res.status}): ${text.slice(0, 200)}` };
    }

    const creds = await getCredential("namecheap");
    if (!creds) return { ok: false, message: "Namecheap credentials not configured." };
    const { sld, tld } = splitDomain(root);
    const url = new URL("https://api.namecheap.com/xml.response");
    url.searchParams.set("ApiUser", creds.apiUser);
    url.searchParams.set("ApiKey", creds.apiKey);
    url.searchParams.set("UserName", creds.apiUser);
    url.searchParams.set("ClientIp", creds.clientIp || (await detectPublicIp()));
    url.searchParams.set("Command", "namecheap.domains.dns.setCustom");
    url.searchParams.set("SLD", sld);
    url.searchParams.set("TLD", tld);
    url.searchParams.set("Nameservers", nameServers.join(","));
    const res = await fetch(url);
    const xml = await res.text();
    if (xml.includes('Status="OK"')) {
      return { ok: true, message: `Namecheap nameservers updated for ${root}.` };
    }
    const err = /<Error[^>]*>([^<]+)<\/Error>/.exec(xml)?.[1];
    return { ok: false, message: err ?? "Namecheap refused the update." };
  } catch (error) {
    return {
      ok: false,
      message: error instanceof Error ? error.message : "Nameserver update failed.",
    };
  }
}
