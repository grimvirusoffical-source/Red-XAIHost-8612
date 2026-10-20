import { useQuery } from "@tanstack/react-query";
import { orpc } from "../lib/api";

/** Server-decided session role. Keyed by the session user so a new sign-in refetches. */
export function useMe(userId: string | null | undefined) {
  return useQuery({ ...orpc.me.queryOptions(), enabled: Boolean(userId), retry: false, queryKey: ["me", userId ?? ""] });
}
