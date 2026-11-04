import { createORPCClient } from "@orpc/client";
import { RPCLink } from "@orpc/client/fetch";
import { createTanstackQueryUtils } from "@orpc/tanstack-query";
import type { AppRouterClient } from "../../api";
import { panelBase } from "./panel";

const link = new RPCLink({
  url: `${panelBase()}/api/rpc`,
  // Both the canonical website adapter and local Better Auth validate their
  // existing HttpOnly cookies server-side. No browser bearer identity is made.
  fetch: (request, init) => fetch(request, { ...init, credentials: "include" }),
});

/** Direct typed client: await client.ping() */
export const client: AppRouterClient = createORPCClient(link);

/** TanStack Query helpers: useQuery(orpc.ping.queryOptions()) */
export const orpc = createTanstackQueryUtils(client);
