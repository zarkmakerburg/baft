'use strict';
const test=require('node:test'),assert=require('node:assert/strict'),fs=require('node:fs'),path=require('node:path');
const go=fs.readFileSync(path.join(__dirname,'..','..','internal','bcc','dashboard.go'),'utf8');
test('BCC dashboard keeps all existing operational sections',()=>{
for(const name of ['Live Monitoring','7-Day Traffic & Uptime','Register / update node','Add a server over SSH','Existing tunnels','Layered health','Change History','Safe SSH Port Migration','Route Doctor','Automatic Path Discovery','Connectivity Matrix','Cluster Nodes','Recent Jobs','Immutable Audit Log','Node Finance','Finance Reports'])assert.ok(go.includes(name),'Missing: '+name);
});
test('BCC visual tokens and focus states are present',()=>{for(const token of ['--bcc-bg','--bcc-panel','--bcc-line','--bcc-gold','--bcc-focus'])assert.ok(go.includes(token));assert.ok(go.includes('button:focus-visible'));assert.ok(go.includes('prefers-reduced-motion'))});
test('BCC dashboard remains wired to existing operational loaders',()=>{for(const fn of ['loadNodes','loadMonitoring','loadJobs','loadAudit','loadFinance','loadHistory','loadTunnels','loadAll'])assert.ok(go.includes('function '+fn+'('),'Missing loader: '+fn)});
