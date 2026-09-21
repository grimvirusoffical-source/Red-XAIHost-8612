import Constants from "expo-constants";
import { createORPCClient } from "@orpc/client";
import { RPCLink } from "@orpc/client/fetch";
import { createTanstackQueryUtils } from "@orpc/tanstack-query";
import type { AppRouterClient } from "@template/web";

const baseUrl = Constants.expoConfig?.extra?.apiUrl ?? process.env.EXPO_PUBLIC_API_URL;

const link = new RPCLink({
  url: `${baseUrl}/api/rpc`,
});

/** Direct typed client: await client.ping() */
export const client: AppRouterClient = createORPCClient(link);

/** TanStack Query helpers: useQuery(orpc.ping.queryOptions()) */
export const orpc = createTanstackQueryUtils(client);
