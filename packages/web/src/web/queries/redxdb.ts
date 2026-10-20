import { useQuery } from "@tanstack/react-query";
import { orpc } from "../lib/api";

export function useRedxdbStatus() {
  return useQuery({
    ...orpc.redxdb.status.queryOptions(),
    refetchInterval: 15_000,
  });
}

export function useRedxdbDatabases() {
  return useQuery({
    ...orpc.redxdb.listDatabases.queryOptions(),
    retry: false,
  });
}
