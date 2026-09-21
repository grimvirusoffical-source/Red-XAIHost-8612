import { useQuery } from "@tanstack/react-query";
import { orpc } from "../lib/api";

export function useOverview() {
  return useQuery({
    ...orpc.overview.queryOptions(),
    refetchInterval: 15_000,
  });
}

export function useActivityFeed(limit = 40) {
  return useQuery({
    ...orpc.usage.feed.queryOptions({ input: { limit } }),
    refetchInterval: 20_000,
  });
}
