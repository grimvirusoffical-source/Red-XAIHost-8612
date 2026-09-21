import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { orpc } from "../lib/api";

export function useProviders() {
  return useQuery(orpc.settings.providers.queryOptions());
}

export function useOwnerInfo() {
  return useQuery(orpc.settings.ownerInfo.queryOptions());
}

function useProviderInvalidation() {
  const qc = useQueryClient();
  return () => {
    void qc.invalidateQueries({ queryKey: orpc.settings.key() });
    void qc.invalidateQueries({ queryKey: orpc.overview.key() });
  };
}

export function useSaveProvider() {
  const invalidate = useProviderInvalidation();
  return useMutation({ ...orpc.settings.saveProvider.mutationOptions(), onSuccess: invalidate });
}

export function useVerifyProvider() {
  const invalidate = useProviderInvalidation();
  return useMutation({ ...orpc.settings.verifyProvider.mutationOptions(), onSuccess: invalidate });
}

export function useRemoveProvider() {
  const invalidate = useProviderInvalidation();
  return useMutation({ ...orpc.settings.removeProvider.mutationOptions(), onSuccess: invalidate });
}
