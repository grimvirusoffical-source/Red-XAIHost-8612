import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { orpc } from "../lib/api";

export function useNodes() {
  return useQuery({
    ...orpc.nodes.list.queryOptions(),
    refetchInterval: 10_000,
  });
}

export function useCapacity() {
  return useQuery({
    ...orpc.nodes.capacity.queryOptions(),
    refetchInterval: 10_000,
  });
}

function useNodeInvalidation() {
  const qc = useQueryClient();
  return () => {
    void qc.invalidateQueries({ queryKey: orpc.nodes.key() });
    void qc.invalidateQueries({ queryKey: orpc.overview.key() });
  };
}

export function useCreateNode() {
  const invalidate = useNodeInvalidation();
  return useMutation({
    ...orpc.nodes.create.mutationOptions(),
    onSuccess: invalidate,
  });
}

export function useUpdateNode() {
  const invalidate = useNodeInvalidation();
  return useMutation({
    ...orpc.nodes.update.mutationOptions(),
    onSuccess: invalidate,
  });
}

export function useRotateNodeToken() {
  const invalidate = useNodeInvalidation();
  return useMutation({
    ...orpc.nodes.rotateToken.mutationOptions(),
    onSuccess: invalidate,
  });
}

export function useRemoveNode() {
  const invalidate = useNodeInvalidation();
  return useMutation({
    ...orpc.nodes.remove.mutationOptions(),
    onSuccess: invalidate,
  });
}
