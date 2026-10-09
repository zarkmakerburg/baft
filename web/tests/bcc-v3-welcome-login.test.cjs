'use strict';
const test=require('node:test'),assert=require('node:assert/strict'),fs=require('node:fs'),path=require('node:path');
const html=fs.readFileSync(path.join(__dirname,'..','bcc-welcome-login-v3-preview.html'),'utf8');
test('welcome and login sections are present',()=>{for(const id of ['welcome','capabilities','signin','demoLogin','user','password','officialLogo'])assert.match(html,new RegExp('id="'+id+'"'))});
test('sign-in controls are disabled until backend security review',()=>{assert.match(html,/<input id="user"[^>]*disabled/);assert.match(html,/<input id="password"[^>]*disabled/);assert.match(html,/<button type="submit"[^>]*disabled/)});
test('no outbound login API or credential storage',()=>{assert.doesNotMatch(html,/\bfetch\s*\(|XMLHttpRequest|localStorage|sessionStorage|sendBeacon/);assert.match(html,/preventDefault\(\)/)});
test('bilingual text and direction support',()=>{assert.match(html,/fa:\{/);assert.match(html,/en:\{/);assert.match(html,/root\.dir=lang==='fa'\?'rtl':'ltr'/)});
test('safe preview metadata',()=>{assert.match(html,/noindex,nofollow/);assert.match(html,/no-referrer/);assert.match(html,/prefers-reduced-motion/);assert.match(html,/class="skip"/)});
test('welcome logo resolves to the same committed BAFT asset as the product shell',()=>{const src=html.match(/id="officialLogo"[^>]*src="\.\/assets\/([^"]+)"/)?.[1];assert.ok(src,'Missing external BAFT logo URL');assert.ok(fs.statSync(path.join(__dirname,'..','assets',src)).size>0,'BAFT logo asset missing or empty');const shell=fs.readFileSync(path.join(__dirname,'..','bcc-product-v3.js'),'utf8');assert.ok(shell.includes('assets/'+src),'Welcome and product logo assets differ');assert.match(html,/img\.addEventListener\('error'/)});

test('welcome page does not expose a live credential submission route',()=>{assert.doesNotMatch(html,/<form[^>]+action=/i);assert.match(html,/<button type="submit"[^>]*disabled/i);});
test('all localization keys are present in both languages',()=>{
 const keys=[...html.matchAll(/data-i="([^"]+)"/g)].map(m=>m[1]);
 const match=html.match(/const tr=\{\s*fa:\{([\s\S]*?)\},\s*en:\{([\s\S]*?)\}\};/);
 assert.ok(match,'Missing bilingual translation dictionary');
 for(const [index,language] of [[1,'Persian'],[2,'English']]){
  const found=new Set([...match[index].matchAll(/(?:^|,)\s*([a-zA-Z][a-zA-Z0-9]*):/g)].map(m=>m[1]));
  for(const key of keys)assert.ok(found.has(key),'Missing '+language+': '+key);
 }
});
test('primary anchors point to real sections',()=>{for(const id of ['signin','capabilities'])assert.match(html,new RegExp('href="#'+id+'"'));});
test('all interactive controls have labels',()=>{assert.match(html,/<label for="user"/);assert.match(html,/<label for="password"/);assert.match(html,/<button(?=[^>]*id="lang")(?=[^>]*aria-label=)[^>]*>/);assert.match(html,/<button(?=[^>]*id="theme")(?=[^>]*aria-label=)[^>]*>/);});
test('mobile layout and reduced-motion support are present',()=>{assert.match(html,/@media\(max-width:850px\)/);assert.match(html,/@media\(max-width:520px\)/);assert.match(html,/@media\(prefers-reduced-motion:reduce\)/);});

test('accessible labels follow language and theme changes',()=>{for(const label of ['تغییر زبان به انگلیسی','Switch language to Persian','فعال‌کردن حالت روشن','فعال‌کردن حالت تاریک','Switch to light theme','Switch to dark theme','امکانات مرکز فرماندهی','Command Center capabilities'])assert.ok(html.includes(label),'Missing localized label: '+label);assert.match(html,/document\.title=lang==='fa'/);});

test('welcome light/dark text tokens meet 4.5:1 contrast on intended surfaces',()=>{
 const css=html.match(/<style>([\s\S]*?)<\/style>/)?.[1];
 assert.ok(css,'Missing CSS');
 const tokens=(selector)=>{
  const start=css.indexOf(selector+'{');
  assert.notEqual(start,-1,'Missing palette '+selector);
  const block=css.slice(start+selector.length+1).split('}')[0];
  return Object.fromEntries([...block.matchAll(/--([\w-]+):\s*(#[0-9a-f]{6})/gi)].map(m=>[m[1],m[2]]));
 };
 const luminance=(hex)=>{
  assert.match(hex,/^#[0-9a-f]{6}$/i);
  const rgb=[1,3,5].map(i=>parseInt(hex.slice(i,i+2),16)/255);
  const linear=rgb.map(v=>v<=0.04045?v/12.92:((v+0.055)/1.055)**2.4);
  return 0.2126*linear[0]+0.7152*linear[1]+0.0722*linear[2];
 };
 const ratio=(a,b)=>{const x=luminance(a),y=luminance(b);return (Math.max(x,y)+0.05)/(Math.min(x,y)+0.05)};
 const pairs=[['ink','bg'],['ink','panel'],['muted','bg'],['muted','panel'],['muted','soft'],['gold','bg'],['gold','panel'],['gold','soft']];
 for(const selector of [':root',':root[data-theme=light]']){
  const t=tokens(selector);
  for(const [fg,bg] of pairs)assert.ok(ratio(t[fg],t[bg])>=4.5,selector+' '+fg+'/'+bg+' contrast '+ratio(t[fg],t[bg]).toFixed(2)+':1 below 4.5:1');
 }
});
