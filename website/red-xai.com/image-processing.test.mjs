import test from 'node:test';
import assert from 'node:assert/strict';
import sharp from 'sharp';
import {imageKind,processImage} from './site-backend.mjs';

test('supported raster uploads are really decoded and re-encoded as metadata-free WebP',async()=>{
  for(const [format,type]of [['png','image/png'],['jpeg','image/jpeg'],['webp','image/webp'],['gif','image/gif']]){
    const input=await sharp({create:{width:4,height:2,channels:3,background:'#c91031'}}).withMetadata({orientation:6}).toFormat(format).toBuffer();
    assert.equal(imageKind(input),type);
    const output=await processImage(input,type),meta=await sharp(output.bytes).metadata();
    assert.equal(meta.format,'webp');assert.equal(meta.exif,undefined);assert.equal(meta.icc,undefined);assert.equal(meta.orientation,undefined);
    assert.equal(output.width*output.height,8);assert.equal(meta.width,output.width);assert.equal(meta.height,output.height);
  }
});
test('rejects mismatched declarations, corrupt raster data and unsupported vector uploads',async()=>{
  const png=await sharp({create:{width:1,height:1,channels:3,background:'#ffffff'}}).png().toBuffer();
  for(const [bytes,type]of [[png,'image/jpeg'],[Buffer.from('not an image'),'image/png'],[Buffer.from('<svg xmlns="http://www.w3.org/2000/svg"/>'),'image/svg+xml'],[png.subarray(0,12),'image/png']])await assert.rejects(processImage(bytes,type),e=>e.status===415);
});
test('limits decoded image area before accepting a high-compression raster',async()=>{
  const input=await sharp({create:{width:4097,height:4096,channels:3,background:'#ffffff'}}).png().toBuffer();
  await assert.rejects(processImage(input,'image/png'),e=>e.status===415);
});
