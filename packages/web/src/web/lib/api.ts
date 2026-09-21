import { createORPCClient } from "@orpc/client";
import { RPCLink } from "@orpc/client/fetch";
import { createTanstackQueryUtils } from "@orpc/tanstack-query";
import type { AppRouterClient } from "../../api";
import { authClient } from "./auth";

const link = new RPCLink({
  url: `${import.meta.env.VITE_WEBSITE_URL ?? window.location.origin}/api/rpc`,
  headers: () => {
    const token = authClient.managedAuth.getToken();
    return token ? { Authorization: `Bearer ${token}` } : {};
  },
});

/** Direct typed client: await client.ping() */
export const client: AppRouterClient = createORPCClient(link);

/** TanStack Query helpers: useQuery(orpc.ping.queryOptions()) */
export const orpc = createTanstackQueryUtils(client);
