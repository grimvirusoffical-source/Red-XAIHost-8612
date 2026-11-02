import { ORPCError } from "@orpc/server";
import { eq } from "drizzle-orm";
import { base } from "../__core/app";
import { auth, isOwnerEmail } from "../auth";
import { db } from "../database";
import { user as authUser } from "../database/auth-schema";
import { withTenantContext } from "../lib/tenant-context";
import { websiteIdentity, websiteSessionEnabled } from "../lib/website-session";

export async function resolveHostIdentity(headers: Headers) {
  if (websiteSessionEnabled()) {
    const found = await websiteIdentity(headers);
    if (!found) throw new ORPCError("UNAUTHORIZED");
    const userId = `website:${found.id}`;
    const epoch = new Date(0);
    // Request-local identity only: never manufacture or store an independent
    // long-lived Host login that could outlive website revocation.
    const session = {
      user: { id: userId, email: found.email, name: found.name, image: null,
        emailVerified: false, createdAt: epoch, updatedAt: epoch },
      session: { id: "website-authority", userId, token: "", createdAt: epoch,
        updatedAt: epoch, expiresAt: new Date(found.expiresAt) },
    };
    return { session, tenantId: found.owner ? "owner" : found.id, owner: found.owner };
  }
  const session = await auth.api.getSession({ headers });
  if (!session) throw new ORPCError("UNAUTHORIZED");
  const owner = isOwnerEmail(session.user.email);
  const [stored] = await db
    .select({ redxAccountId: authUser.redxAccountId })
    .from(authUser)
    .where(eq(authUser.id, session.user.id))
    .limit(1);
  const tenantId = owner ? "owner" : stored?.redxAccountId;
  if (!tenantId) {
    throw new ORPCError("FORBIDDEN", {
      message: "Sign in with your Red-XAI account to open your Host workspace.",
    });
  }
  return { session, tenantId, owner };
}

/** Optional auth — context.user is the session user or null. */
export const withUser = base.use(async ({ context, next }) => {
  if (websiteSessionEnabled()) {
    const found = await resolveHostIdentity(context.headers).catch(() => null);
    return next({ context: { user: found?.session.user ?? null, session: found?.session.session ?? null } });
  }
  const session = await auth.api.getSession({ headers: context.headers });
  return next({ context: { user: session?.user ?? null, session: session?.session ?? null } });
});

/** Any authenticated Red-XAI account, isolated to one Host tenant. */
export const tenant = base.use(async ({ context, next }) => {
  const found = await resolveHostIdentity(context.headers);
  return withTenantContext(
    { tenantId: found.tenantId, isOwner: found.owner, userId: found.session.user.id },
    () => next({
      context: {
        user: found.session.user,
        session: found.session.session,
        tenantId: found.tenantId,
        isOwner: found.owner,
      },
    }),
  );
});

/** Infrastructure-wide administration remains owner-only. */
export const owner = base.use(async ({ context, next }) => {
  const found = await resolveHostIdentity(context.headers);
  if (!found.owner) {
    throw new ORPCError("FORBIDDEN", { message: "This operation is owner-only." });
  }
  return withTenantContext(
    { tenantId: "owner", isOwner: true, userId: found.session.user.id },
    () => next({
      context: {
        user: found.session.user,
        session: found.session.session,
        tenantId: "owner",
        isOwner: true,
      },
    }),
  );
});
