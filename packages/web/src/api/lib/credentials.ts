import { eq } from "drizzle-orm";
import { db } from "../database";
import { discoverAccount, explainCloudflareError, TOKEN_TEMPLATE_URL, verifyToken } from "./cloudflare-token";
import { credentials } from "../database/schema";
import { decryptJson, encryptJson, keyHint } from "./crypto";
import { nowSeconds } from "./ids";

export type ProviderId = "openai" | "cloudflare" | "godaddy" | "namecheap" | "github" | "expo";

export const PROVIDERS: Record<
  ProviderId,
  { label: string; fields: { key: string; label: string; optional?: boolean }[]; docs: string }
> = {
  openai: {
    label: "OpenAI",
    fields: [{ key: "apiKey", label: "API key (sk-…)" }],
    docs: "https://platform.openai.com/api-keys",
  },
  cloudflare: {
    label: "Cloudflare",
    fields: [
      { key: "apiToken", label: "API token — use the one-click link, permissions come pre-ticked" },
      { key: "accountId", label: "Account ID — found automatically from the token", optional: true },
    ],
    docs: TOKEN_TEMPLATE_URL,
  },
  godaddy: {
    label: "GoDaddy",
    fields: [
      { key: "apiKey", label: "API key" },
      { key: "apiSecret", label: "API secret" },
    ],
    docs: "https://developer.godaddy.com/keys",
  },
  namecheap: {
    label: "Namecheap",
    fields: [
      { key: "apiUser", label: "API user (your Namecheap username)" },
      { key: "apiKey", label: "API key" },
      { key: "clientIp", label: "Whitelisted IP", optional: true },
    ],
    docs: "https://ap.www.namecheap.com/settings/tools/apiaccess/",
  },
  github: {
    label: "GitHub",
    fields: [
      { key: "token", label: "Personal access token (repo + workflow)" },
      { key: "repo", label: "owner/repo", optional: true },
      { key: "webhookSecret", label: "Push webhook secret (for auto-deploy)", optional: true },
    ],
    docs: "https://github.com/settings/tokens",
  },
  expo: {
    label: "Expo / EAS",
    fields: [{ key: "token", label: "EAS access token" }],
    docs: "https://expo.dev/settings/access-tokens",
  },
};

export async function getCredential(provider: ProviderId): Promise<Record<string, string> | null> {
  const [row] = await db.select().from(credentials).where(eq(credentials.id, provider));
  if (!row) return null;
  return decryptJson<Record<string, string>>(row.secretEnc);
}

export async function saveCredential(
  provider: ProviderId,
  values: Record<string, string>,
): Promise<void> {
  const primary = values.apiKey ?? values.apiToken ?? values.token ?? values.apiUser ?? "";
  const row = {
    id: provider,
    label: PROVIDERS[provider].label,
    secretEnc: encryptJson(values),
    hint: keyHint(primary),
    verifyStatus: "unknown" as const,
    verifyMessage: null,
    verifiedAt: null,
    updatedAt: nowSeconds(),
  };
  await db.insert(credentials).values(row).onConflictDoUpdate({ target: credentials.id, set: row });
}

export async function setVerifyResult(
  provider: ProviderId,
  ok: boolean,
  message: string,
): Promise<void> {
  await db
    .update(credentials)
    .set({
      verifyStatus: ok ? "valid" : "invalid",
      verifyMessage: message.slice(0, 500),
      verifiedAt: nowSeconds(),
    })
    .where(eq(credentials.id, provider));
}

/**
 * Live credential check against each provider's own API. `discovered` carries
 * fields the provider itself could tell us — Cloudflare's account id, for one,
 * so the owner never has to go find it by hand.
 */
export async function verifyCredential(
  provider: ProviderId,
  values: Record<string, string>,
): Promise<{ ok: boolean; message: string; discovered?: Record<string, string> }> {
  try {
    if (provider === "openai") {
      const res = await fetch("https://api.openai.com/v1/models", {
        headers: { Authorization: `Bearer ${values.apiKey}` },
      });
      return res.ok
        ? { ok: true, message: "Key accepted by OpenAI." }
        : { ok: false, message: `OpenAI rejected the key (${res.status}).` };
    }

    if (provider === "cloudflare") {
      const probe = await verifyToken(values.apiToken!);
      if (!probe.ok) {
        return { ok: false, message: explainCloudflareError(probe.error) };
      }

      // Find the account id from the token itself, by whichever route the
      // token's permissions allow — listing accounts, or reading it off a zone
      // the token can already see. A zone-scoped token cannot list accounts,
      // and that used to be reported as a broken token.
      const account = await discoverAccount(values.apiToken!, values.accountId ?? null);
      if (!account.id) {
        return {
          ok: false,
          message:
            "Token works, but the panel cannot read your account ID with it. Tick Account · Account Settings · Read (read-only) — or use the one-click token link, which includes it.",
        };
      }

      const named = account.name ? `"${account.name}"` : account.id;
      const autofilled = values.accountId !== account.id;
      return {
        ok: true,
        message: autofilled
          ? `Connected to Cloudflare account ${named} — account ID filled in automatically.`
          : `Connected to Cloudflare account ${named}.`,
        discovered: account.name
          ? { accountId: account.id, accountName: account.name }
          : { accountId: account.id },
      };
    }

    if (provider === "godaddy") {
      const res = await fetch("https://api.godaddy.com/v1/domains?limit=1", {
        headers: { Authorization: `sso-key ${values.apiKey}:${values.apiSecret}` },
      });
      if (res.ok) return { ok: true, message: "GoDaddy API key verified." };
      if (res.status === 401) return { ok: false, message: "GoDaddy rejected the key/secret." };
      if (res.status === 403) {
        return {
          ok: false,
          message:
            "Key authenticated but access is denied — GoDaddy limits production API keys to accounts with 10+ domains or a reseller plan.",
        };
      }
      return { ok: false, message: `GoDaddy returned ${res.status}.` };
    }

    if (provider === "namecheap") {
      const clientIp = values.clientIp || (await detectPublicIp());
      const url = new URL("https://api.namecheap.com/xml.response");
      url.searchParams.set("ApiUser", values.apiUser);
      url.searchParams.set("ApiKey", values.apiKey);
      url.searchParams.set("UserName", values.apiUser);
      url.searchParams.set("ClientIp", clientIp);
      url.searchParams.set("Command", "namecheap.domains.getList");
      url.searchParams.set("PageSize", "10");
      const res = await fetch(url);
      const xml = await res.text();
      if (xml.includes('Status="OK"')) return { ok: true, message: "Namecheap API verified." };
      const err = /<Error[^>]*>([^<]+)<\/Error>/.exec(xml)?.[1];
      return { ok: false, message: err ?? "Namecheap rejected the request." };
    }

    if (provider === "github") {
      const res = await fetch("https://api.github.com/user", {
        headers: {
          Authorization: `Bearer ${values.token}`,
          Accept: "application/vnd.github+json",
          "User-Agent": "redxaihost",
        },
      });
      if (!res.ok) return { ok: false, message: `GitHub rejected the token (${res.status}).` };
      const user = (await res.json()) as { login?: string };
      return { ok: true, message: `Authenticated as ${user.login ?? "unknown"}.` };
    }

    if (provider === "expo") {
      const res = await fetch("https://api.expo.dev/v2/auth/userInfo", {
        headers: { Authorization: `Bearer ${values.token}` },
      });
      return res.ok
        ? { ok: true, message: "Expo token verified." }
        : { ok: false, message: `Expo rejected the token (${res.status}).` };
    }

    return { ok: false, message: "Unknown provider." };
  } catch (error) {
    return { ok: false, message: error instanceof Error ? error.message : "Verification failed." };
  }
}

export async function detectPublicIp(): Promise<string> {
  try {
    const res = await fetch("https://api.ipify.org?format=json");
    const body = (await res.json()) as { ip?: string };
    return body.ip ?? "0.0.0.0";
  } catch {
    return "0.0.0.0";
  }
}
