package bcc

const dashboardHTML = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>BAFT Command Center</title>
<style>
:root{color-scheme:dark;font-family:Inter,system-ui,sans-serif;background:#090b10;color:#eef1f6}
body{margin:0;background:radial-gradient(circle at top,#182238,#090b10 42%);min-height:100vh}
.wrap{max-width:1180px;margin:auto;padding:28px}.top{display:flex;justify-content:space-between;align-items:center;gap:16px}
h1{font-size:24px;margin:0}.muted{color:#8e9aaf}.card{background:#111722;border:1px solid #263147;border-radius:16px;padding:18px;margin-top:18px}
.grid{display:grid;grid-template-columns:repeat(4,minmax(0,1fr));gap:10px}.grid input,.grid select{background:#0b1019;border:1px solid #2a3650;color:#fff;border-radius:10px;padding:10px}
button{background:#e8b54a;color:#16100a;border:0;border-radius:10px;padding:10px 14px;font-weight:700;cursor:pointer}button.alt{background:#25324a;color:#e8eef8}
table{width:100%;border-collapse:collapse;margin-top:12px}th,td{text-align:left;padding:12px;border-bottom:1px solid #222c3e;font-size:14px}
.badge{padding:4px 9px;border-radius:999px;background:#263147}.up{background:#123e2c;color:#92f0bf}.down{background:#4b2026;color:#ffabb6}
.actions{display:flex;gap:10px;flex-wrap:wrap;margin-top:14px}@media(max-width:760px){.grid{grid-template-columns:1fr}table{font-size:12px}.wrap{padding:14px}}
</style></head>
<body><div class="wrap">
<div class="top"><div><h1>BAFT Command Center</h1><div class="muted">Dynamic N-Node control plane · Alpha</div></div><button class="alt" onclick="setToken()">Admin Token</button></div>
<div class="card"><b>Register / update node</b><div class="grid" style="margin-top:12px">
<input id="nid" placeholder="node id"><input id="alias" placeholder="alias"><input id="addr" placeholder="host:port">
<select id="role"><option value="foreign">foreign</option><option value="worker">worker</option><option value="master">master</option></select>
<input id="pub" placeholder="public key"><input id="agent" placeholder="agent token"><button onclick="saveNode()">Save Node</button>
</div></div>
<div class="card"><div class="top"><b>Cluster Nodes</b><span id="count" class="muted"></span></div>
<table><thead><tr><th></th><th>Alias</th><th>ID</th><th>Address</th><th>Role</th><th>Health</th><th>Last Check</th></tr></thead><tbody id="rows"></tbody></table>
<div class="actions"><button onclick="deploy()">One-Click Deploy Selected</button><button class="alt" onclick="enroll()">Enroll Worker Across Foreign Nodes</button><button class="alt" onclick="load()">Refresh</button></div></div>
<div class="card"><b>Recent Jobs</b><div id="jobs" class="muted" style="margin-top:10px">Admin token required to view jobs.</div></div>
</div>
<script>
const q=s=>document.querySelector(s);let nodes=[];
function token(){return localStorage.getItem('bccToken')||''}
function setToken(){localStorage.setItem('bccToken',prompt('BCC admin token')||'');loadJobs()}
function ah(){return {'Authorization':'Bearer '+token(),'Content-Type':'application/json'}}
async function load(){nodes=await (await fetch('/api/nodes')).json();q('#count').textContent=nodes.length+' nodes';
q('#rows').innerHTML=nodes.map(n=>'<tr><td><input type=checkbox class=pick value="'+esc(n.id)+'"></td><td>'+esc(n.alias)+'</td><td>'+esc(n.id)+'</td><td>'+esc(n.address)+'</td><td><span class=badge>'+esc(n.role)+'</span></td><td><span class="badge '+esc(n.health)+'">'+esc(n.health)+'</span></td><td>'+esc(n.last_checked||'—')+'</td></tr>').join('');loadJobs()}
function esc(v){return String(v??'').replace(/[&<>"']/g,c=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]))}
async function saveNode(){let body={ID:q('#nid').value,Alias:q('#alias').value,Address:q('#addr').value,Role:q('#role').value,PublicKey:q('#pub').value,AgentToken:q('#agent').value};
let r=await fetch('/api/nodes',{method:'POST',headers:ah(),body:JSON.stringify(body)});if(!r.ok)alert(await r.text());else load()}
async function deploy(){let ids=[...document.querySelectorAll('.pick:checked')].map(x=>x.value);let version=prompt('BAFT version (example: v0.3.0)');if(!version)return;
let r=await fetch('/api/deploy',{method:'POST',headers:ah(),body:JSON.stringify({node_ids:ids,version})});alert(r.ok?'Deploy jobs queued':await r.text());loadJobs()}
async function enroll(){let worker_id=prompt('Worker node ID');if(!worker_id)return;let n=nodes.find(x=>x.id===worker_id);let public_key=n?.public_key||prompt('Worker public key');if(!public_key)return;
let r=await fetch('/api/enroll',{method:'POST',headers:ah(),body:JSON.stringify({worker_id,public_key})});alert(r.ok?'Enrollment jobs queued for foreign nodes':await r.text());loadJobs()}
async function loadJobs(){if(!token())return;let r=await fetch('/api/jobs',{headers:ah()});if(!r.ok){q('#jobs').textContent='Unable to load jobs';return}let j=await r.json();q('#jobs').innerHTML=j.slice(0,20).map(x=>'<div>'+esc(x.id)+' · '+esc(x.type)+' · '+esc(x.node_id)+' · <b>'+esc(x.status)+'</b></div>').join('')||'No jobs'}
load();setInterval(load,5000);
</script></body></html>`
