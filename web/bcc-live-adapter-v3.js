/* BCC read-only topology adapter. Must be served on SAME ORIGIN as BCC.
   No token persistence; no background polling. Node coordinates are user-approved
   local input, not inferred from country names or IP addresses. */
(function(){
'use strict';
const $=id=>document.getElementById(id),status=$('bccLiveStatus'),btn=$('bccLiveLoad');
if(!btn)return;
let adminToken='';
function msg(t){if(status)status.textContent=t}
async function get(path){
 const headers={'Accept':'application/json'};if(adminToken)headers['Authorization']='Bearer '+adminToken;
 const r=await fetch(path,{method:'GET',headers,credentials:'same-origin',cache:'no-store',redirect:'error'});
 if(!r.ok){if(r.status===401||r.status===403)adminToken='';throw Error(path+': HTTP '+r.status)}
 return r.json();
}
function coordsFromText(raw){
 let d=JSON.parse(raw||'{}');if(!d||typeof d!=='object'||Array.isArray(d))throw Error('Coordinates must be a JSON object');
 return d;
}
function normalizeCoords(c){
 if(!c||typeof c.lat!=='number'||typeof c.lon!=='number'||!Number.isFinite(c.lat)||!Number.isFinite(c.lon)||Math.abs(c.lat)>90||Math.abs(c.lon)>180)return null;
 return {lat:c.lat,lon:c.lon};
}
function validId(s){return typeof s==='string'&&/^[\w-]{1,64}$/.test(s)}
function build(tunnels,locations){
 const used=new Set(),links=[],nodes=[];
 for(const t of tunnels){
  if(!t||!validId(t.id)||!validId(t.ir_node)||!validId(t.ex_node)||t.ir_node===t.ex_node)continue;
  if(!normalizeCoords(locations[t.ir_node])||!normalizeCoords(locations[t.ex_node]))continue;
  if(used.has(t.id))continue;used.add(t.id);
  // Node health is NOT tunnel health. Tunnel phase 'active' is NOT live proof.
  // Without tunnel-scoped evidence, do not mark an active tunnel healthy.
  let status='unknown'; // lifecycle phase cannot prove current packet-path health
  links.push({id:t.id,source:t.ir_node,destination:t.ex_node,status,rttMs:null,upBps:null,downBps:null,updatedAt:null});
 }
 const ids=new Set();for(const l of links){ids.add(l.source);ids.add(l.destination)}
 for(const id of ids){const c=normalizeCoords(locations[id]);nodes.push({id,name:id,lat:c.lat,lon:c.lon})}
 return {nodes,links};
}
btn.addEventListener('click',async()=>{
 btn.disabled=true;try{
 if(location.protocol!=='https:'&&!(location.protocol==='http:'&&['localhost','127.0.0.1','[::1]'].includes(location.hostname)))throw Error('Authenticated BCC API requires HTTPS or local development origin. Do not enter a token in file:// preview.');
 const loc=coordsFromText($('bccCoordinates').value);
 if(!adminToken){adminToken=prompt('BCC admin token (used in memory for this page only):')||'';if(!adminToken){msg('Token required.');return}}
 const ts=await get('/api/tunnels');
 if(!Array.isArray(ts))throw Error('Unexpected BCC API response');
 const d=build(ts,loc);window.dispatchEvent(new CustomEvent('baft:topology',{detail:d}));
 msg('Read-only BCC snapshot: '+d.links.length+' mapped tunnels; all link health UNKNOWN until link-scoped telemetry is available. Node telemetry is not link telemetry.');
 }catch(e){msg('Read-only load failed: '+e.message)}finally{btn.disabled=false}
});
})();
