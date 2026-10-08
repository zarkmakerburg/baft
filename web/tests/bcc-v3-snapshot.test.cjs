// Run: node --test web/tests/bcc-v3-snapshot.test.cjs
'use strict';
const test=require('node:test'),assert=require('node:assert/strict'),vm=require('node:vm'),fs=require('node:fs'),path=require('node:path');
const src=fs.readFileSync(path.join(__dirname,'..','bcc-topology-snapshot-v3.js'),'utf8');
function harness(snapshot){
 const events=[],input={files:[{size:Buffer.byteLength(snapshot),text:async()=>snapshot}],value:'',addEventListener(_,fn){this.change=fn}},status={textContent:''};
 const sandbox={document:{getElementById:id=>({topologyFile:input,snapshotStatus:status})[id]},window:{dispatchEvent:e=>events.push(e.detail)},CustomEvent:class{constructor(_,v){this.detail=v.detail}},Date,JSON,Set,Error,Number,Array,Math};
 vm.runInNewContext(src,sandbox);return {input,status,events,run:()=>input.change()};
}
const base=(link={})=>JSON.stringify({schemaVersion:1,nodes:[{id:'ir',name:'Iran',lat:35.6892,lon:51.389},{id:'de',name:'Germany',lat:50.1109,lon:8.6821}],links:[{id:'t1',source:'ir',destination:'de',status:'healthy',rttMs:50,upBps:1000,downBps:2000,updatedAt:new Date().toISOString(),...link}]});
test('fresh valid snapshot retains tunnel-scoped telemetry',async()=>{const h=harness(base());await h.run();assert.equal(h.events.length,1);assert.equal(h.events[0].links[0].status,'healthy');assert.equal(h.events[0].links[0].rttMs,50)});
test('future timestamp never proves link health',async()=>{const h=harness(base({updatedAt:new Date(Date.now()+60000).toISOString()}));await h.run();assert.equal(h.events[0].links[0].status,'unknown');assert.equal(h.events[0].links[0].rttMs,null)});
test('stale telemetry becomes UNKNOWN',async()=>{const h=harness(base({updatedAt:new Date(Date.now()-240000).toISOString()}));await h.run();assert.equal(h.events[0].links[0].status,'unknown');assert.equal(h.events[0].links[0].upBps,null)});
test('unknown state cannot show metrics',async()=>{const h=harness(base({status:'unknown'}));await h.run();assert.equal(h.events[0].links[0].rttMs,null);assert.equal(h.events[0].links[0].downBps,null)});
test('timestamp without timezone cannot establish freshness',async()=>{const h=harness(base({updatedAt:'2026-10-08T12:00:00'}));await h.run();assert.equal(h.events[0].links[0].status,'unknown')});
test('missing referenced node rejects import',async()=>{const h=harness(base({destination:'not-found'}));await h.run();assert.equal(h.events.length,0);assert.match(h.status.textContent,/rejected/i)});
test('invalid coordinate rejects import',async()=>{const data=JSON.parse(base());data.nodes[0].lat=100;const h=harness(JSON.stringify(data));await h.run();assert.equal(h.events.length,0)});
