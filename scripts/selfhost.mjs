import { existsSync, mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { randomBytes } from "node:crypto";
import { resolve } from "node:path";
import { spawnSync } from "node:child_process";

const root=resolve(import.meta.dirname,"..");
const envPath=resolve(root,".env");
const dataDir=resolve(root,"data");
mkdirSync(dataDir,{recursive:true});

function parseEnv(text){
  const out=new Map();
  for(const line of text.split(/\r?\n/)){
    const trimmed=line.trim();
    if(!trimmed||trimmed.startsWith("#")||!trimmed.includes("="))continue;
    const i=trimmed.indexOf("=");
    out.set(trimmed.slice(0,i),trimmed.slice(i+1));
  }
  return out;
}
function serialize(map){
  return [...map.entries()].map(([k,v])=>k+"="+v).join("\n")+"\n";
}
function ensureSecret(map,key,bytes=32){
  if(!map.get(key))map.set(key,randomBytes(bytes).toString("hex"));
}
const base=existsSync(envPath)?readFileSync(envPath,"utf8"):readFileSync(resolve(root,".env.template"),"utf8");
const env=parseEnv(base);
env.set("NODE_ENV",env.get("NODE_ENV")||"production");
const publicUrl=(env.get("REDX_PUBLIC_URL")||"").trim().replace(/\/+$/,"");
if(publicUrl&&!/^https:\/\//i.test(publicUrl)){
  throw new Error("REDX_PUBLIC_URL must be an HTTPS URL, for example https://host.example.com");
}
const controlUrl=publicUrl||env.get("WEBSITE_URL")||"http://127.0.0.1:4200";
env.set("WEBSITE_URL",controlUrl);
env.set("VITE_WEBSITE_URL",publicUrl||env.get("VITE_WEBSITE_URL")||controlUrl);
env.set("REDX_BIND_HOST",env.get("REDX_BIND_HOST")||(publicUrl?"0.0.0.0":"127.0.0.1"));
env.set("OWNER_EMAIL",env.get("OWNER_EMAIL")||"grimvirusoffical@gmail.com");
env.set("VITE_OWNER_EMAIL",env.get("VITE_OWNER_EMAIL")||env.get("OWNER_EMAIL")||"grimvirusoffical@gmail.com");
env.set("REDX_AUTO_LOCAL_NODE",env.get("REDX_AUTO_LOCAL_NODE")||"true");
env.set("REDX_DATA_DIR",env.get("REDX_DATA_DIR")||"./data");
ensureSecret(env,"BETTER_AUTH_SECRET");
ensureSecret(env,"CREDENTIAL_SECRET");
writeFileSync(envPath,serialize(env),{mode:0o600});
console.log("RedXAIHost environment ready:",envPath);
if(!publicUrl){
  console.log("RedXAIHost is in local-only control-plane mode.");
  console.log("Set REDX_PUBLIC_URL=https://your-hostname before adding remote PCs/VPS nodes or GitHub webhooks.");
}else{
  console.log("Public control plane:",publicUrl);
}

if(process.argv.includes("--prepare")){
  const steps=[
    ["bun",["install","--frozen-lockfile"]],
    ["bun",["run","db:push"]],
    ["bun",["run","build"]],
  ];
  for(const [cmd,args] of steps){
    console.log("\n>",cmd,...args);
    const result=spawnSync(cmd,args,{cwd:root,stdio:"inherit",shell:process.platform==="win32"});
    if(result.status!==0)process.exit(result.status??1);
  }
  console.log("\nRedXAIHost prepared successfully.");
}
if(process.argv.includes("--run")){
  const result=spawnSync("bun",["--env-file=.env","packages/web/src/__server.ts"],{
    cwd:root,stdio:"inherit",shell:process.platform==="win32"
  });
  process.exit(result.status??0);
}
