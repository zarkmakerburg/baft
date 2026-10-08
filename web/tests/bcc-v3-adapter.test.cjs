// Run: node --test web/tests/bcc-v3-adapter.test.cjs
// Zero dependencies; execute the actual browser adapter in a sandboxed VM.
'use strict';
const test=require('node:test'),assert=require('node:assert/strict'),vm=require('node:vm'),fs=require('node:fs'),path=require('node:path');
const source=fs.readFileSync(path.join(__dirname,'..','bcc-live-adapter-v3.js'),'utf8');
function harness(tunnels,options={}){
 const elements={bccLiveStatus:{textContent:''},bccLiveLoad:{disabled:false,addEventListener(_,fn){this.click=fn}},bccCoordinates:{value:JSON.stringify(options.coords||{ir:{lat:35.6892,lon:51.389},de:{lat:50.1109,lon:8.6821}})}};
 const emitted=[],calls=[];
 const sandbox={location:{protocol:options.protocol||'https:',hostname:options.hostname||'bcc.example.test'},document:{documentElement:{lang:'en'},getElementById:id=>elements[id]},window:{dispatchEvent:e=>emitted.push(e.detail)},CustomEvent:class{constructor(_,v){this.detail=v.detail}},prompt:()=>options.token===undefined?'test-token':options.token,fetch:async(url,opts)=>{calls.push({url,opts});return {ok:options.httpStatus===undefined,status:options.httpStatus||200,json:async()=>tunnels}},console};
 vm.runInNewContext(source,sandbox);
 return {elements,emitted,calls,run:()=>elements.bccLiveLoad.click()};
}
test('GET only, bearer token stays out of URL and topology is UNKNOWN',async()=>{
 const h=harness([{id:'t1',ir_node:'ir',ex_node:'de',phase:'active'}]);await h.run();
 assert.equal(h.calls.length,1);assert.equal(h.calls[0].url,'/api/tunnels');assert.equal(h.calls[0].opts.method,'GET');
 assert.equal(h.calls[0].opts.headers.Authorization,'Bearer test-token');
 assert.equal(h.emitted.length,1);assert.equal(h.emitted[0].links[0].status,'unknown');
 assert.equal(h.emitted[0].links[0].rttMs,null);
});
test('invalid or absent coordinates exclude tunnel without guessing',async()=>{
 const h=harness([{id:'t1',ir_node:'ir',ex_node:'de',phase:'active'}],{coords:{ir:{lat:35,lon:51}}});await h.run();
 assert.equal(h.emitted[0].links.length,0);
});
test('duplicate tunnel IDs do not duplicate rendered connections',async()=>{
 const h=harness([{id:'t1',ir_node:'ir',ex_node:'de',phase:'active'},{id:'t1',ir_node:'ir',ex_node:'de',phase:'active'}]);await h.run();
 assert.equal(h.emitted[0].links.length,1);
});
test('401 fails closed and emits no topology',async()=>{
 const h=harness([],{httpStatus:401});await h.run();
 assert.equal(h.emitted.length,0);assert.match(h.elements.bccLiveStatus.textContent,/Read-only load failed/);
 assert.equal(h.elements.bccLiveLoad.disabled,false);
});
test('insecure origin refuses token and network',async()=>{const h=harness([],{protocol:'file:'});await h.run();assert.equal(h.calls.length,0);assert.match(h.elements.bccLiveStatus.textContent,/Read-only load failed/)});
test('missing token does not call BCC',async()=>{
 const h=harness([],{token:''});await h.run();assert.equal(h.calls.length,0);
});
test('invalid JSON is rejected without any network request',async()=>{
 const h=harness([]);h.elements.bccCoordinates.value='{bad';await h.run();assert.equal(h.calls.length,0);
});

test('excessive coordinate input is rejected before authentication',async()=>{const h=harness([]);h.elements.bccCoordinates.value=' '.repeat(16385);await h.run();assert.equal(h.calls.length,0);assert.match(h.elements.bccLiveStatus.textContent,/Read-only load failed/)});
test('oversized tunnel response is rejected without dispatch',async()=>{const ts=Array.from({length:301},(_,i)=>({id:'t'+i,ir_node:'ir',ex_node:'de'}));const h=harness(ts);await h.run();assert.equal(h.emitted.length,0);assert.match(h.elements.bccLiveStatus.textContent,/Read-only load failed/)});

test('non-array API response fails closed',async()=>{const h=harness({tunnels:[]});await h.run();assert.equal(h.emitted.length,0);assert.match(h.elements.bccLiveStatus.textContent,/Read-only load failed/)});
test('invalid tunnel entries are skipped safely',async()=>{const h=harness([null,{id:'valid',ir_node:'ir',ex_node:'de'},{id:'bad',ir_node:'ir',ex_node:'ir'}]);await h.run();assert.equal(h.emitted[0].links.length,1)});
