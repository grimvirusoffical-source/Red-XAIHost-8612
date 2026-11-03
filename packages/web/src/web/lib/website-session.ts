import { useEffect, useState } from "react";
import { panelFetch } from "./panel";

type State = {
  enabled: boolean;
  user: { id: string; email: string; name: string; isOwner: boolean } | null;
  pending: boolean;
  failed: boolean;
};

export function useWebsiteSession(): State {
  const [state, setState] = useState<State>({ enabled: false, user: null, pending: true, failed: false });
  useEffect(() => {
    let active = true;
    let request = 0;
    let controller: AbortController | null = null;
    async function check(hide: boolean) {
      const sequence = ++request;
      controller?.abort();
      controller = new AbortController();
      if (hide) setState(previous => ({ ...previous, pending: true }));
      try {
        const response = await panelFetch("/api/website-session", { signal: controller.signal, cache: "no-store" });
        if (!response.ok) throw new Error("Session check unavailable");
        const json = await response.json() as Omit<State, "pending" | "failed">;
        if (!active || sequence !== request) return;
        if (typeof json.enabled !== "boolean" || (json.user && typeof json.user.id !== "string")) throw new Error("Invalid session response");
        setState({ enabled: json.enabled, user: json.user ?? null, pending: false, failed: false });
      } catch {
        if (active && sequence === request) setState(previous => ({ ...previous, user: null, pending: false, failed: true }));
      }
    }
    const focus = () => { if (document.visibilityState !== "hidden") void check(true); };
    void check(true);
    window.addEventListener("focus", focus);
    window.addEventListener("pageshow", focus);
    document.addEventListener("visibilitychange", focus);
    const timer = window.setInterval(() => { if (document.visibilityState !== "hidden") void check(false); }, 30_000);
    return () => {
      active = false;
      controller?.abort();
      window.clearInterval(timer);
      window.removeEventListener("focus", focus);
      window.removeEventListener("pageshow", focus);
      document.removeEventListener("visibilitychange", focus);
    };
  }, []);
  return state;
}
