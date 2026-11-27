const test = require('node:test');
const assert = require('node:assert/strict');
const {imageProfile, chooseImageConnection, mediaURL} = require('../../internal/coreui/assets/js/chunnel-ai-images.source.js');
const {chooseConnection} = require('../../internal/coreui/assets/js/chunnel-ai-editor.source.js');

test('writing and image choices stay separate and recover when a provider or model is removed', () => {
  const data = {default_provider:'text', default_image_provider:'nano', providers:[
    {id:'text',models:['writer'],default_model:'writer'},
    {id:'gpt',protocol:'openai_images',models:['gpt-image-1','gpt-image-1.5'],default_model:'gpt-image-1'},
    {id:'nano',protocol:'gemini_images',models:['nano-image'],default_model:'nano-image'},
  ]};
  assert.deepEqual(chooseImageConnection(data), {provider_id:'nano',model:'nano-image'});
  assert.deepEqual(chooseImageConnection(data, {provider_id:'gpt',models:{gpt:'gpt-image-1.5'}}), {provider_id:'gpt',model:'gpt-image-1.5'});
  assert.deepEqual(chooseImageConnection(data, {provider_id:'gpt',models:{gpt:'removed'}}), {provider_id:'gpt',model:'gpt-image-1'});
  assert.deepEqual(chooseImageConnection(data, {provider_id:'text'}), {provider_id:'nano',model:'nano-image'});
  assert.deepEqual(chooseImageConnection({providers:[data.providers[0]]}), {provider_id:'',model:''});
  assert.deepEqual(chooseConnection(data, {provider_id:'nano'}), {provider_id:'text',model:'writer'});
});

test('image options reflect each protocol and older Nano resolution support', () => {
  assert.deepEqual(imageProfile('openai_images').ratios, ['1:1','3:2','2:3']);
  assert.equal(imageProfile('openai_images').quality,true);
  assert.deepEqual(imageProfile('gemini_images','gemini-2.5-flash-image').resolutions, ['']);
  assert.deepEqual(imageProfile('gemini_interactions','gemini-3.1-flash-image').resolutions, ['','1K','2K','4K']);
  assert.equal(imageProfile('seedream').ratios.includes('16:9'),true);
  assert.deepEqual(imageProfile('seedream').resolutions,['','2K','4K']);
});

test('image preview and insertion accept only active local media', () => {
  const item = {id:7,kind:'image',status:'active',public_url:'/media/7/image.png',width:1200,height:800};
  assert.equal(mediaURL(item),item.public_url);
  for (const public_url of ['https://foreign.test/x','//foreign.test/x','/media/7/evil\\path','/media/7/x?redirect=1','javascript:alert(1)','/media/7/x\n']) assert.equal(mediaURL({...item,public_url}), '');
  assert.equal(mediaURL({...item,status:'trash'}),'');
  assert.equal(mediaURL({...item,kind:'document'}),'');
  assert.equal(mediaURL({...item,width:0}),'');
  assert.equal(mediaURL(null),'');
});
