import test from 'node:test';
import assert from 'node:assert/strict';
import {readVortexRelease,VORTEX_RELEASE_SOURCE} from './vortex-release.mjs';
const valid=()=>({version:'fixture-1',publishedAt:'2026-01-01T00:00:00Z',releaseUrl:'https://github.com/grimvirusoffical-source/red-xai-vortex/releases/tag/fixture-1',assets:[{name:'vortex-fixture-windows.exe',size:123,url:'https://github.com/grimvirusoffical-source/red-xai-vortex/releases/download/fixture-1/vortex-fixture-windows.exe'}]});
test('Vortex releases use the existing fixed Host reader without transmitting account credentials',async()=>{
  const result=await readVortexRelease(async(url,options)=>{assert.equal(url,VORTEX_RELEASE_SOURCE);assert.deepEqual(options.headers,{accept:'application/json'});assert.equal(options.redirect,'manual');return Response.json(valid());});
  assert.equal(result.assets.length,1);assert.equal(result.version,'fixture-1');
});
test('Vortex release metadata rejects unexpected destinations and invalid assets',async()=>{
  for(const change of [v=>{v.releaseUrl='https://invalid.example/';},v=>{v.assets[0].url='https://github.com/other/repo/releases/download/x/file.exe';},v=>{v.assets[0].url+='?credential=fixture';},v=>{v.assets[0].size=-1;},v=>{v.assets[0].name='bad\nname';},v=>{v.assets={};}]){
    const value=valid();change(value);await assert.rejects(readVortexRelease(async()=>Response.json(value)),e=>e.status===503);
  }
});
test('Vortex release outages and redirects do not publish invented versions or downloads',async()=>{
  for(const reply of [async()=>{throw Error('fixture');},async()=>new Response(null,{status:302}),async()=>Response.json({},{status:503})])await assert.rejects(readVortexRelease(reply),e=>e.status===503);
});
