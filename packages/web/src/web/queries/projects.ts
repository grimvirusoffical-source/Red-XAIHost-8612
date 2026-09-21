import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { orpc } from "../lib/api";

export function useProjects() {
  return useQuery({
    ...orpc.projects.list.queryOptions(),
    refetchInterval: 10_000,
  });
}

export function useProject(id: string) {
  return useQuery({
    ...orpc.projects.get.queryOptions({ input: { id } }),
    enabled: Boolean(id),
    refetchInterval: 8_000,
  });
}

function useProjectInvalidation() {
  const qc = useQueryClient();
  return () => {
    void qc.invalidateQueries({ queryKey: orpc.projects.key() });
    void qc.invalidateQueries({ queryKey: orpc.overview.key() });
  };
}

export function useCreateProject() {
  const invalidate = useProjectInvalidation();
  return useMutation({ ...orpc.projects.create.mutationOptions(), onSuccess: invalidate });
}

export function useUpdateProject() {
  const invalidate = useProjectInvalidation();
  return useMutation({ ...orpc.projects.update.mutationOptions(), onSuccess: invalidate });
}

export function useRemoveProject() {
  const invalidate = useProjectInvalidation();
  return useMutation({ ...orpc.projects.remove.mutationOptions(), onSuccess: invalidate });
}

export function useDeployProject() {
  const invalidate = useProjectInvalidation();
  return useMutation({ ...orpc.projects.deploy.mutationOptions(), onSuccess: invalidate });
}

export function useAiPlan() {
  const invalidate = useProjectInvalidation();
  return useMutation({ ...orpc.projects.aiPlan.mutationOptions(), onSuccess: invalidate });
}

export function usePresignBundle() {
  return useMutation(orpc.projects.presignBundle.mutationOptions());
}
