import assert from 'node:assert/strict';
import {fileURLToPath} from 'node:url';
import {mock} from 'bun:test';
import * as React from 'react';
import {renderToStaticMarkup} from 'react-dom/server';

// Evaluate the real component and its event handlers with inert hook adapters.
// No browser, transport, authority, process or worker action is used.
const initialSearch=process.argv[2];
assert.equal(process.argv.length,3);
assert.ok(initialSearch===''||initialSearch==='?embed=site');
const expectedDestination=initialSearch==='?embed=site'?'/host/projects?embed=site':'/host/projects';
const states:unknown[]=[];let cursor=0,changed=false;
mock.module('react',()=>({...React,useState:(initial:unknown)=>{
  const index=cursor++;
  if(index===states.length)states.push(initial);
  return [states[index],(next:unknown)=>{
    states[index]=typeof next==='function'?(next as (value:unknown)=>unknown)(states[index]):next;
    changed=true;
  }];
}}));
const Button=()=>null,Stub=()=>null,PageHeader=()=>null;
mock.module('@/components/ui/button',()=>({Button}));
mock.module('@/components/ui/card',()=>({Card:Stub,CardBody:Stub,CardHeader:Stub,CardTitle:Stub}));
mock.module('@/components/ui/field',()=>({Input:Stub,Label:Stub,Select:Stub,Textarea:Stub}));
mock.module('wouter',()=>({Link:Stub,useParams:()=>({id:'synthetic-project'})}));
const modulePath=(relative:string)=>fileURLToPath(new URL(relative,import.meta.url));
mock.module(modulePath('../src/web/components/shell.tsx'),()=>({PageHeader}));
mock.module(modulePath('../src/web/lib/panel.ts'),()=>({PANEL_MOUNT:'/host',panelBase:()=>'/host'}));
mock.module(modulePath('../src/web/queries/nodes.ts'),()=>({useNodes:()=>({data:[]})}));

const project={id:'synthetic-project',name:'Synthetic project',slug:'synthetic-project',status:'running',
  updatedAt:1,runtime:'static',nodeId:null,port:null,installCommand:null,buildCommand:null,
  startCommand:null,dockerfile:null,envVars:null,sourceKind:'bundle',autoDeploy:false};
let calls=0,mutate:()=>Promise<unknown>=async()=>({ok:true});
const remove={isPending:false,mutateAsync:async(input:unknown)=>{
  assert.deepEqual(input,{id:'synthetic-project'});calls++;return mutate();
}};
mock.module(modulePath('../src/web/queries/projects.ts'),()=>({
  useProject:()=>({data:{project},isLoading:false}),useRemoveProject:()=>remove,
  useUpdateProject:()=>({isPending:false}),useDeployProject:()=>({isPending:false}),
  useAiPlan:()=>({isPending:false}),
}));
const destinations:string[]=[],prompts:string[]=[];let consent=true;
Object.assign(globalThis,{
  confirm:(text:string)=>{prompts.push(text);return consent;},
  window:{location:{search:initialSearch,assign:(target:string)=>destinations.push(target)}},
  fetch:()=>{throw new Error('Network is forbidden in the UI fixture');},
});
const {default:ProjectDetail}=await import('../src/web/pages/project');
const {StatusPill,toneOf}=await import('../src/web/components/status');
// SPA links may drop their query; the real helper must retain initial site mode.
window.location.search='';
type Element={type:unknown;props:{children?:unknown;actions?:unknown;onClick?:()=>void;disabled?:boolean;role?:string}};
function walk(value:unknown):Element[] {
  if(Array.isArray(value))return value.flatMap(walk);
  if(!value||typeof value!=='object'||!('props' in value))return [];
  const element=value as Element;
  return [element,...walk(element.props.children),...walk(element.props.actions)];
}
function text(value:unknown):string {
  if(typeof value==='string'||typeof value==='number')return String(value);
  if(Array.isArray(value))return value.map(text).join('');
  if(value&&typeof value==='object'&&'props' in value)return text((value as Element).props.children);
  return '';
}
function render():unknown {
  let result:unknown;
  for(let pass=0;pass<3;pass++) {
    cursor=0;changed=false;result=ProjectDetail();if(!changed)return result;
  }
  throw new Error('Unexpected render loop');
}
const button=(tree:unknown,label:string)=>{
  const found=walk(tree).find(element=>element.type===Button&&text(element.props.children).trim()===label);
  assert.ok(found,'expected button: '+label);return found;
};
const settle=async()=>{await Promise.resolve();await Promise.resolve();};

assert.equal(toneOf('removing'),'warn');
const pill=renderToStaticMarkup(React.createElement(StatusPill,{status:'removing'}));
assert.match(pill,/removal pending/);assert.match(pill,/animate-pulse/);
let tree=render();
for(const label of ['Deploy','Restart','Stop','Save','Write the run plan'])
  assert.equal(button(tree,label).props.disabled,false);

// User cancellation neither mutates nor navigates.
consent=false;button(tree,'Remove project').props.onClick!();await settle();
assert.equal(calls,0);assert.deepEqual(destinations,[]);

// Navigation must wait for acceptance, including a slow request.
consent=true;let accept!:(value:unknown)=>void;
mutate=()=>new Promise(resolve=>{accept=resolve;});
button(tree,'Remove project').props.onClick!();await settle();
assert.equal(calls,1);assert.deepEqual(destinations,[]);
assert.match(prompts.at(-1)!,/stays visible until its nodes confirm teardown/);
accept({ok:true});await settle();assert.deepEqual(destinations,[expectedDestination]);

// A rejected request stays on the project and displays its error.
destinations.length=0;mutate=async()=>{throw new Error('Synthetic removal refused');};
button(render(),'Remove project').props.onClick!();await settle();tree=render();
assert.equal(calls,2);assert.deepEqual(destinations,[]);
assert.match(text(tree),/Synthetic removal refused/);

// Unknown failures also stay put with an actionable fallback notice.
mutate=async()=>{throw null;};
button(tree,'Remove project').props.onClick!();await settle();
assert.deepEqual(destinations,[]);assert.match(text(render()),/Removal could not be confirmed/);
assert.match(text(render()),/Refresh the project before retrying/);

// Pending projects explain offline work and refuse conflicting actions.
project.status='removing';project.updatedAt++;tree=render();
for(const label of ['Deploy','Restart','Stop','Save','Write the run plan'])
  assert.equal(button(tree,label).props.disabled,true,label);
assert.ok(walk(tree).some(element=>element.props.role==='status'));
assert.match(text(tree),/Reconnect offline nodes/);
remove.isPending=true;assert.equal(button(render(),'Retry removal').props.disabled,true);

// An available retry uses the unchanged request shape and waits for acceptance.
remove.isPending=false;mutate=async()=>({ok:true});
button(render(),'Retry removal').props.onClick!();await settle();
assert.match(prompts.at(-1)!,/Offline nodes must reconnect/);
assert.deepEqual(destinations,[expectedDestination]);assert.equal(calls,4);
