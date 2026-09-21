import { useQuery } from "@tanstack/react-query";
import { orpc } from "../lib/api";

export function useUsageSeries(days: number) {
  return useQuery({
    ...orpc.usage.series.queryOptions({ input: { days } }),
    refetchInterval: 30_000,
  });
}
