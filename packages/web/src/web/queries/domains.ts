import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { orpc } from "../lib/api";

export function useDomains() {
  return useQuery({
    ...orpc.domains.list.queryOptions(),
    refetchInterval: 20_000,
  });
}

export function useRegistrarDomains(registrar: "godaddy" | "namecheap", enabled: boolean) {
  return useQuery({
    ...orpc.domains.registrarDomains.queryOptions({ input: { registrar } }),
    enabled,
    retry: false,
  });
}

function useDomainInvalidation() {
  const qc = useQueryClient();
  return () => {
    void qc.invalidateQueries({ queryKey: orpc.domains.key() });
    void qc.invalidateQueries({ queryKey: orpc.overview.key() });
  };
}

export function useAddDomain() {
  const invalidate = useDomainInvalidation();
  return useMutation({ ...orpc.domains.add.mutationOptions(), onSuccess: invalidate });
}

export function useUpdateDomain() {
  const invalidate = useDomainInvalidation();
  return useMutation({ ...orpc.domains.update.mutationOptions(), onSuccess: invalidate });
}

export function useRemoveDomain() {
  const invalidate = useDomainInvalidation();
  return useMutation({ ...orpc.domains.remove.mutationOptions(), onSuccess: invalidate });
}

export function useConnectDomain() {
  const invalidate = useDomainInvalidation();
  return useMutation({ ...orpc.domains.connect.mutationOptions(), onSuccess: invalidate });
}

export function useRecheckDomain() {
  const invalidate = useDomainInvalidation();
  return useMutation({ ...orpc.domains.recheck.mutationOptions(), onSuccess: invalidate });
}
