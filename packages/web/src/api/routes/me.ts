import { isOwnerEmail } from "../auth";
import { withUser } from "../middleware/auth";

/**
 * Who the current session is, as the server sees it. The web client decides
 * whether to show the panel from `owner` here; it keeps no owner list itself.
 */
export const me = withUser.handler(({ context }) => {
  const email = context.user?.email ?? null;
  return { signedIn: Boolean(context.user), email, owner: isOwnerEmail(email) };
});
