package bcc

const dashboardHTML = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>BAFT Command Center</title>
<style>
:root{color-scheme:dark;font-family:Inter,system-ui,sans-serif;background:#090b10;color:#eef1f6}
body{margin:0;background:radial-gradient(circle at top,#182238,#090b10 42%);min-height:100vh}
.wrap{max-width:1280px;margin:auto;padding:24px}.top{display:flex;justify-content:space-between;align-items:center;gap:12px}
h1{font-size:24px;margin:0}.muted{color:#8e9aaf}.card{background:#111722;border:1px solid #263147;border-radius:16px;padding:18px;margin-top:16px}
.grid{display:grid;grid-template-columns:repeat(4,minmax(0,1fr));gap:10px}.grid input,.grid select,select{background:#0b1019;border:1px solid #2a3650;color:#fff;border-radius:10px;padding:10px}
button{background:#e8b54a;color:#16100a;border:0;border-radius:10px;padding:10px 14px;font-weight:700;cursor:pointer}button.alt{background:#25324a;color:#e8eef8}
table{width:100%;border-collapse:collapse;margin-top:12px}th,td{text-align:left;padding:10px;border-bottom:1px solid #222c3e;font-size:13px;vertical-align:top}
.badge{display:inline-block;padding:4px 9px;border-radius:999px;background:#263147;text-transform:uppercase;font-size:11px}.up{background:#123e2c;color:#92f0bf}.down{background:#4b2026;color:#ffabb6}.unknown{background:#37333a;color:#d9cedd}
.route{display:flex;gap:6px;align-items:center;margin:3px 0;white-space:nowrap}.actions{display:flex;gap:10px;flex-wrap:wrap;margin-top:12px}
.kpis{display:grid;grid-template-columns:repeat(4,minmax(0,1fr));gap:10px}.kpi{background:#0b1019;border-radius:12px;padding:12px}.kpi b{display:block;font-size:20px;margin-top:4px}
canvas{width:100%;height:240px;background:#0b1019;border-radius:12px;margin-top:12px}
@media(max-width:820px){.grid,.kpis{grid-template-columns:1fr 1fr}.wrap{padding:12px}.tablewrap{overflow:auto}}@media(max-width:520px){.grid,.kpis{grid-template-columns:1fr}}
</style></head>
<body><div class="wrap">
<div class="top"><div><h1>BAFT Command Center</h1><div class="muted">Operational monitoring, finance & hardening · Step 5.4</div></div><button class="alt" onclick="setToken()">Admin Token</button></div>

<div class="card">
<div class="top"><b>Live Monitoring</b><span id="monitorTime" class="muted">—</span></div>
<div class="kpis" style="margin-top:12px"><div class="kpi">Nodes<b id="kNodes">0</b></div><div class="kpi">UP<b id="kUp">0</b></div><div class="kpi">DOWN<b id="kDown">0</b></div><div class="kpi">UNKNOWN<b id="kUnknown">0</b></div></div>
<div class="tablewrap"><table><thead><tr><th>Node</th><th>Status</th><th>Noise RTT</th><th>Health latency</th><th>Error rate</th><th>Last Seen</th><th>Sessions</th><th>Routes</th></tr></thead><tbody id="monitorRows"></tbody></table></div>
</div>

<div class="card"><div class="top"><b>7-Day Traffic & Uptime</b><select id="historyNode" onchange="loadHistory()"></select></div><canvas id="historyCanvas" width="1100" height="240"></canvas><div id="historyLegend" class="muted" style="margin-top:8px">Select a node.</div></div>

<div class="card"><b>Register / update node</b><div class="grid" style="margin-top:12px">
<input id="nid" placeholder="node id"><input id="alias" placeholder="alias"><input id="addr" placeholder="host:port">
<select id="role"><option value="foreign">foreign</option><option value="worker">worker</option><option value="master">master</option></select>
<input id="pub" placeholder="public key"><input id="agentEnv" placeholder="agent token env name"><button onclick="saveNode()">Save Node</button>
</div></div>

<div class="card"><div class="top"><b>Cluster Nodes</b><span id="count" class="muted"></span></div>
<div class="tablewrap"><table><thead><tr><th></th><th>Alias</th><th>ID</th><th>Address</th><th>Role</th><th>Health Check</th><th>Last Check</th><th>Security</th></tr></thead><tbody id="rows"></tbody></table></div>
<div class="actions"><button onclick="deploy()">One-Click Deploy Selected</button><button class="alt" onclick="enroll()">Enroll Worker Across Foreign Nodes</button><button class="alt" onclick="loadAll()">Refresh</button></div></div>

<div class="card"><b>Recent Jobs</b><div id="jobs" class="muted" style="margin-top:10px">Admin token required.</div></div>
<div class="card"><div class="top"><b>Immutable Audit Log</b><button class="alt" onclick="loadAudit()">Refresh</button></div>
<div class="muted" style="margin-top:8px">Read-only hash-chained admin events. No delete/edit action is exposed.</div>
<div class="tablewrap"><table><thead><tr><th>#</th><th>UTC</th><th>IP</th><th>Action</th><th>Target</th><th>Outcome</th><th>Hash</th></tr></thead><tbody id="auditRows"></tbody></table></div></div>
<div class="card"><div class="top"><b>Node Finance</b><button class="alt" onclick="setFinance()">Set Versioned Rate</button></div>
<div class="tablewrap"><table><thead><tr><th>Node</th><th>Traffic GiB</th><th>Cost</th><th>Revenue</th><th>Profit</th></tr></thead><tbody id="financeRows"></tbody></table></div></div>

<div class="card"><div class="top"><b>Finance Reports</b><span class="muted">Default timezone: Asia/Tehran</span></div>
<div class="grid" style="margin-top:12px">
<select id="reportPeriod"><option value="daily">Daily</option><option value="monthly">Monthly</option></select>
<input id="reportFrom" type="date"><input id="reportTo" type="date"><input id="reportTZ" value="Asia/Tehran" placeholder="Timezone">
<button onclick="loadFinanceReport()">Load Report</button><button class="alt" onclick="downloadFinanceCSV()">Download CSV</button>
</div>
<div class="tablewrap"><table><thead><tr><th>Period</th><th>Scope</th><th>Node</th><th>Ingress</th><th>Egress</th><th>Cost</th><th>Revenue</th><th>Profit</th><th>Currency</th></tr></thead><tbody id="reportRows"></tbody></table></div>
</div>
</div>
<script>
const q=s=>document.querySelector(s);let nodes=[],monitor=[];
function token(){return localStorage.getItem('bccToken')||''}
function setToken(){localStorage.setItem('bccToken',prompt('BCC admin token')||'');loadAll()}
function ah(){return {'Authorization':'Bearer '+token(),'Content-Type':'application/json'}}
function esc(v){return String(v??'').replace(/[&<>"']/g,c=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]))}
function ago(v){if(!v)return '—';let d=(Date.now()-new Date(v).getTime())/1000;if(d<60)return Math.max(0,Math.round(d))+'s ago';if(d<3600)return Math.round(d/60)+'m ago';return Math.round(d/3600)+'h ago'}
function rate(v){return (Number(v||0)/1000).toFixed(2)+'/min'}
function routeHTML(rs){if(!rs||!rs.length)return '<span class=muted>—</span>';return rs.map(r=>'<div class=route><span class="badge '+esc(r.status)+'">'+esc(r.status)+'</span><span>'+esc(r.route_id)+'</span><span class=muted>'+((r.latency_ms??-1)>=0?esc(r.latency_ms)+' ms':'—')+' · '+esc(r.probe_kind||'')+'</span></div>').join('')}
async function loadNodes(){if(!token())return;let r=await fetch('/api/nodes',{headers:ah()});if(!r.ok)return;nodes=await r.json();q('#count').textContent=nodes.length+' nodes';q('#rows').innerHTML=nodes.map(n=>'<tr><td><input type=checkbox class=pick value="'+esc(n.id)+'"></td><td>'+esc(n.alias)+'</td><td>'+esc(n.id)+'</td><td>'+esc(n.address)+'</td><td>'+esc(n.role)+'</td><td><span class="badge '+esc(n.health)+'">'+esc(n.health)+'</span> '+((n.latency_ms??-1)>=0?esc(n.latency_ms)+' ms':'')+'</td><td>'+esc(n.last_checked||'—')+'</td><td>'+(n.revoked?'<span class="badge down">REVOKED</span>':'<button class="alt" onclick="rotateToken(\''+esc(n.id)+'\')">Rotate</button> <button onclick="revokeNode(\''+esc(n.id)+'\')">Kill</button>')+'</td></tr>').join('')}
async function loadMonitoring(){if(!token())return;let r=await fetch('/api/monitoring',{headers:ah()});if(!r.ok)return;monitor=await r.json();q('#monitorTime').textContent=new Date().toLocaleTimeString();q('#kNodes').textContent=monitor.length;q('#kUp').textContent=monitor.filter(x=>x.status==='up').length;q('#kDown').textContent=monitor.filter(x=>x.status==='down').length;q('#kUnknown').textContent=monitor.filter(x=>x.status==='unknown').length;
q('#monitorRows').innerHTML=monitor.map(n=>'<tr><td><b>'+esc(n.alias)+'</b><div class=muted>'+esc(n.node_id)+'</div></td><td><span class="badge '+esc(n.status)+'">'+esc(n.status)+'</span></td><td>'+((n.noise_latency_ms??-1)>=0?esc(n.noise_latency_ms)+' ms':'—')+'<div class=muted>Noise RTT</div></td><td>'+((n.latency_ms??-1)>=0?esc(n.latency_ms)+' ms':'—')+'<div class=muted>TCP health-check</div></td><td>'+rate(n.handshake_error_rate_milli_per_min)+'</td><td>'+ago(n.last_seen)+'</td><td>'+esc(n.active_sessions||0)+'</td><td>'+routeHTML(n.routes)+'</td></tr>').join('');
let sel=q('#historyNode'),cur=sel.value;sel.innerHTML=monitor.map(n=>'<option value="'+esc(n.node_id)+'">'+esc(n.alias)+'</option>').join('');if(cur&&monitor.some(n=>n.node_id===cur))sel.value=cur;if(!sel.dataset.loaded&&monitor.length){sel.dataset.loaded='1';loadHistory()}}
async function loadJobs(){if(!token())return;let r=await fetch('/api/jobs',{headers:ah()});if(!r.ok)return;let j=await r.json();q('#jobs').innerHTML=j.slice(0,20).map(x=>'<div>'+esc(x.id)+' · '+esc(x.type)+' · '+esc(x.node_id)+' · <b>'+esc(x.status)+'</b></div>').join('')||'No jobs'}
async function loadAudit(){if(!token())return;let r=await fetch('/api/audit',{headers:ah()});if(!r.ok)return;let a=await r.json();q('#auditRows').innerHTML=a.slice().reverse().map(x=>'<tr><td>'+esc(x.sequence)+'</td><td>'+esc(x.timestamp)+'</td><td>'+esc(x.remote_ip)+'</td><td>'+esc(x.action)+'</td><td>'+esc(x.target||'—')+'</td><td>'+esc(x.outcome)+'</td><td class=muted>'+esc((x.hash||'').slice(0,16))+'…</td></tr>').join('')}
function micros(v){return (Number(v||0)/1000000).toFixed(4)}
async function loadFinance(){if(!token())return;let r=await fetch('/api/finance',{headers:ah()});if(!r.ok)return;let fs=await r.json();q('#financeRows').innerHTML=fs.map(x=>{let n=nodes.find(n=>n.id===x.node_id);let gib=(Number(x.ingress_bytes||0)+Number(x.egress_bytes||0))/1073741824;return '<tr><td>'+esc(n?.alias||x.node_id)+'</td><td>'+gib.toFixed(3)+'</td><td>'+micros(x.cost_micros)+'</td><td>'+micros(x.revenue_micros)+'</td><td>'+micros(x.profit_micros)+'</td></tr>'}).join('')}
async function loadHistory(){if(!token())return;let id=q('#historyNode').value;if(!id)return;let r=await fetch('/api/history?node_id='+encodeURIComponent(id),{headers:ah()});if(!r.ok)return;drawHistory(await r.json())}
function drawHistory(points){let c=q('#historyCanvas'),x=c.getContext('2d'),w=c.width,h=c.height;x.clearRect(0,0,w,h);x.strokeStyle='#263147';x.strokeRect(0,0,w,h);if(!points.length){q('#historyLegend').textContent='No history yet.';return}let vals=points.map(p=>Number(p.ingress_bytes||0)+Number(p.egress_bytes||0)),max=Math.max(1,...vals),step=w/Math.max(1,points.length-1);x.lineWidth=2;x.strokeStyle='#e8b54a';x.beginPath();points.forEach((p,i)=>{let y=h-20-(vals[i]/max)*(h-40);if(i===0)x.moveTo(0,y);else x.lineTo(i*step,y)});x.stroke();x.strokeStyle='#63d39a';x.beginPath();points.forEach((p,i)=>{let up=p.node_health==='up';let y=up?15:h-15;if(i===0)x.moveTo(0,y);else x.lineTo(i*step,y)});x.stroke();q('#historyLegend').textContent='Gold: cumulative traffic · Green: health-check uptime · '+points.length+' samples / last 7 days'}
async function saveNode(){let body={ID:q('#nid').value,Alias:q('#alias').value,Address:q('#addr').value,Role:q('#role').value,PublicKey:q('#pub').value,AgentTokenEnv:q('#agentEnv').value};let r=await fetch('/api/nodes',{method:'POST',headers:ah(),body:JSON.stringify(body)});if(!r.ok)alert(await r.text());else loadAll()}
async function deploy(){let ids=[...document.querySelectorAll('.pick:checked')].map(x=>x.value);let version=prompt('BAFT version');if(!version)return;let r=await fetch('/api/deploy',{method:'POST',headers:ah(),body:JSON.stringify({node_ids:ids,version})});alert(r.ok?'Deploy jobs queued':await r.text());loadJobs();loadAudit()}
async function enroll(){let worker_id=prompt('Worker node ID');if(!worker_id)return;let n=nodes.find(x=>x.id===worker_id);let public_key=n?.public_key||prompt('Worker public key');if(!public_key)return;let r=await fetch('/api/enroll',{method:'POST',headers:ah(),body:JSON.stringify({worker_id,public_key})});alert(r.ok?'Enrollment jobs queued':await r.text());loadJobs();loadAudit()}
async function revokeNode(node_id){let reason=prompt('Kill Switch reason','manual security revoke');if(reason===null)return;let r=await fetch('/api/nodes/revoke',{method:'POST',headers:ah(),body:JSON.stringify({node_id,reason})});alert(r.ok?'Node revoked immediately':await r.text());loadNodes();loadAudit()}
async function rotateToken(node_id){let agent_token_env=prompt('Environment variable containing the NEW agent token');if(!agent_token_env)return;let grace=prompt('Old-token grace seconds (0-900)','60');if(grace===null)return;let r=await fetch('/api/nodes/rotate-token',{method:'POST',headers:ah(),body:JSON.stringify({node_id,agent_token_env,grace_seconds:Number(grace)})});alert(r.ok?'Token rotation activated':await r.text());loadNodes();loadAudit()}
async function setFinance(){let node_id=prompt('Node ID');if(!node_id)return;let cost=prompt('Cost micros per GiB','0');let revenue=prompt('Revenue micros per GiB','0');let currency=prompt('Currency code','IRR')||'IRR';let effective_from=prompt('Effective from RFC3339 (blank = now)','')||'';if(cost===null||revenue===null)return;let r=await fetch('/api/finance',{method:'POST',headers:ah(),body:JSON.stringify({node_id,cost_micros_per_gib:Number(cost),revenue_micros_per_gib:Number(revenue),currency,effective_from})});alert(r.ok?'Versioned finance rate saved':await r.text());loadFinance();loadAudit()}
function reportURL(format){let p=q('#reportPeriod').value,f=q('#reportFrom').value,t=q('#reportTo').value,z=q('#reportTZ').value||'Asia/Tehran';return '/api/finance/report?period='+encodeURIComponent(p)+'&from='+encodeURIComponent(f)+'&to='+encodeURIComponent(t)+'&tz='+encodeURIComponent(z)+(format?'&format='+format:'')}
async function loadFinanceReport(){if(!token())return;let r=await fetch(reportURL(''),{headers:ah()});if(!r.ok){alert(await r.text());return}let data=await r.json();q('#reportRows').innerHTML=data.rows.map(x=>'<tr><td>'+esc(x.period)+'</td><td>'+esc(x.scope)+'</td><td>'+esc(x.node_id||'ALL')+'</td><td>'+esc(x.ingress_bytes)+'</td><td>'+esc(x.egress_bytes)+'</td><td>'+esc(x.cost_micros)+'</td><td>'+esc(x.revenue_micros)+'</td><td>'+esc(x.profit_micros)+'</td><td>'+esc(x.currency)+'</td></tr>').join('')}
async function downloadFinanceCSV(){if(!token())return;let r=await fetch(reportURL('csv'),{headers:ah()});if(!r.ok){alert(await r.text());return}let blob=await r.blob(),u=URL.createObjectURL(blob),a=document.createElement('a');a.href=u;a.download='baft-finance-'+q('#reportPeriod').value+'.csv';a.click();URL.revokeObjectURL(u)}
function initReportDates(){let d=new Date(),to=d.toISOString().slice(0,10),first=new Date(Date.UTC(d.getUTCFullYear(),d.getUTCMonth(),1)).toISOString().slice(0,10);q('#reportFrom').value=first;q('#reportTo').value=to}
async function loadAll(){await loadNodes();await Promise.all([loadMonitoring(),loadJobs(),loadFinance(),loadAudit()])}
initReportDates();loadAll();setInterval(()=>{loadMonitoring();loadFinance()},5000);
</script></body></html>`
