// Public visual preview only. This file never imports the production gateway or opens an upstream connection.
import http from 'node:http';
import path from 'node:path';
import {readFile} from 'node:fs/promises';
import {fileURLToPath,pathToFileURL} from 'node:url';

const HERE=path.dirname(fileURLToPath(import.meta.url));
const ASSETS=new Set(['app.js','styles.css','catalog.json','public-secrets-docs.json','cli-installation.md','site/dom.js','site/api.js','site/catalog.js','site/auth.js','site/session.js','site/editor.js','site/pages.js','site/legal.js']);
const MIME={'.js':'text/javascript; charset=utf-8','.css':'text/css; charset=utf-8','.json':'application/json; charset=utf-8','.md':'text/plain; charset=utf-8'};
const capabilities=Object.freeze({cms:false,media:false,resendVerification:false,deleteAccount:false,subscriptions:false});
const CSP="default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data: blob:; connect-src 'self'; frame-src 'none'; object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'";
function headers(res){res.setHeader('Content-Security-Policy',CSP);res.setHeader('X-Frame-Options','DENY');res.setHeader('X-Content-Type-Options','nosniff');res.setHeader('Cache-Control','no-store');res.setHeader('Permissions-Policy','camera=(), microphone=(), geolocation=()');res.setHeader('Referrer-Policy','no-referrer');}
function json(res,status,value){res.statusCode=status;res.setHeader('Content-Type','application/json; charset=utf-8');res.end(JSON.stringify(value));}
export function createPreviewHandler({directory=HERE}={}){
  return async(req,res)=>{
    headers(res);
    try{
      // Ignore cookies and authorization headers. No preview request can authenticate an account.
      const url=new URL(req.url,'http://127.0.0.1'),p=url.pathname;
      if(req.method==='GET'&&p==='/api/site/session')return json(res,200,{signedIn:false,owner:false,capabilities,preview:true});
      if(req.method==='GET'&&p==='/api/site/config')return json(res,200,{preview:true,capabilities,downloads:{},socials:{},categories:[]});
      if(req.method==='GET'&&p==='/api/site/posts')return json(res,200,{posts:[],preview:true});
      if(p.startsWith('/api/')||p==='/v1'||p.startsWith('/v1/')||p==='/ui'||p.startsWith('/ui/')||p.startsWith('/redxai-connect'))return json(res,503,{error:'preview_disabled',message:'Guest preview only. Production authentication, Host, DB and account changes are disabled.'});
      if(!['GET','HEAD'].includes(req.method)){req.resume();return json(res,405,{error:'preview_read_only',message:'This preview is read-only.'});}
      if(['/signup','/activate','/tos'].includes(p)){
        res.setHeader('Content-Type','text/html; charset=utf-8');return res.end('<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Registration disabled in guest preview</title><link rel="stylesheet" href="/styles.css"></head><body><main class="wrap section"><h1>Guest preview</h1><p>Registration, activation and existing registration terms remain on the unchanged primary account service. This isolated preview does not contact that service.</p><a class="button" href="/">Return to Hub preview</a></main></body></html>');
      }
      if(ASSETS.has(p.slice(1))){const bytes=await readFile(path.join(directory,p.slice(1)));res.setHeader('Content-Type',MIME[path.extname(p)]);res.setHeader('Content-Length',bytes.length);return res.end(req.method==='HEAD'?undefined:bytes);}
      if(path.extname(p))return json(res,404,{error:'not_found'});
      const source=await readFile(path.join(directory,'index.html'),'utf8');
      const html=source.replace('<body>','<body><aside class="preview-banner" role="note">LOCAL GUEST PREVIEW · Signed out · Account, Host and DB connections disabled</aside>');
      res.setHeader('Content-Type','text/html; charset=utf-8');res.end(req.method==='HEAD'?undefined:html);
    }catch{json(res,503,{error:'preview_asset_unavailable',message:'The preview asset could not be read.'});}
  };
}
export const createPreviewServer=options=>http.createServer(createPreviewHandler(options));
export async function startPreview({port=14310}={}){
  if(!Number.isInteger(port)||port<1024||port>65535)throw new Error('Choose a loopback preview port from 1024 to 65535.');
  const server=createPreviewServer();await new Promise((resolve,reject)=>{server.once('error',reject);server.listen(port,'127.0.0.1',resolve);});return server;
}
if(process.argv[1]&&pathToFileURL(path.resolve(process.argv[1])).href===import.meta.url){
  const argument=process.argv[2]||'14310';
  startPreview({port:Number(argument)}).then(()=>process.stdout.write('Guest preview: http://127.0.0.1:'+argument+'\nProduction account, Host and DB connections are disabled.\n')).catch(error=>{process.stderr.write(error.message+'\n');process.exitCode=1;});
}
