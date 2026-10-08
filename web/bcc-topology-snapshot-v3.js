/* Explicit local-file import only. No network calls, no persistence, no credentials. */
(function(){
'use strict';
const MAX_BYTES=512*1024, MAX_AGE_MS=180000;
const byId=id=>document.getElementById(id);
function finiteCoord(v,min,max){return typeof v==='number'&&Number.isFinite(v)&&v>=min&&v<=max}
function optionalNumber(v){return v==null||typeof v==='number'&&Number.isFinite(v)&&v>=0}
function parse(raw){
 const data=JSON.parse(raw);
 if(!data||data.schemaVersion!==1||!Array.isArray(data.nodes)||!Array.isArray(data.links))throw Error('Unsupported topology snapshot');
 if(data.nodes.length>100||data.links.length>300)throw Error('Too many nodes or connections');
 const ids=new Set(),nodes=[];
 for(const n of data.nodes){
 if(!n||typeof n.id!=='string'||!/^[\w-]{1,64}$/.test(n.id)||ids.has(n.id)||typeof n.name!=='string'||n.name.length>100||!finiteCoord(n.lat,-90,90)||!finiteCoord(n.lon,-180,180))throw Error('Invalid node or coordinates');
 ids.add(n.id);nodes.push({id:n.id,name:n.name,lat:n.lat,lon:n.lon});
 }
 const links=[],linkIds=new Set(),now=Date.now();
 for(const l of data.links){
 if(!l||typeof l.id!=='string'||!/^[\w-]{1,64}$/.test(l.id)||linkIds.has(l.id)||!ids.has(l.source)||!ids.has(l.destination)||l.source===l.destination||!['healthy','degraded','down','unknown'].includes(l.status)||!optionalNumber(l.rttMs)||!optionalNumber(l.upBps)||!optionalNumber(l.downBps))throw Error('Invalid connection');
 linkIds.add(l.id);
 const ts=typeof l.updatedAt==='string'&&/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})$/.test(l.updatedAt)?Date.parse(l.updatedAt):NaN;
 const fresh=Number.isFinite(ts)&&ts<=now&&now-ts<=MAX_AGE_MS;
 links.push({id:l.id,source:l.source,destination:l.destination,status:fresh?l.status:'unknown',rttMs:fresh&&l.status!=='unknown'?(l.rttMs??null):null,upBps:fresh&&l.status!=='unknown'?(l.upBps??null):null,downBps:fresh&&l.status!=='unknown'?(l.downBps??null):null,updatedAt:Number.isFinite(ts)?new Date(ts).toISOString():null});
 }
 return {nodes,links};
}
const input=byId('topologyFile'),status=byId('snapshotStatus');
if(!input)return;
input.addEventListener('change',async()=>{
 try{const f=input.files&&input.files[0];if(!f)return;if(f.size>MAX_BYTES)throw Error('File exceeds 512 KiB');
 const topology=parse(await f.text());window.dispatchEvent(new CustomEvent('baft:topology',{detail:topology}));
 if(status)status.textContent='Imported '+topology.nodes.length+' nodes and '+topology.links.length+' connections (stale states set to unknown).';
 }catch(e){if(status)status.textContent='Import rejected: '+e.message}finally{input.value=''}
});
})();
