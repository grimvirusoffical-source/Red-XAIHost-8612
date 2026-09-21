import { ORPCError } from "@orpc/server";
import { base } from "../__core/app";
import { auth, isOwnerEmail } from "../auth";

/** Optional auth — `context.user` is the session user or null. */
export const withUser = base.use(async ({ context, next }) => {
  const session = await auth.api.getSession({ headers: context.headers });
  return next({
    context: { user: session?.user ?? null, session: session?.session ?? null },
  });
});

/**
 * Every protected procedure in RedXAIHost runs through here: a valid session is
 * not enough, the session's email must be on the owner allow-list.
 */
export const owner = base.use(async ({ context, next }) => {
  const session = await auth.api.getSession({ headers: context.headers });
  if (!session) throw new ORPCError("UNAUTHORIZED");
  if (!isOwnerEmail(session.user.email)) {
    throw new ORPCError("FORBIDDEN", { message: "This control panel is owner-only." });
  }
  return next({ context: { user: session.user, session: session.session } });
});
