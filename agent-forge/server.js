import http from 'node:http';
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const __dirname=path.dirname(fileURLToPath(import.meta.url));
const PORT=3000;
const MODEL=process.env.OPENAI_MODEL||'gpt-5.6';
const API_KEY=process.env.OPENAI_API_KEY||'';
const GITHUB_TOKEN=process.env.GITHUB_TOKEN||'';

const roles=[
  ['lead','Lead Orchestrator','Architect, decompose work, coordinate dependencies, integrate and verify.'],
  ['programmer','Universal Programmer','Implement across required languages; research unfamiliar stacks and verify with tests.'],
  ['sound','Sound Designer / Audio Engineer','DSP, recording, MIDI, mixing, mastering, audio engines and audio QA.'],
  ['ui','UI/UX Designer','Professional native/web/mobile UX, accessibility, motion, responsive interaction and Red-XAI styling.'],
  ['qa','QA Tester','Normal flows, regression, compatibility, integration, reproducible bug reports.'],
  ['stress','Stress / Security Tester','Concurrency, malformed input, resource exhaustion, crash recovery, auth boundaries and endurance.'],
  ['builder','Builder / Release Engineer','Reproducible builds, CI, dependencies, packaging, signing pipeline and release candidates.'],
  ['exporter','Exporter / Distribution Engineer','Final artifacts, manifests, hashes, packaging, distribution and install verification.']
].map(([id,name,mission])=>({id,name,mission}));

const skillRules=`
You are part of Red-XAI Agent Forge.
Follow this lifecycle: DISCOVER -> PLAN -> BUILD -> REVIEW -> TEST -> INTEGRATE -> VERIFY -> REPORT.
Use evidence before calling work complete. Never claim perfection without tests.
Use relevant non-roleplay integrations when they are actually available; never access unrelated private data merely because a connector exists.
Do not perform destructive actions without required approval.
Never expose secrets. Prefer established cryptography for sensitive data.
Return compact structured JSON with keys: summary, tasks, risks, tests, handoff.
`;

async function callAgent(role,objective,context=''){
  if(!API_KEY) throw new Error('OPENAI_API_KEY is not configured');
  const prompt=`${skillRules}\nROLE: ${role.name}\nMISSION: ${role.mission}\nOBJECTIVE: ${objective}\nCONTEXT: ${context}`;
  const r=await fetch('https://api.openai.com/v1/responses',{method:'POST',headers:{'Authorization':`Bearer ${API_KEY}`,'Content-Type':'application/json'},body:JSON.stringify({model:MODEL,input:prompt})});
  if(!r.ok){const body=await r.text();throw new Error(`OpenAI ${r.status}: ${body.slice(0,500)}`)}
  const data=await r.json();
  return data.output_text||data.output?.map(x=>x.content?.map(c=>c.text||'').join('')).join('')||'';
}

async function runTeam(objective){
  const lead=roles[0];
  const plan=await callAgent(lead,objective,'Create the architecture, task graph, acceptance gates and specialist assignments.');
  const specialistRoles=roles.slice(1);
  const results=await Promise.all(specialistRoles.map(r=>callAgent(r,objective,`Lead plan:\n${plan}`)));
  const joined=specialistRoles.map((r,i)=>`## ${r.name}\n${results[i]}`).join('\n\n');
  const final=await callAgent(lead,objective,`Initial lead plan:\n${plan}\n\nSpecialist outputs:\n${joined}\n\nIntegrate, identify conflicts, require evidence, and produce the final verified execution plan.`);
  return {objective,model:MODEL,roles,plan,specialists:specialistRoles.map((r,i)=>({role:r,result:results[i]})),final,createdAt:new Date().toISOString()};
}

function json(res,status,data){res.writeHead(status,{'Content-Type':'application/json','Cache-Control':'no-store'});res.end(JSON.stringify(data));}
const secretStatus=()=>({openai:{name:'OPENAI_API_KEY',configured:Boolean(API_KEY)},github:{name:'GITHUB_TOKEN',configured:Boolean(GITHUB_TOKEN)},note:'Values are never returned by this API. Configure them as sealed Railway variables or synchronized repository secrets.'});

const server=http.createServer(async(req,res)=>{
  try{
    if(req.url==='/health') return json(res,200,{ok:true,service:'Red-XAI Agent Forge',model:MODEL,keyConfigured:Boolean(API_KEY),githubConfigured:Boolean(GITHUB_TOKEN),appDir:__dirname,uiExists:fs.existsSync(path.join(__dirname,'public','index.html'))});
    if(req.url==='/api/secrets/status'&&req.method==='GET') return json(res,200,secretStatus());
    if(req.url==='/'&&req.method==='GET'){const ui=path.join(__dirname,'public','index.html');if(fs.existsSync(ui)){res.writeHead(200,{'Content-Type':'text/html; charset=utf-8'});return fs.createReadStream(ui).pipe(res);}}
    if(req.url==='/api/team'&&req.method==='GET') return json(res,200,{roles,model:MODEL,keyConfigured:Boolean(API_KEY),githubConfigured:Boolean(GITHUB_TOKEN)});
    if(req.url==='/api/run'&&req.method==='POST'){
      let body=''; for await(const c of req) body+=c;
      const {objective}=JSON.parse(body||'{}');
      if(!objective||typeof objective!=='string') return json(res,400,{error:'objective is required'});
      const result=await runTeam(objective.slice(0,12000));
      return json(res,200,result);
    }
    const target=req.url==='/'?'index.html':req.url?.replace(/^\//,'');
    const safe=path.normalize(target||'').replace(/^\.\.(\/|\\|$)/,'');
    const file=path.join(__dirname,'public',safe);
    if(file.startsWith(path.join(__dirname,'public'))&&fs.existsSync(file)&&fs.statSync(file).isFile()){
      const ext=path.extname(file); const types={'.html':'text/html; charset=utf-8','.css':'text/css','.js':'text/javascript'};
      res.writeHead(200,{'Content-Type':types[ext]||'application/octet-stream'}); return fs.createReadStream(file).pipe(res);
    }
    json(res,404,{error:'not found'});
  }catch(e){console.error(e);json(res,500,{error:e?.message||'internal error'});}
});
server.listen(PORT,'0.0.0.0',()=>console.log(`Red-XAI Agent Forge listening on ${PORT}`));
