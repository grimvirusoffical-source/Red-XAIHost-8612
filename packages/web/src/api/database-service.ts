import type { Hono } from "hono";
import { and, desc, eq, isNull } from "drizzle-orm";
import { randomBytes } from "node:crypto";
import { auth, isOwnerEmail } from "./auth";
import { db } from "./database";
import {
  redxAccessTokens,
  redxApiKeys,
  redxAudit,
  redxDatabases,
} from "./database/schema";
import { hashToken, tokenPreview } from "./lib/crypto";
import { newId, nowSeconds } from "./lib/ids";

const MAX_SOURCE_BYTES = 2 * 1024 * 1024;
const validScopes = new Set(["read", "write", "admin"]);

function normalizeName(input: string): string {
  let value = input.trim();
  if (!value) throw new Error("Database name is required.");
  value = value.replace(/\.[^./\\]+$/u, "");
  value = value.replace(/[\\/:*?"<>|\u0000-\u001f]/gu, "-").trim();
  if (!value) throw new Error("Database name is invalid.");
  if (value.length > 120) throw new Error("Database name is too long.");
  return value + ".Red-XAI";
}

function validateSource(source: string): string {
  if (typeof source !== "string") throw new Error("Database source must be text.");
  if (Buffer.byteLength(source, "utf8") > MAX_SOURCE_BYTES) {
    throw new Error("Database source exceeds the 2 MiB limit.");
  }
  return source;
}

function normalizeScopes(input: unknown): string {
  const requested = Array.isArray(input)
    ? input.map(String)
    : String(input ?? "read").split(",");
  const scopes = [...new Set(requested.map((v) => v.trim().toLowerCase()).filter(Boolean))];
  if (!scopes.length || scopes.some((scope) => !validScopes.has(scope))) {
    throw new Error("Scopes must be read, write, or admin.");
  }
  if (scopes.includes("admin")) return "read,write,admin";
  if (scopes.includes("write") && !scopes.includes("read")) scopes.unshift("read");
  return scopes.join(",");
}

function hasScope(scopes: string, wanted: "read" | "write" | "admin"): boolean {
  const set = new Set(scopes.split(",").map((v) => v.trim()));
  if (set.has("admin")) return true;
  if (wanted === "read" && set.has("write")) return true;
  return set.has(wanted);
}

function requestIp(headers: Headers): string | null {
  return (
    headers.get("cf-connecting-ip") ||
    headers.get("x-forwarded-for")?.split(",")[0]?.trim() ||
    null
  );
}

async function sessionFor(headers: Headers) {
  return auth.api.getSession({ headers });
}

async function audit(
  headers: Headers,
  action: string,
  userId?: string | null,
  databaseId?: string | null,
  meta?: unknown,
) {
  await db.insert(redxAudit).values({
    id: newId("audit"),
    userId: userId ?? null,
    databaseId: databaseId ?? null,
    action,
    ip: requestIp(headers),
    userAgent: headers.get("user-agent"),
    meta: meta === undefined ? null : JSON.stringify(meta),
    createdAt: nowSeconds(),
  });
}

async function ownedDatabase(userId: string, id: string) {
  const [row] = await db
    .select()
    .from(redxDatabases)
    .where(and(eq(redxDatabases.id, id), eq(redxDatabases.ownerUserId, userId)))
    .limit(1);
  return row ?? null;
}

async function authenticateApiKey(headers: Headers, databaseId: string) {
  const raw = headers.get("authorization")?.replace(/^Bearer\s+/i, "").trim();
  if (!raw?.startsWith("rxdb_")) return null;
  const hash = hashToken(raw);
  const [key] = await db
    .select()
    .from(redxApiKeys)
    .where(and(eq(redxApiKeys.keyHash, hash), eq(redxApiKeys.databaseId, databaseId)))
    .limit(1);
  if (!key || key.revokedAt) return null;
  const now = nowSeconds();
  if (key.expiresAt && key.expiresAt <= now) return null;
  await db.update(redxApiKeys).set({ lastUsedAt: now }).where(eq(redxApiKeys.id, key.id));
  return key;
}

async function jsonBody(c: any): Promise<any> {
  const type = c.req.header("content-type") ?? "";
  if (!type.toLowerCase().includes("application/json")) {
    throw new Error("Content-Type must be application/json.");
  }
  return c.req.json();
}

export function registerDatabaseServiceRoutes(app: Hono) {
  app.get("/api/database/v1/list", async (c) => {
    const session = await sessionFor(c.req.raw.headers);
    if (!session) return c.json({ error: "unauthorized" }, 401);
    const rows = await db
      .select({
        id: redxDatabases.id,
        name: redxDatabases.name,
        revision: redxDatabases.revision,
        createdAt: redxDatabases.createdAt,
        updatedAt: redxDatabases.updatedAt,
      })
      .from(redxDatabases)
      .where(and(eq(redxDatabases.ownerUserId, session.user.id), isNull(redxDatabases.deletedAt)))
      .orderBy(desc(redxDatabases.updatedAt));
    return c.json({ databases: rows });
  });

  app.post("/api/database/v1/create", async (c) => {
    const session = await sessionFor(c.req.raw.headers);
    if (!session) return c.json({ error: "unauthorized" }, 401);
    try {
      const body = await jsonBody(c);
      const name = normalizeName(String(body.name ?? ""));
      const source = validateSource(String(body.source ?? ""));
      const id = newId("rdb");
      const now = nowSeconds();
      await db.insert(redxDatabases).values({
        id,
        ownerUserId: session.user.id,
        name,
        source,
        revision: 1,
        createdAt: now,
        updatedAt: now,
      });
      await audit(c.req.raw.headers, "database.create", session.user.id, id, { name });
      return c.json({ id, name, revision: 1 }, 201);
    } catch (error) {
      const message = error instanceof Error ? error.message : "invalid_request";
      return c.json({ error: message }, 400);
    }
  });

  app.get("/api/database/v1/:id", async (c) => {
    const session = await sessionFor(c.req.raw.headers);
    if (!session) return c.json({ error: "unauthorized" }, 401);
    const row = await ownedDatabase(session.user.id, c.req.param("id"));
    if (!row || row.deletedAt) return c.json({ error: "not_found" }, 404);
    return c.json({ database: row });
  });

  app.put("/api/database/v1/:id", async (c) => {
    const session = await sessionFor(c.req.raw.headers);
    if (!session) return c.json({ error: "unauthorized" }, 401);
    try {
      const id = c.req.param("id");
      const row = await ownedDatabase(session.user.id, id);
      if (!row || row.deletedAt) return c.json({ error: "not_found" }, 404);
      const body = await jsonBody(c);
      const expectedRevision = Number(body.expectedRevision);
      if (!Number.isInteger(expectedRevision) || expectedRevision !== row.revision) {
        return c.json({ error: "revision_conflict", currentRevision: row.revision }, 409);
      }
      const source = body.source === undefined ? row.source : validateSource(String(body.source));
      const name = body.name === undefined ? row.name : normalizeName(String(body.name));
      const revision = row.revision + 1;
      const updatedAt = nowSeconds();
      await db
        .update(redxDatabases)
        .set({ source, name, revision, updatedAt })
        .where(and(eq(redxDatabases.id, id), eq(redxDatabases.ownerUserId, session.user.id)));
      await audit(c.req.raw.headers, "database.update", session.user.id, id, { revision });
      return c.json({ id, name, revision, updatedAt });
    } catch (error) {
      const message = error instanceof Error ? error.message : "invalid_request";
      return c.json({ error: message }, 400);
    }
  });

  app.delete("/api/database/v1/:id", async (c) => {
    const session = await sessionFor(c.req.raw.headers);
    if (!session) return c.json({ error: "unauthorized" }, 401);
    const id = c.req.param("id");
    const row = await ownedDatabase(session.user.id, id);
    if (!row) return c.json({ error: "not_found" }, 404);
    const deletedAt = nowSeconds();
    await db
      .update(redxDatabases)
      .set({ deletedAt, updatedAt: deletedAt })
      .where(and(eq(redxDatabases.id, id), eq(redxDatabases.ownerUserId, session.user.id)));
    await audit(c.req.raw.headers, "database.delete", session.user.id, id);
    return c.json({ ok: true });
  });

  app.post("/api/database/v1/:id/restore", async (c) => {
    const session = await sessionFor(c.req.raw.headers);
    if (!session) return c.json({ error: "unauthorized" }, 401);
    const id = c.req.param("id");
    const row = await ownedDatabase(session.user.id, id);
    if (!row) return c.json({ error: "not_found" }, 404);
    await db
      .update(redxDatabases)
      .set({ deletedAt: null, updatedAt: nowSeconds() })
      .where(and(eq(redxDatabases.id, id), eq(redxDatabases.ownerUserId, session.user.id)));
    await audit(c.req.raw.headers, "database.restore", session.user.id, id);
    return c.json({ ok: true });
  });

  app.get("/api/database/v1/:id/api-keys", async (c) => {
    const session = await sessionFor(c.req.raw.headers);
    if (!session) return c.json({ error: "unauthorized" }, 401);
    const id = c.req.param("id");
    if (!(await ownedDatabase(session.user.id, id))) return c.json({ error: "not_found" }, 404);
    const rows = await db
      .select({
        id: redxApiKeys.id,
        name: redxApiKeys.name,
        keyPreview: redxApiKeys.keyPreview,
        scopes: redxApiKeys.scopes,
        expiresAt: redxApiKeys.expiresAt,
        lastUsedAt: redxApiKeys.lastUsedAt,
        revokedAt: redxApiKeys.revokedAt,
        createdAt: redxApiKeys.createdAt,
      })
      .from(redxApiKeys)
      .where(and(eq(redxApiKeys.databaseId, id), eq(redxApiKeys.ownerUserId, session.user.id)))
      .orderBy(desc(redxApiKeys.createdAt));
    return c.json({ keys: rows });
  });

  app.post("/api/database/v1/:id/api-keys", async (c) => {
    const session = await sessionFor(c.req.raw.headers);
    if (!session) return c.json({ error: "unauthorized" }, 401);
    try {
      const databaseId = c.req.param("id");
      if (!(await ownedDatabase(session.user.id, databaseId))) return c.json({ error: "not_found" }, 404);
      const body = await jsonBody(c);
      const raw = "rxdb_" + randomBytes(32).toString("base64url");
      const id = newId("key");
      const scopes = normalizeScopes(body.scopes);
      const expiresAt = body.expiresAt == null ? null : Number(body.expiresAt);
      await db.insert(redxApiKeys).values({
        id,
        databaseId,
        ownerUserId: session.user.id,
        name: String(body.name ?? "API Key").slice(0, 80),
        keyHash: hashToken(raw),
        keyPreview: tokenPreview(raw),
        scopes,
        expiresAt: Number.isFinite(expiresAt) ? expiresAt : null,
        createdAt: nowSeconds(),
      });
      await audit(c.req.raw.headers, "api_key.create", session.user.id, databaseId, { id, scopes });
      return c.json({ id, key: raw, keyPreview: tokenPreview(raw), scopes }, 201);
    } catch (error) {
      return c.json({ error: error instanceof Error ? error.message : "invalid_request" }, 400);
    }
  });

  app.post("/api/database/v1/:databaseId/api-keys/:keyId/revoke", async (c) => {
    const session = await sessionFor(c.req.raw.headers);
    if (!session) return c.json({ error: "unauthorized" }, 401);
    const databaseId = c.req.param("databaseId");
    if (!(await ownedDatabase(session.user.id, databaseId))) return c.json({ error: "not_found" }, 404);
    await db
      .update(redxApiKeys)
      .set({ revokedAt: nowSeconds() })
      .where(and(
        eq(redxApiKeys.id, c.req.param("keyId")),
        eq(redxApiKeys.databaseId, databaseId),
        eq(redxApiKeys.ownerUserId, session.user.id),
      ));
    await audit(c.req.raw.headers, "api_key.revoke", session.user.id, databaseId, { id: c.req.param("keyId") });
    return c.json({ ok: true });
  });

  app.get("/api/database/v1/:id/access-tokens", async (c) => {
    const session = await sessionFor(c.req.raw.headers);
    if (!session) return c.json({ error: "unauthorized" }, 401);
    const databaseId = c.req.param("id");
    if (!(await ownedDatabase(session.user.id, databaseId))) return c.json({ error: "not_found" }, 404);
    const rows = await db
      .select({
        id: redxAccessTokens.id,
        name: redxAccessTokens.name,
        tokenId: redxAccessTokens.tokenId,
        boxPath: redxAccessTokens.boxPath,
        tokenPreview: redxAccessTokens.tokenPreview,
        scopes: redxAccessTokens.scopes,
        expiresAt: redxAccessTokens.expiresAt,
        lastUsedAt: redxAccessTokens.lastUsedAt,
        revokedAt: redxAccessTokens.revokedAt,
        createdAt: redxAccessTokens.createdAt,
      })
      .from(redxAccessTokens)
      .where(and(eq(redxAccessTokens.databaseId, databaseId), eq(redxAccessTokens.ownerUserId, session.user.id)))
      .orderBy(desc(redxAccessTokens.createdAt));
    return c.json({ tokens: rows });
  });

  app.post("/api/database/v1/:id/access-tokens", async (c) => {
    const session = await sessionFor(c.req.raw.headers);
    if (!session) return c.json({ error: "unauthorized" }, 401);
    try {
      const databaseId = c.req.param("id");
      if (!(await ownedDatabase(session.user.id, databaseId))) return c.json({ error: "not_found" }, 404);
      const body = await jsonBody(c);
      const tokenId = Number(body.tokenId);
      if (!Number.isInteger(tokenId) || tokenId <= 0) throw new Error("Access token ID must be a positive whole number.");
      const raw = "rxat_" + randomBytes(32).toString("base64url");
      const id = newId("tok");
      const scopes = normalizeScopes(body.scopes);
      const expiresAt = body.expiresAt == null ? null : Number(body.expiresAt);
      await db.insert(redxAccessTokens).values({
        id,
        databaseId,
        ownerUserId: session.user.id,
        name: String(body.name ?? "Access Token").slice(0, 80),
        tokenId,
        boxPath: body.boxPath ? String(body.boxPath).slice(0, 300) : null,
        tokenHash: hashToken(raw),
        tokenPreview: tokenPreview(raw),
        scopes,
        expiresAt: Number.isFinite(expiresAt) ? expiresAt : null,
        createdAt: nowSeconds(),
      });
      await audit(c.req.raw.headers, "access_token.create", session.user.id, databaseId, { id, tokenId, scopes });
      return c.json({ id, token: raw, tokenId, tokenPreview: tokenPreview(raw), scopes }, 201);
    } catch (error) {
      return c.json({ error: error instanceof Error ? error.message : "invalid_request" }, 400);
    }
  });

  app.post("/api/database/v1/:databaseId/access-tokens/:tokenKey/revoke", async (c) => {
    const session = await sessionFor(c.req.raw.headers);
    if (!session) return c.json({ error: "unauthorized" }, 401);
    const databaseId = c.req.param("databaseId");
    if (!(await ownedDatabase(session.user.id, databaseId))) return c.json({ error: "not_found" }, 404);
    await db
      .update(redxAccessTokens)
      .set({ revokedAt: nowSeconds() })
      .where(and(
        eq(redxAccessTokens.id, c.req.param("tokenKey")),
        eq(redxAccessTokens.databaseId, databaseId),
        eq(redxAccessTokens.ownerUserId, session.user.id),
      ));
    await audit(c.req.raw.headers, "access_token.revoke", session.user.id, databaseId, { id: c.req.param("tokenKey") });
    return c.json({ ok: true });
  });

  app.get("/api/database/v1/:id/audit", async (c) => {
    const session = await sessionFor(c.req.raw.headers);
    if (!session) return c.json({ error: "unauthorized" }, 401);
    const databaseId = c.req.param("id");
    if (!(await ownedDatabase(session.user.id, databaseId))) return c.json({ error: "not_found" }, 404);
    const rows = await db
      .select()
      .from(redxAudit)
      .where(eq(redxAudit.databaseId, databaseId))
      .orderBy(desc(redxAudit.createdAt))
      .limit(200);
    return c.json({ events: rows });
  });

  app.get("/api/db/v1/:id/source", async (c) => {
    const databaseId = c.req.param("id");
    const key = await authenticateApiKey(c.req.raw.headers, databaseId);
    if (!key || !hasScope(key.scopes, "read")) return c.json({ error: "unauthorized" }, 401);
    const [row] = await db.select().from(redxDatabases).where(eq(redxDatabases.id, databaseId)).limit(1);
    if (!row || row.deletedAt) return c.json({ error: "not_found" }, 404);
    await audit(c.req.raw.headers, "api.read", key.ownerUserId, databaseId, { keyId: key.id });
    return c.json({ id: row.id, name: row.name, source: row.source, revision: row.revision, updatedAt: row.updatedAt });
  });

  app.put("/api/db/v1/:id/source", async (c) => {
    const databaseId = c.req.param("id");
    const key = await authenticateApiKey(c.req.raw.headers, databaseId);
    if (!key || !hasScope(key.scopes, "write")) return c.json({ error: "unauthorized" }, 401);
    try {
      const [row] = await db.select().from(redxDatabases).where(eq(redxDatabases.id, databaseId)).limit(1);
      if (!row || row.deletedAt) return c.json({ error: "not_found" }, 404);
      const body = await jsonBody(c);
      const expectedRevision = Number(body.expectedRevision);
      if (!Number.isInteger(expectedRevision) || expectedRevision !== row.revision) {
        return c.json({ error: "revision_conflict", currentRevision: row.revision }, 409);
      }
      const source = validateSource(String(body.source ?? ""));
      const revision = row.revision + 1;
      const updatedAt = nowSeconds();
      await db.update(redxDatabases).set({ source, revision, updatedAt }).where(eq(redxDatabases.id, databaseId));
      await audit(c.req.raw.headers, "api.write", key.ownerUserId, databaseId, { keyId: key.id, revision });
      return c.json({ id: databaseId, revision, updatedAt });
    } catch (error) {
      return c.json({ error: error instanceof Error ? error.message : "invalid_request" }, 400);
    }
  });

  app.get("/api/admin/database/v1/overview", async (c) => {
    const session = await sessionFor(c.req.raw.headers);
    if (!session || !isOwnerEmail(session.user.email)) return c.json({ error: "forbidden" }, 403);
    const databases = await db.select().from(redxDatabases).orderBy(desc(redxDatabases.updatedAt)).limit(500);
    const events = await db.select().from(redxAudit).orderBy(desc(redxAudit.createdAt)).limit(200);
    return c.json({
      databaseCount: databases.length,
      databases: databases.map(({ source, ...row }) => ({ ...row, sourceBytes: Buffer.byteLength(source, "utf8") })),
      events,
    });
  });
}
