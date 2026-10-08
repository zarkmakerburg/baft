'use strict';
const test=require('node:test'),assert=require('node:assert/strict'),fs=require('node:fs'),path=require('node:path');
const html=fs.readFileSync(path.join(__dirname,'..','bcc-welcome-login-v3-preview.html'),'utf8');
test('welcome and login sections are present',()=>{for(const id of ['welcome','capabilities','signin','demoLogin','user','password','officialLogo'])assert.match(html,new RegExp('id="'+id+'"'))});
test('sign-in controls are disabled until backend security review',()=>{assert.match(html,/<input id="user"[^>]*disabled/);assert.match(html,/<input id="password"[^>]*disabled/);assert.match(html,/<button type="submit"[^>]*disabled/)});
test('no outbound login API or credential storage',()=>{assert.doesNotMatch(html,/\bfetch\s*\(|XMLHttpRequest|localStorage|sessionStorage|sendBeacon/);assert.match(html,/preventDefault\(\)/)});
test('bilingual text and direction support',()=>{assert.match(html,/fa:\{/);assert.match(html,/en:\{/);assert.match(html,/root\.dir=lang==='fa'\?'rtl':'ltr'/)});
test('safe preview metadata',()=>{assert.match(html,/noindex,nofollow/);assert.match(html,/no-referrer/);assert.match(html,/prefers-reduced-motion/);assert.match(html,/class="skip"/)});
test('logo is external official asset path, never an invented replacement',()=>{assert.match(html,/assets\/baft-official-logo\.png/);assert.match(html,/img\.addEventListener\('error'/)});

test('welcome page does not expose a live credential submission route',()=>{assert.doesNotMatch(html,/<form[^>]+action=/i);assert.match(html,/<button type="submit"[^>]*disabled/i);});
test('all localization keys are present in both languages',()=>{
 const keys=[...html.matchAll(/data-i="([^"]+)"/g)].map(m=>m[1]);
 const fa=html.split('fa:{')[1].split('},\nen:{')[0],en=html.split('en:{')[1].split('}};')[0];
 for(const key of keys){assert.ok(fa.includes(key+':'), 'Missing Persian: '+key);assert.ok(en.includes(key+':'),'Missing English: '+key)}
});
test('primary anchors point to real sections',()=>{for(const id of ['signin','capabilities'])assert.match(html,new RegExp('href="#'+id+'"'));});
test('all interactive controls have labels',()=>{assert.match(html,/<label for="user"/);assert.match(html,/<label for="password"/);assert.match(html,/<button(?=[^>]*id="lang")(?=[^>]*aria-label=)[^>]*>/);assert.match(html,/<button(?=[^>]*id="theme")(?=[^>]*aria-label=)[^>]*>/);});
test('mobile layout and reduced-motion support are present',()=>{assert.match(html,/@media\(max-width:850px\)/);assert.match(html,/@media\(max-width:520px\)/);assert.match(html,/@media\(prefers-reduced-motion:reduce\)/);});

test('accessible labels follow language and theme changes',()=>{for(const label of ['تغییر زبان به انگلیسی','Switch language to Persian','فعال‌کردن حالت روشن','فعال‌کردن حالت تاریک','Switch to light theme','Switch to dark theme','امکانات مرکز فرماندهی','Command Center capabilities'])assert.ok(html.includes(label),'Missing localized label: '+label);assert.match(html,/document\.title=lang==='fa'/);});
