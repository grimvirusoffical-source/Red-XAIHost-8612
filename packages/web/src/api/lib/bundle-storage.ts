import type { Hono } from "hono";
import { S3Client, GetObjectCommand, PutObjectCommand } from "@aws-sdk/client-s3";
import { getSignedUrl } from "@aws-sdk/s3-request-presigner";
import { createHmac, timingSafeEqual } from "node:crypto";
import { mkdirSync, existsSync } from "node:fs";
import { readFile, writeFile } from "node:fs/promises";
import { resolve } from "node:path";
import { fileURLToPath } from "node:url";

const repoRoot=fileURLToPath(new URL("../../../../../",import.meta.url));
const localRoot=resolve(repoRoot,process.env.REDX_DATA_DIR||"data","bundles");
mkdirSync(localRoot,{recursive:true});

const s3Configured=Boolean(
  process.env.S3_ENDPOINT &&
  process.env.S3_ACCESS_KEY_ID &&
  process.env.S3_SECRET_ACCESS_KEY &&
  process.env.S3_BUCKET
);
const s3=s3Configured?new S3Client({
  region:"auto",
  endpoint:process.env.S3_ENDPOINT,
  forcePathStyle:false,
  credentials:{
    accessKeyId:process.env.S3_ACCESS_KEY_ID!,
    secretAccessKey:process.env.S3_SECRET_ACCESS_KEY!,
  },
}):null;
const BUCKET=process.env.S3_BUCKET||"";
const signingSecret=process.env.CREDENTIAL_SECRET||process.env.BETTER_AUTH_SECRET||"";
const baseUrl=()=>(process.env.REDX_PUBLIC_URL||process.env.WEBSITE_URL||"http://127.0.0.1:4200").replace(/\/+$/,"");

function safeKey(value:string){
  return /^bundles\/[A-Za-z0-9._-]{1,220}$/.test(value);
}
function localPath(key:string){
  if(!safeKey(key))throw new Error("Invalid bundle key.");
  return resolve(localRoot,key.slice("bundles/".length));
}
function sign(method:string,key:string,expires:number){
  if(!signingSecret)throw new Error("BETTER_AUTH_SECRET/CREDENTIAL_SECRET is required.");
  return createHmac("sha256",signingSecret).update(method+"\n"+key+"\n"+expires).digest("hex");
}
function verify(method:string,key:string,expires:number,provided:string){
  if(!safeKey(key)||!Number.isFinite(expires)||expires<Date.now()||!provided)return false;
  const expected=sign(method,key,expires),a=Buffer.from(expected),b=Buffer.from(provided);
  return a.length===b.length&&timingSafeEqual(a,b);
}
function localUrl(method:string,key:string,ttlMs:number){
  const expires=Date.now()+ttlMs;
  const sig=sign(method,key,expires);
  return `${baseUrl()}/api/bundles/local?key=${encodeURIComponent(key)}&expires=${expires}&sig=${sig}`;
}
export function bundleStorageMode(){return s3Configured?"s3":"local";}
export async function createBundleUpload(filename:string,contentType:string){
  const key=`bundles/${Date.now()}-${filename.replace(/[^\w.-]+/g,"_").slice(0,180)}`;
  if(s3){
    const url=await getSignedUrl(s3,new PutObjectCommand({Bucket:BUCKET,Key:key,ContentType:contentType}),{expiresIn:900});
    return {url,key,mode:"s3" as const};
  }
  return {url:localUrl("PUT",key,15*60*1000),key,mode:"local" as const};
}
export async function createBundleDownload(key:string){
  if(s3){
    const url=await getSignedUrl(s3,new GetObjectCommand({Bucket:BUCKET,Key:key}),{expiresIn:3600});
    return url;
  }
  return localUrl("GET",key,60*60*1000);
}
export function registerLocalBundleRoutes(app:Hono){
  app.put("/api/bundles/local",async c=>{
    if(s3Configured)return c.json({error:"not_found"},404);
    const key=c.req.query("key")||"",expires=Number(c.req.query("expires")),sig=c.req.query("sig")||"";
    if(!verify("PUT",key,expires,sig))return c.json({error:"invalid_or_expired_signature"},403);
    const length=Number(c.req.header("content-length")||0);
    if(length>1024*1024*1024)return c.json({error:"bundle_too_large"},413);
    const target=localPath(key);
    await writeFile(target,Buffer.from(await c.req.raw.arrayBuffer()));
    return c.json({ok:true,key},200);
  });
  app.get("/api/bundles/local",async c=>{
    if(s3Configured)return c.json({error:"not_found"},404);
    const key=c.req.query("key")||"",expires=Number(c.req.query("expires")),sig=c.req.query("sig")||"";
    if(!verify("GET",key,expires,sig))return c.json({error:"invalid_or_expired_signature"},403);
    const target=localPath(key);
    if(!existsSync(target))return c.json({error:"not_found"},404);
    return new Response(await readFile(target),{headers:{"content-type":"application/octet-stream","cache-control":"private, no-store"}});
  });
}
