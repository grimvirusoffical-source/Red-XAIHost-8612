/**
 * Panel sign-in backed by InfectedNation (REDX_IDENTITY_SOURCE=nation).
 *
 * Email/password sign-in is checked against Nation's /api/v1/login/email first.
 * Only when Nation accepts the credentials (including its TOTP step) does the
 * local Better Auth flow run, and the local credential is synced to the
 * password Nation just accepted, so the local session is only a cookie jar
 * for the panel UI. Local sign-up is closed in this mode: accounts are created
 * in Nation. The owner allow-list in auth.ts still applies to every user.
 *
 * With REDX_IDENTITY_SOURCE unset or "local" the panel keeps its own accounts.
 */
import { APIError, createAuthMiddleware } from "better-auth/api";
import { nationLoginEmail } from "./nation";
import { CLIENT_IP_HEADER } from "./client-ip";

export type IdentitySource = "nation" | "local";

export function identitySource(env: Record<string, string | undefined> = process.env): IdentitySource {
  const v = (env.REDX_IDENTITY_SOURCE ?? "local").trim().toLowerCase();
  if (v === "nation") return "nation";
  if (v === "local" || v === "") return "local";
  throw new Error(`REDX_IDENTITY_SOURCE must be "nation" or "local", got "${v}".`);
}

function loopbackClient(headers: Headers | undefined): boolean {
  const ip = String(headers?.get(CLIENT_IP_HEADER) ?? "").replace(/^::ffff:/, "");
  return ip === "::1" || /^127(\.\d{1,3}){3}$/.test(ip);
}

export const nationIdentityHook = createAuthMiddleware(async (ctx) => {
  if (identitySource() !== "nation") {
    // Local accounts: the owner account can only be created from this machine (setup), never remotely.
    if (ctx.path === "/sign-up/email" && !loopbackClient(ctx.headers)) {
      throw new APIError("FORBIDDEN", { message: "Create the owner account on the host itself (http://127.0.0.1:4200)." });
    }
    return;
  }

  if (ctx.path === "/sign-up/email") {
    throw new APIError("FORBIDDEN", {
      message: "Accounts are managed by InfectedNation. Sign up there, then sign in here.",
    });
  }
  if (ctx.path !== "/sign-in/email") return;

  const body = (ctx.body ?? {}) as { email?: unknown; password?: unknown; totpCode?: unknown };
  const email = String(body.email ?? "").trim().toLowerCase();
  const password = String(body.password ?? "");
  if (!email || !password) throw new APIError("BAD_REQUEST", { message: "Email and password are required." });

  const nation = await nationLoginEmail(email, password, body.totpCode ? String(body.totpCode) : undefined);
  if (!nation.ok) {
    const message = String(nation.data.error ?? "Sign-in was rejected.");
    if (nation.status >= 500) throw new APIError("SERVICE_UNAVAILABLE", { message: `Identity service: ${message}` });
    throw new APIError("UNAUTHORIZED", { message, needsTotp: Boolean(nation.data.needsTotp) });
  }
  const account = (nation.data.account ?? {}) as {
    email?: unknown; firstName?: unknown; lastName?: unknown; username?: unknown; role?: unknown; emailVerified?: unknown;
  };
  if (String(account.email ?? "").toLowerCase() !== email) {
    throw new APIError("UNAUTHORIZED", { message: "Sign in with your email address." });
  }
  // Only owners provisioned on the Nation host (role "owner" is set solely by provision-owner) map to the panel.
  // When Nation runs email verification, the owner must be verified too.
  if (account.role !== "owner" || account.emailVerified === false) {
    throw new APIError("FORBIDDEN", { message: "This control panel is owner-only." });
  }

  const adapter = ctx.context.internalAdapter;
  const hash = await ctx.context.password.hash(password);
  const found = await adapter.findUserByEmail(email, { includeAccounts: true });
  if (!found) {
    const name =
      [account.firstName, account.lastName].map((p) => String(p ?? "").trim()).filter(Boolean).join(" ") ||
      String(account.username ?? email);
    // createUser runs the owner allow-list hook from auth.ts; non-owners are refused here.
    const user = await adapter.createUser({ email, name, emailVerified: true });
    await adapter.linkAccount({ userId: user.id, providerId: "credential", accountId: user.id, password: hash });
    return;
  }
  if (found.accounts.some((a: { providerId: string }) => a.providerId === "credential")) {
    await adapter.updatePassword(found.user.id, hash);
  } else {
    await adapter.linkAccount({ userId: found.user.id, providerId: "credential", accountId: found.user.id, password: hash });
  }
});
