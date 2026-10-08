/* BAFT georeferenced interactive globe preview. No backend or production calls.
   Coordinates are WGS84 illustrative city points, not inferred server locations. */
(function(){
'use strict';
const R=Math.PI/180, TWO=Math.PI*2;
const examples=[
{id:'ir',name:'Tehran (demo)',lat:35.6892,lon:51.3890},
{id:'de',name:'Frankfurt (demo)',lat:50.1109,lon:8.6821},
{id:'nl',name:'Amsterdam (demo)',lat:52.3676,lon:4.9041},
{id:'uk',name:'London (demo)',lat:51.5074,lon:-0.1278},
{id:'tr',name:'Istanbul (demo)',lat:41.0082,lon:28.9784},
{id:'ca',name:'Toronto (demo)',lat:43.6532,lon:-79.3832}
];
const links=examples.slice(1).map((n,i)=>({id:'demo-'+n.id,source:'ir',destination:n.id,status:i===3?'down':'unknown',rttMs:null,upBps:null,downBps:null,updatedAt:null}));
const canvas=document.getElementById('baftGlobe');if(!canvas)return;
const ctx=canvas.getContext('2d');if(!ctx)return;
const label=document.getElementById('globeSelection');
const pause=window.matchMedia('(prefers-reduced-motion: reduce)').matches;
let w=1,h=1,rotLon=25*R,rotLat=24*R,zoom=1,selected=null,hovered=null,drag=false,lastX=0,lastY=0,anim=0,focus=null,focusStart=0,focusDuration=900;
function xyz(lat,lon){let a=lat*R,b=lon*R;return [Math.cos(a)*Math.sin(b),Math.sin(a),Math.cos(a)*Math.cos(b)]}
function project(lat,lon){let [x,y,z]=xyz(lat,lon),c=Math.cos(rotLon),s=Math.sin(rotLon);[x,z]=[x*c-z*s,x*s+z*c];c=Math.cos(rotLat);s=Math.sin(rotLat);[y,z]=[y*c-z*s,y*s+z*c];let radius=Math.min(w,h)*.41*zoom;return {x:w/2+x*radius,y:h/2-y*radius,z,radius}}
function arc(a,b,t){const va=xyz(a.lat,a.lon),vb=xyz(b.lat,b.lon),dot=Math.max(-1,Math.min(1,va.reduce((v,x,i)=>v+x*vb[i],0))),theta=Math.acos(dot);let v;if(theta<1e-6)v=va;else{let k=Math.sin(theta),aa=Math.sin((1-t)*theta)/k,bb=Math.sin(t*theta)/k;v=va.map((x,i)=>aa*x+bb*vb[i])}let m=Math.hypot(...v),lat=Math.asin(v[1]/m)/R,lon=Math.atan2(v[0],v[2])/R;let p=project(lat,lon),height=1+0.18*Math.sin(Math.PI*t);return {...p,x:w/2+(p.x-w/2)*height,y:h/2+(p.y-h/2)*height,z:p.z*height}}
function coords(e){const b=canvas.getBoundingClientRect();return {x:(e.clientX-b.left)*w/b.width,y:(e.clientY-b.top)*h/b.height}}
function pointDist(x,y,a,b){let dx=b.x-a.x,dy=b.y-a.y,t=Math.max(0,Math.min(1,((x-a.x)*dx+(y-a.y)*dy)/(dx*dx+dy*dy||1)));return Math.hypot(x-a.x-t*dx,y-a.y-t*dy)}
function draw(time){
if(focus){const t=Math.min(1,(time-focusStart)/focusDuration),ease=t*t*(3-2*t);rotLon=focus.fromLon+(focus.toLon-focus.fromLon)*ease;rotLat=focus.fromLat+(focus.toLat-focus.fromLat)*ease;zoom=focus.fromZoom+(focus.toZoom-focus.fromZoom)*ease;if(t>=1)focus=null;}
ctx.clearRect(0,0,w,h);const p=project(0,0),radius=p.radius;
let g=ctx.createRadialGradient(w*.43,h*.36,radius*.15,w/2,h/2,radius*1.2);g.addColorStop(0,'#385b71');g.addColorStop(.65,'#172a3b');g.addColorStop(1,'#07101a');
ctx.beginPath();ctx.arc(w/2,h/2,radius,0,TWO);ctx.fillStyle=g;ctx.fill();ctx.strokeStyle='rgba(209,176,113,.35)';ctx.lineWidth=2;ctx.stroke();
ctx.save();ctx.beginPath();ctx.arc(w/2,h/2,radius,0,TWO);ctx.clip();
ctx.strokeStyle='rgba(178,202,211,.15)';ctx.lineWidth=1;
for(let lat=-75;lat<=75;lat+=15){ctx.beginPath();let started=false;for(let lon=-180;lon<=180;lon+=3){let v=project(lat,lon);if(v.z<0){started=false;continue}if(!started){ctx.moveTo(v.x,v.y);started=true}else ctx.lineTo(v.x,v.y)}ctx.stroke()}
for(let lon=-180;lon<180;lon+=15){ctx.beginPath();let started=false;for(let lat=-90;lat<=90;lat+=3){let v=project(lat,lon);if(v.z<0){started=false;continue}if(!started){ctx.moveTo(v.x,v.y);started=true}else ctx.lineTo(v.x,v.y)}ctx.stroke()}ctx.restore();
for(const l of links){let a=examples.find(n=>n.id===l.source),b=examples.find(n=>n.id===l.destination);const active=l.id===(selected||hovered),color=l.status==='down'?'#e66b72':l.status==='healthy'?'#d6b16d':'#9ba8b2';ctx.strokeStyle=color;ctx.lineWidth=active?3.7:1.7;ctx.shadowColor=color;ctx.shadowBlur=active?13:5;ctx.beginPath();for(let i=0;i<=70;i++){let v=arc(a,b,i/70);if(i===0)ctx.moveTo(v.x,v.y);else ctx.lineTo(v.x,v.y)}ctx.stroke();ctx.shadowBlur=0;
if(l.status!=='unknown'&&!pause){let t=((time/4000)%1);let v=arc(a,b,t);ctx.beginPath();ctx.arc(v.x,v.y,active?4:2.5,0,TWO);ctx.fillStyle=color;ctx.fill()}
if(l.status==='down'&&!pause){let v=arc(a,b,.5);ctx.globalAlpha=.4+.6*Math.abs(Math.sin(time/500));ctx.beginPath();ctx.arc(v.x,v.y,active?7:4,0,TWO);ctx.fillStyle=color;ctx.fill();ctx.globalAlpha=1}}
ctx.font='12px system-ui';ctx.textAlign='center';for(const n of examples){let v=project(n.lat,n.lon);if(v.z<-.12)continue;ctx.fillStyle='#f2d18f';ctx.beginPath();ctx.arc(v.x,v.y,5,0,TWO);ctx.fill();ctx.fillStyle='#f1eee8';ctx.fillText(n.id.toUpperCase(),v.x,v.y-12)}
if(!pause)requestAnimationFrame(draw)
}
function hit(x,y){let best=null,dist=14;for(const l of links){let a=examples.find(n=>n.id===l.source),b=examples.find(n=>n.id===l.destination),prev=arc(a,b,0);for(let i=1;i<=55;i++){let next=arc(a,b,i/55),d=pointDist(x,y,prev,next);if(d<dist){dist=d;best=l.id}prev=next}}return best}
function inspect(id,autoFocus=false){selected=id;const l=links.find(x=>x.id===id);if(!l)return;const a=examples.find(n=>n.id===l.source),b=examples.find(n=>n.id===l.destination);if(label)label.textContent=a.name+' → '+b.name+' | '+l.status.toUpperCase()+' | RTT: unknown | Up/Down: unknown | Updated: never (demo)';if(autoFocus&&!pause){const mid=arc(a,b,.5);const targetLon=Math.atan2(Math.sin((a.lon+b.lon)*R/2),Math.cos((a.lon+b.lon)*R/2));let delta=targetLon-rotLon;while(delta>Math.PI)delta-=TWO;while(delta< -Math.PI)delta+=TWO;focus={fromLon:rotLon,toLon:rotLon+delta,fromLat:rotLat,toLat:Math.max(-.7,Math.min(.7,(a.lat+b.lat)*R/2*.5)),fromZoom:zoom,toZoom:1.15};focusStart=performance.now();}}}
canvas.addEventListener('pointerdown',e=>{drag=true;lastX=e.clientX;lastY=e.clientY;canvas.setPointerCapture(e.pointerId)});
canvas.addEventListener('pointermove',e=>{if(drag){focus=null;rotLon+=(e.clientX-lastX)*.006;rotLat=Math.max(-1.1,Math.min(1.1,rotLat+(e.clientY-lastY)*.006));lastX=e.clientX;lastY=e.clientY;if(pause)draw(0)}else{let q=coords(e);hovered=hit(q.x,q.y);canvas.style.cursor=hovered?'pointer':'grab';if(hovered&&selected!==hovered)inspect(hovered,true);if(pause)draw(0)}});
canvas.addEventListener('pointerup',e=>{drag=false;const q=coords(e),id=hit(q.x,q.y);if(id)inspect(id,true)});
canvas.addEventListener('wheel',e=>{e.preventDefault();focus=null;zoom=Math.max(.65,Math.min(1.45,zoom-e.deltaY*.001));if(pause)draw(0)},{passive:false});
canvas.addEventListener('keydown',e=>{if(e.key==='ArrowLeft')rotLon-=.1;else if(e.key==='ArrowRight')rotLon+=.1;else if(e.key==='ArrowUp')rotLat=Math.min(1.1,rotLat+.1);else if(e.key==='ArrowDown')rotLat=Math.max(-1.1,rotLat-.1);else if(e.key==='Enter'){inspect(links[(links.findIndex(l=>l.id===selected)+1)%links.length].id,true)}else return;e.preventDefault();if(pause)draw(0)});
function resize(){const rect=canvas.getBoundingClientRect(),dpr=Math.min(devicePixelRatio||1,2);w=Math.max(1,Math.round(rect.width*dpr));h=Math.max(1,Math.round(rect.height*dpr));canvas.width=w;canvas.height=h;if(pause)draw(0)}
new ResizeObserver(resize).observe(canvas);resize();if(!pause)requestAnimationFrame(draw);
})();
