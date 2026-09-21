import { z } from "zod";
import { desc, gte } from "drizzle-orm";
import { owner } from "../middleware/auth";
import { db } from "../database";
import { activity, projects, usageEvents } from "../database/schema";
import { nowSeconds } from "../lib/ids";

export const usage = {
  /** Traffic rollups for the charts: per-hour totals plus a per-project split. */
  series: owner
    .input(z.object({ days: z.number().int().min(1).max(90).default(7) }))
    .handler(async ({ input }) => {
      const since = nowSeconds() - input.days * 86400;
      const rows = await db.select().from(usageEvents).where(gte(usageEvents.bucketAt, since));
      const projectRows = await db.select().from(projects);

      const byBucket = new Map<number, { requests: number; errors: number; bytesOut: number }>();
      const byProject = new Map<
        string,
        { requests: number; errors: number; bytesOut: number; visitors: number }
      >();

      for (const row of rows) {
        const bucket = byBucket.get(row.bucketAt) ?? { requests: 0, errors: 0, bytesOut: 0 };
        bucket.requests += row.requests;
        bucket.errors += row.errors;
        bucket.bytesOut += row.bytesOut;
        byBucket.set(row.bucketAt, bucket);

        const project = byProject.get(row.projectId) ?? {
          requests: 0,
          errors: 0,
          bytesOut: 0,
          visitors: 0,
        };
        project.requests += row.requests;
        project.errors += row.errors;
        project.bytesOut += row.bytesOut;
        project.visitors += row.uniqueVisitors;
        byProject.set(row.projectId, project);
      }

      return {
        buckets: [...byBucket.entries()]
          .sort((a, b) => a[0] - b[0])
          .map(([bucketAt, value]) => ({ bucketAt, ...value })),
        projects: [...byProject.entries()]
          .map(([projectId, value]) => ({
            projectId,
            name: projectRows.find((project) => project.id === projectId)?.name ?? "deleted project",
            ...value,
          }))
          .sort((a, b) => b.requests - a.requests),
        totals: {
          requests: rows.reduce((sum, row) => sum + row.requests, 0),
          errors: rows.reduce((sum, row) => sum + row.errors, 0),
          bytesOut: rows.reduce((sum, row) => sum + row.bytesOut, 0),
          visitors: rows.reduce((sum, row) => sum + row.uniqueVisitors, 0),
        },
      };
    }),

  feed: owner
    .input(z.object({ limit: z.number().int().min(1).max(200).default(40) }))
    .handler(async ({ input }) => {
      return db.select().from(activity).orderBy(desc(activity.createdAt)).limit(input.limit);
    }),
};
