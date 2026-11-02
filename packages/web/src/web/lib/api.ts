import { createORPCClient } from "@orpc/client";
import { RPCLink } from "@orpc/client/fetch";
import { createTanstackQueryUtils } from "@orpc/tanstack-query";
import type { AppRouterClient } from "../../api";
import { authClient, MANAGED_AUTH_ENABLED } from "./auth";
import { panelBase } from "./panel";

const link = new RPCLink({
  url: `${panelBase()}/api/rpc`,
  headers: () => {
    const token = MANAGED_AUTH_ENABLED ? authClient.managedAuth.getToken() : undefined;
    return token ? { Authorization: `Bearer ${token}` } : {};
  },
});

/** Direct typed client: await client.ping() */
export const client: AppRouterClient = createORPCClient(link);

/** TanStack Query helpers: useQuery(orpc.ping.queryOptions()) */
export const orpc = createTanstackQueryUtils(client);
