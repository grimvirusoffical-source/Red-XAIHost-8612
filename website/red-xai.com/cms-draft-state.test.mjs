import test from 'node:test';
import assert from 'node:assert/strict';
import {createDraftStore,createTraversalHandler} from './site/session.js';

const owner=(id='owner-A',expiresMs=2000)=>({signedIn:true,owner:true,account:{id},expiresMs});
const snapshot=()=>({post:null,title:'  Incomplete draft  ',category:'maintenance',body:{type:'doc',children:[]},starts:'',ends:'2026-11-04T18:30'});

test('draft snapshots are detached, route-bound, and never silently overwrite another editor',()=>{
  const store=createDraftStore({now:()=>1000});store.setSession(owner());
  const a=store.lease('announcements'),b=store.lease('updates'),token={},other={},input=snapshot();
  assert.equal(a.remember(token,input),true);input.title='mutated';
  const copy=a.peek();assert.equal(copy.snapshot.title,'  Incomplete draft  ');copy.snapshot.body.children.push({type:'codeBlock',text:'mutated'});
  assert.equal(a.peek().snapshot.body.children.length,0);assert.equal(b.peek(),null);
  assert.equal(a.remember(other,{...snapshot(),title:'replacement'}),false);assert.equal(a.peek().token,token);
  assert.equal(a.discard(other),false);assert.equal(a.discard(token),true);assert.equal(store.hasAny(),false);
  a.remember(other,snapshot());assert.equal(a.peek().token,other);
});

test('same valid owner refresh preserves drafts; expiry cannot be bypassed by renewing the same account',()=>{
  let clock=1000;const store=createDraftStore({now:()=>clock});store.setSession(owner());
  const old=store.lease('updates'),token={};old.remember(token,snapshot());
  store.setSession(owner('owner-A',2500));assert.ok(old.peek());
  clock=2500;assert.equal(old.peek(),null);assert.equal(store.hasAny(),false);
  store.setSession(owner('owner-A',3500));const fresh=store.lease('updates'),freshToken={};fresh.remember(freshToken,snapshot());
  assert.equal(old.remember(token,snapshot()),false);old.settle(freshToken,true);assert.equal(fresh.peek().token,freshToken);
});

test('logout/error revocation, sign-out, account changes, and loss of owner invalidate old callbacks',()=>{
  for(const transition of [null,{signedIn:false},{...owner(),owner:false},owner('owner-B')]){
    const store=createDraftStore({now:()=>1000});store.setSession(owner());const old=store.lease('updates'),token={};old.remember(token,snapshot(),{pending:true});
    if(transition===null)store.clear();else store.setSession(transition);
    assert.equal(old.peek(),null);assert.equal(old.valid(),false);
    store.setSession(owner());const fresh=store.lease('updates'),freshToken={};fresh.remember(freshToken,{...snapshot(),title:'new generation'});
    old.settle(token,true);old.settle(token,false);old.remember(token,snapshot());old.discard();
    assert.equal(fresh.peek().token,freshToken);assert.equal(fresh.peek().snapshot.title,'new generation');
  }
});

test('same-owner renewal after the prior expiry reports a boundary so the router can rebuild invalid leases',()=>{
  let clock=1000;const store=createDraftStore({now:()=>clock});store.setSession(owner());
  const old=store.lease('updates');old.remember({},snapshot());
  assert.equal(store.setSession(owner('owner-A',2000)),false);
  clock=2000;assert.equal(store.setSession(owner('owner-A',3000)),true);
  assert.equal(old.valid(),false);assert.equal(old.peek(),null);
  assert.equal(store.lease('updates').valid(),true);assert.equal(store.hasAny(),false);
  assert.equal(store.setSession(owner('owner-A',3500)),false);
});

test('pending snapshots cannot restore; settlement is token-bound and uncertain failure retains content',()=>{
  const store=createDraftStore({now:()=>1000});store.setSession(owner());const lease=store.lease('updates'),token={};
  lease.remember(token,snapshot(),{pending:true});assert.equal(lease.take(token),null);
  lease.settle({},true);assert.equal(lease.peek().pending,true);
  lease.settle(token,false);assert.equal(lease.peek().pending,false);assert.equal(lease.peek().failed,true);
  const restored=lease.take(token);assert.deepEqual(restored,snapshot());assert.equal(lease.peek(),null);
  const newer={};lease.remember(newer,{...snapshot(),title:'newer'});lease.settle(token,true);assert.equal(lease.peek().snapshot.title,'newer');
  lease.settle(newer,true);assert.equal(store.hasAny(),false);
});

test('traversal disposes the departing page before rendering the selected destination and never writes history',async()=>{
  const previous=Object.getOwnPropertyDescriptor(globalThis,'history');
  const forbidden=()=>{throw Error('Traversal must not write or move history');};
  Object.defineProperty(globalThis,'history',{configurable:true,value:{pushState:forbidden,replaceState:forbidden,go:forbidden,back:forbidden,forward:forbidden}});
  try{
    for(const privateDestination of [false,true]){
      const calls=[];let retained=false;
      const handler=createTraversalHandler({dispose:()=>{retained=true;calls.push('retain/dispose');},shouldRefresh:()=>privateDestination,refresh:async()=>{assert.ok(retained);calls.push('refresh');},renderHeader:()=>calls.push('header'),renderRoute:async()=>{assert.ok(retained);calls.push('selected destination');}});
      await handler({preventDefault:forbidden});
      assert.deepEqual(calls,['retain/dispose',...(privateDestination?['refresh']:[]),'header','selected destination']);
    }
  }finally{if(previous)Object.defineProperty(globalThis,'history',previous);else delete globalThis.history;}
});
