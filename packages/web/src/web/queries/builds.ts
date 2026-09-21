import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { orpc } from "../lib/api";

export function useBuilds() {
  return useQuery({
    ...orpc.builds.list.queryOptions(),
    refetchInterval: 20_000,
  });
}

export function useBuildTargets() {
  return useQuery(orpc.builds.targets.queryOptions());
}

function useBuildInvalidation() {
  const qc = useQueryClient();
  return () => {
    void qc.invalidateQueries({ queryKey: orpc.builds.key() });
    void qc.invalidateQueries({ queryKey: orpc.overview.key() });
  };
}

export function useDispatchBuild() {
  const invalidate = useBuildInvalidation();
  return useMutation({ ...orpc.builds.dispatch.mutationOptions(), onSuccess: invalidate });
}

export function useSyncBuild() {
  const invalidate = useBuildInvalidation();
  return useMutation({ ...orpc.builds.sync.mutationOptions(), onSuccess: invalidate });
}

export function useRemoveBuild() {
  const invalidate = useBuildInvalidation();
  return useMutation({ ...orpc.builds.remove.mutationOptions(), onSuccess: invalidate });
}
