/**
 * Edge hardening for every zone this panel manages.
 *
 * The node behind a tunnel is a home PC or a small VPS, so it cannot absorb a
 * flood — the protection has to happen at Cloudflare, before traffic is ever
 * tunnelled down. This applies a balanced baseline: real visitors never see a
 * challenge, obvious floods and unverified bots do.
 *
 * Every step degrades on its own. A zone on the free plan, or a token missing
 * one of the newer permissions, still gets whatever the rest of the steps can
 * do rather than failing the whole domain — the panel reports exactly which
 * parts applied so the owner can see what is and is not protecting them.
 */
import { cf } from "./cloudflare-token";
import { cloudflareConfig } from "./cloudflare";

export interface HardeningStep {
  step: string;
  ok: boolean;
  detail: string;
  /** True when the step failed only because the token lacks the permission. */
  needsPermission?: boolean;
}

export interface HardeningResult {
  ok: boolean;
  steps: HardeningStep[];
}

const RATE_LIMIT_RULE_REF = "redxaihost_flood_guard";
const WAF_RULE_REF = "redxaihost_bot_guard";

function looksLikePermission(error: string | undefined): boolean {
  const raw = (error ?? "").toLowerCase();
  return (
    raw.includes("authentication error") ||
    raw.includes("unauthorized") ||
    raw.includes("requires permission") ||
    raw.includes("not entitled")
  );
}

/**
 * Zone settings are one call each, and a plan that does not include a setting
 * rejects only that one, so they are applied independently.
 */
async function applyZoneSettings(token: string, zoneId: string): Promise<HardeningStep[]> {
  const wanted: { id: string; value: string; label: string }[] = [
    { id: "security_level", value: "medium", label: "Security level: medium" },
    { id: "always_use_https", value: "on", label: "Always use HTTPS" },
    { id: "min_tls_version", value: "1.2", label: "Minimum TLS 1.2" },
    { id: "browser_check", value: "on", label: "Browser integrity check" },
  ];

  const steps: HardeningStep[] = [];
  for (const setting of wanted) {
    const res = await cf(`/zones/${zoneId}/settings/${setting.id}`, {
      token,
      method: "PATCH",
      body: JSON.stringify({ value: setting.value }),
    });
    steps.push({
      step: setting.label,
      ok: res.ok,
      detail: res.ok
        ? "applied"
        : looksLikePermission(res.error)
          ? "needs Zone · Zone Settings · Edit on your Cloudflare token"
          : (res.error ?? "Cloudflare rejected it"),
      needsPermission: !res.ok && looksLikePermission(res.error),
    });
  }
  return steps;
}

/**
 * Bot Fight Mode. Free on every plan, and the single most effective switch
 * against scripted traffic. Cloudflare has never documented this endpoint in
 * the token-template permission table, so it is attempted and reported rather
 * than assumed to work.
 */
async function applyBotFightMode(token: string, zoneId: string): Promise<HardeningStep> {
  const res = await cf(`/zones/${zoneId}/bot_management`, {
    token,
    method: "PUT",
    body: JSON.stringify({ fight_mode: true }),
  });
  return {
    step: "Bot Fight Mode",
    ok: res.ok,
    detail: res.ok
      ? "on — Cloudflare challenges scripted traffic at the edge"
      : looksLikePermission(res.error)
        ? "needs Zone · Bot Management · Edit on your Cloudflare token"
        : (res.error ?? "Cloudflare rejected it"),
    needsPermission: !res.ok && looksLikePermission(res.error),
  };
}

/**
 * One rate-limiting rule via the rulesets API, which is the only way to manage
 * these programmatically. The free plan allows a single rule, so the rule is
 * matched by its ref and updated in place instead of a second one being added.
 */
async function applyRateLimit(token: string, zoneId: string): Promise<HardeningStep> {
  const phase = `/zones/${zoneId}/rulesets/phases/http_ratelimit/entrypoint`;
  const rule = {
    ref: RATE_LIMIT_RULE_REF,
    description: "RedXAIHost flood guard",
    expression: "true",
    action: "managed_challenge",
    ratelimit: {
      characteristics: ["ip.src", "cf.colo.id"],
      period: 10,
      requests_per_period: 100,
      mitigation_timeout: 60,
    },
  };

  const existing = await cf<{ rules?: { id: string; ref?: string }[] }>(phase, {
    token,
    method: "GET",
  });

  // No ruleset yet in this phase: create the entrypoint with the rule in it.
  if (!existing.ok) {
    if (looksLikePermission(existing.error)) {
      return {
        step: "Rate limiting",
        ok: false,
        detail: "needs Zone · Firewall Services · Edit on your Cloudflare token",
        needsPermission: true,
      };
    }
    const created = await cf(phase, {
      token,
      method: "PUT",
      body: JSON.stringify({ rules: [rule] }),
    });
    return {
      step: "Rate limiting",
      ok: created.ok,
      detail: created.ok
        ? "100 requests / 10s per IP, then a managed challenge for 60s"
        : (created.error ?? "Cloudflare rejected it"),
    };
  }

  const rules = existing.result?.rules ?? [];
  const mine = rules.find((r) => r.ref === RATE_LIMIT_RULE_REF);
  const next = mine
    ? rules.map((r) => (r.ref === RATE_LIMIT_RULE_REF ? { ...r, ...rule } : r))
    : [...rules, rule];

  const saved = await cf(phase, {
    token,
    method: "PUT",
    body: JSON.stringify({ rules: next }),
  });
  return {
    step: "Rate limiting",
    ok: saved.ok,
    detail: saved.ok
      ? "100 requests / 10s per IP, then a managed challenge for 60s"
      : looksLikePermission(saved.error)
        ? "needs Zone · Firewall Services · Edit on your Cloudflare token"
        : (saved.error ?? "Cloudflare rejected it — the free plan allows one rule"),
    needsPermission: !saved.ok && looksLikePermission(saved.error),
  };
}

/**
 * A WAF custom rule that managed-challenges unverified bots hitting anything
 * that looks like an admin or config path. This is the layer Bot Fight Mode
 * does not cover: targeted probing rather than volume.
 */
async function applyWafRule(token: string, zoneId: string): Promise<HardeningStep> {
  const phase = `/zones/${zoneId}/rulesets/phases/http_request_firewall_custom/entrypoint`;
  const rule = {
    ref: WAF_RULE_REF,
    description: "RedXAIHost probe guard",
    expression:
      '(not cf.client.bot) and (http.request.uri.path contains "/.env" or http.request.uri.path contains "/.git" or http.request.uri.path contains "/wp-login" or http.request.uri.path contains "/phpmyadmin" or http.request.uri.path contains "/.aws")',
    action: "block",
  };

  const existing = await cf<{ rules?: { id: string; ref?: string }[] }>(phase, {
    token,
    method: "GET",
  });

  if (!existing.ok) {
    if (looksLikePermission(existing.error)) {
      return {
        step: "Probe blocking",
        ok: false,
        detail: "needs Zone · Firewall Services · Edit on your Cloudflare token",
        needsPermission: true,
      };
    }
    const created = await cf(phase, {
      token,
      method: "PUT",
      body: JSON.stringify({ rules: [rule] }),
    });
    return {
      step: "Probe blocking",
      ok: created.ok,
      detail: created.ok
        ? "blocks bots probing for /.env, /.git, /wp-login and similar"
        : (created.error ?? "Cloudflare rejected it"),
    };
  }

  const rules = existing.result?.rules ?? [];
  const mine = rules.find((r) => r.ref === WAF_RULE_REF);
  const next = mine
    ? rules.map((r) => (r.ref === WAF_RULE_REF ? { ...r, ...rule } : r))
    : [...rules, rule];

  const saved = await cf(phase, {
    token,
    method: "PUT",
    body: JSON.stringify({ rules: next }),
  });
  return {
    step: "Probe blocking",
    ok: saved.ok,
    detail: saved.ok
      ? "blocks bots probing for /.env, /.git, /wp-login and similar"
      : looksLikePermission(saved.error)
        ? "needs Zone · Firewall Services · Edit on your Cloudflare token"
        : (saved.error ?? "Cloudflare rejected it"),
    needsPermission: !saved.ok && looksLikePermission(saved.error),
  };
}

/**
 * Applies the whole baseline to one zone. Safe to call repeatedly — every step
 * is an upsert, so re-running it on an already-hardened zone changes nothing.
 */
export async function hardenZone(zoneId: string): Promise<HardeningResult> {
  const config = await cloudflareConfig();
  if (!config) {
    return {
      ok: false,
      steps: [{ step: "Cloudflare", ok: false, detail: "Cloudflare credentials not configured." }],
    };
  }

  const steps: HardeningStep[] = [];
  steps.push(...(await applyZoneSettings(config.token, zoneId)));
  steps.push(await applyBotFightMode(config.token, zoneId));
  steps.push(await applyRateLimit(config.token, zoneId));
  steps.push(await applyWafRule(config.token, zoneId));

  return { ok: steps.some((step) => step.ok), steps };
}

/** Read-only view of what is currently protecting a zone, for the security panel. */
export async function zoneSecurityStatus(zoneId: string): Promise<{
  ok: boolean;
  securityLevel?: string;
  alwaysUseHttps?: boolean;
  minTlsVersion?: string;
  botFightMode?: boolean;
  rateLimitRules: number;
  wafRules: number;
  error?: string;
}> {
  const config = await cloudflareConfig();
  if (!config) return { ok: false, rateLimitRules: 0, wafRules: 0, error: "not configured" };
  const token = config.token;

  const [settings, bots, rateLimit, waf] = await Promise.all([
    cf<{ id: string; value: string }[]>(`/zones/${zoneId}/settings`, { token, method: "GET" }),
    cf<{ fight_mode?: boolean }>(`/zones/${zoneId}/bot_management`, { token, method: "GET" }),
    cf<{ rules?: unknown[] }>(`/zones/${zoneId}/rulesets/phases/http_ratelimit/entrypoint`, {
      token,
      method: "GET",
    }),
    cf<{ rules?: unknown[] }>(
      `/zones/${zoneId}/rulesets/phases/http_request_firewall_custom/entrypoint`,
      { token, method: "GET" },
    ),
  ]);

  const setting = (id: string) => settings.result?.find((row) => row.id === id)?.value;

  return {
    ok: settings.ok || bots.ok || rateLimit.ok || waf.ok,
    securityLevel: setting("security_level"),
    alwaysUseHttps: setting("always_use_https") === "on",
    minTlsVersion: setting("min_tls_version"),
    botFightMode: bots.ok ? Boolean(bots.result?.fight_mode) : undefined,
    rateLimitRules: rateLimit.result?.rules?.length ?? 0,
    wafRules: waf.result?.rules?.length ?? 0,
    error: settings.ok ? undefined : settings.error,
  };
}
