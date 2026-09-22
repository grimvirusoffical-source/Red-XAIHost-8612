import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { orpc } from "../lib/api";

export function useCloudflareStatus() {
  return useQuery({
    ...orpc.cloudflare.status.queryOptions(),
    refetchInterval: 60_000,
    retry: false,
  });
}

/** Live permission checklist. Lazy: only fetched when the owner opens it. */
export function useCloudflarePermissions(enabled: boolean) {
  return useQuery({
    ...orpc.cloudflare.permissions.queryOptions(),
    enabled,
    retry: false,
    staleTime: 15_000,
  });
}

export function useCloudflareAutoConnect() {
  const qc = useQueryClient();
  return useMutation({
    ...orpc.cloudflare.autoConnect.mutationOptions(),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: orpc.cloudflare.key() });
      void qc.invalidateQueries({ queryKey: orpc.domains.key() });
      void qc.invalidateQueries({ queryKey: orpc.overview.key() });
    },
  });
}

/** What is protecting the hosted sites: panel rate limits plus per-zone edge rules. */
export function useSecurityStatus() {
  return useQuery({
    ...orpc.cloudflare.security.queryOptions(),
    refetchInterval: 120_000,
    retry: false,
  });
}

export function useHardenZones() {
  const qc = useQueryClient();
  return useMutation({
    ...orpc.cloudflare.harden.mutationOptions(),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: orpc.cloudflare.key() });
    },
  });
}

export function useManualSetup(id: string, enabled: boolean) {
  return useQuery({
    ...orpc.domains.manualSetup.queryOptions({ input: { id } }),
    enabled,
    retry: false,
  });
}
