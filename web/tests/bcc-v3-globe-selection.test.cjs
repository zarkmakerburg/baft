'use strict';
const test=require('node:test'),assert=require('node:assert/strict'),fs=require('node:fs'),path=require('node:path');
const js=fs.readFileSync(path.join(__dirname,'..','bcc-globe-v3.js'),'utf8');
test('globe selection is explicitly pinned on click and keyboard Enter',()=>{
 assert.match(js,/if\(id\)\{pinned=true;inspect\(id,true\)\}/);
 assert.match(js,/e\.key==='Enter'&&links\.length\)\{pinned=true;inspect/);
});
test('pointer exit preserves a pinned selection',()=>{
 assert.match(js,/pointerleave',[\s\S]*?if\(!pinned\)\{selected=null;restoreHome\(\)\}/);
 assert.match(js,/pointercancel',[\s\S]*?if\(!pinned\)\{selected=null;restoreHome\(\)\}/);
});
test('Escape clears pin and restores home',()=>{
 assert.match(js,/e\.key==='Escape'\)\{pinned=false;selected=null;hovered=null;restoreHome\(\)\}/);
 assert.match(js,/function restoreHome\(\)\{if\(!home\|\|pinned\)return/);
});
test('new topology clears previous pin',()=>{
 assert.match(js,/selected=null;hovered=null;pinned=false;focus=null;home=null/);
});
