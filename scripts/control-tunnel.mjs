import { resolve } from "node:path";
import { ensureTunnel, ensureZone, setTunnelIngress, upsertTunnelCname } from "../packages/web/src/api/lib/cloudflare.ts";

const hostname = process.env.REDX_CONTROL_HOST || "host.infectedvoices.space";
const port = Number(process.env.PORT || 4200);
const tunnelName = process.env.REDX_CONTROL_TUNNEL || "redxaihost-control-plane";
const root = resolve(import.meta.dirname, "..");
const binary = resolve(root, "data", "worker", "bin", process.platform === "win32" ? "cloudflared.exe" : "cloudflared");

const zone = await ensureZone(hostname);
if (!zone.ok || !zone.zoneId) throw new Error(zone.error || "Cloudflare zone unavailable.");
const tunnel = await ensureTunnel(tunnelName);
if (!tunnel.ok || !tunnel.tunnelId || !tunnel.connectorToken) throw new Error(tunnel.error || "Tunnel unavailable.");
const ingress = await setTunnelIngress(tunnel.tunnelId, [{ hostname, service: "http://127.0.0.1:" + port }]);
if (!ingress.ok) throw new Error(ingress.error || "Tunnel ingress failed.");
const dns = await upsertTunnelCname(zone.zoneId, hostname, tunnel.tunnelId);
if (!dns.ok) throw new Error(dns.error || "Tunnel DNS failed.");

console.log("RedXAIHost control tunnel ready:", hostname, "-> 127.0.0.1:" + port);
const child = Bun.spawn([binary, "tunnel", "--no-autoupdate", "run"], {
  cwd: root,
  stdout: "inherit",
  stderr: "inherit",
  env: { ...process.env, TUNNEL_TOKEN: tunnel.connectorToken },
});
process.exit(await child.exited);
