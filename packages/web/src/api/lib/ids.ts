import { rsx } from "../../../../../vendor/rsx-sdk/rsx";

export function newId(prefix: string): string {
  return `${prefix}_${rsx.random(9).toString("base64url")}`;
}

export function nowSeconds(): number {
  return Math.floor(Date.now() / 1000);
}

export function slugify(input: string): string {
  const base = input
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-+|-+$/g, "")
    .slice(0, 40);
  return base || `project-${rsx.random(3).toString("hex")}`;
}
