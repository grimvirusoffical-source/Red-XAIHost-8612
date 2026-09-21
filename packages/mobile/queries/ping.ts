import { useQuery } from "@tanstack/react-query";
import { orpc } from "@/lib/api";

// Data hooks live here, one file per feature — each wraps its query/mutation
// options (staleTime, select, invalidation) so screens just call the hook.
// Mirrors packages/web/src/web/queries/. Patterns: skills/app/references/mobile.md
export function usePing() {
  return useQuery(orpc.ping.queryOptions());
}
