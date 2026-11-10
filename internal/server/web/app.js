(function(){
"use strict";
var doc=document;
var main=doc.getElementById("investigation");
var genInput=doc.querySelector('input[name="generation"]');
var seqInput=doc.querySelector('input[name="sequence"]');
var gen=1;
var seq=1;
var authed=true;
var reqMeta=new WeakMap();
var pendingAbort=null;

function bump(){
gen=Math.min(gen+1,1000000000);
seq=Math.min(seq+1,1000000000);
if(genInput)genInput.value=String(gen);
if(seqInput)seqInput.value=String(seq);
}

var guidance=Object.create(null);
guidance["422"]="Query too broad. Narrow the time window, then run Search.";
guidance["410"]="Results expired. Rerun query.";
guidance["504"]="Query timed out. Narrow the time window, then run Search.";
guidance["503"]="Service busy. Wait, then run Search again.";
var notices=Object.create(null);
notices["invalid_query"]="Query is invalid";
notices["unsupported_path"]="Attribute path not supported";
notices["row_expired"]="Row expired. Rerun query. This row is past the retention window.";
notices["not_found"]="Event not found";
var statusOwnsFocus=false;

function statusEl(){return doc.getElementById("status");}
function resultsEl(){return doc.getElementById("results-region");}
function focusStatus(st){
statusOwnsFocus=true;
st.focus();
}

function clearNamed(form,name){
var one=form.querySelector('input[name="'+name+'"]');
if(!one)one=form.querySelector('[name="'+name+'"]');
if(one)one.value="";
if(form.querySelectorAll){
var all=form.querySelectorAll('[name="'+name+'"]');
for(var i=0;i<all.length;i++)all[i].value="";
}
}

function clearProtected(){
var region=resultsEl();
if(region)region.innerHTML="";
var d=doc.getElementById("detail-region");
if(d)d.innerHTML="";
var st=statusEl();
if(st)st.textContent="";
var f=main?main.querySelector("form"):null;
if(f){
clearNamed(f,"csrf");
clearNamed(f,"source");
clearNamed(f,"from");
clearNamed(f,"to");
clearNamed(f,"severity");
clearNamed(f,"term");
clearNamed(f,"path");
clearNamed(f,"pval");
clearNamed(f,"view");
clearNamed(f,"cursor");
}
}

function enableButtons(){
var btns=doc.querySelectorAll("button[type=submit]");
for(var i=0;i<btns.length;i++){
btns[i].disabled=false;
btns[i].removeAttribute("aria-busy");
}
}

function showGuide(code){
var st=statusEl();
if(!authed||!st)return;
st.textContent=guidance[code];
var region=resultsEl();
if(code==="410"){
var cursors=doc.querySelectorAll('input[name="cursor"]');
for(var i=0;i<cursors.length;i++)cursors[i].value="";
if(region)region.textContent="";
}
if((code==="504"||code==="422"||code==="503")&&region){
region.setAttribute("data-stale","true");
}
enableButtons();
focusStatus(st);
}

function showNotice(text){
var st=statusEl();
if(!authed||!st)return;
st.textContent=text;
enableButtons();
focusStatus(st);
}

function onAuthFail(){
if(!authed)return;
authed=false;
if(pendingAbort)try{pendingAbort.abort();}catch(e){}
pendingAbort=null;
clearLive();
clearProtected();
var st=statusEl();
if(st){
st.textContent="Session expired. ";
var a=doc.createElement("a");
a.href="/login";
a.textContent="Sign in";
st.appendChild(a);
focusStatus(st);
}
}

function onErr(e){
if(!authed)return;
var xhr=e&&e.detail&&e.detail.xhr;
var meta=xhr?reqMeta.get(xhr):null;
if(!meta||meta.gen!==gen||meta.seq!==seq){return;}
if(pendingAbort===xhr){try{pendingAbort.abort();}catch(err){}pendingAbort=null;}
var st=statusEl();
if(st){st.textContent="Request failed. Retry manually.";focusStatus(st);}
var btns=doc.querySelectorAll("button[type=submit]");
for(var i=0;i<btns.length;i++){
btns[i].disabled=false;
btns[i].removeAttribute("aria-busy");
}
}

htmx.on("htmx:configRequest",function(e){
var path=(e.detail&&e.detail.path)||"";
if(isLivePath(path)){
if(!authed){e.preventDefault();return;}
var intent=(e.detail.parameters&&e.detail.parameters.intent)||"poll";
if(intent!=="start"&&!liveRun){e.preventDefault();return;}
if(liveXhrOpen()){e.preventDefault();return;}
if(e.detail.parameters)e.detail.parameters.live_gen=String(liveGen);
return;
}
if(!authed){e.preventDefault();return;}
bump();
e.detail.parameters.generation=String(gen);
e.detail.parameters.sequence=String(seq);
});

htmx.on("htmx:beforeRequest",function(e){
var d=e.detail;
var kind="results";
var sel="";
var origin="";
var path=d.requestConfig.path||"";
if(isLivePath(path)){
var intent=(d.requestConfig.parameters&&d.requestConfig.parameters.intent)||"";
if(intent!=="start"&&!liveRun){e.preventDefault();return;}
if(liveXhrOpen()){e.preventDefault();return;}
d.xhr.__epoch=liveEpoch;
liveXhr=d.xhr;
liveXhrs.add(d.xhr);
return;
}
var st=statusEl();
if(path.indexOf("/search")!==-1&&st&&authed){
st.textContent="Searching...";
}
var region=resultsEl();
if(region&&path.indexOf("/search")!==-1){
region.setAttribute("data-stale","true");
}
if(path.indexOf("/detail")!==-1){
kind="detail";
if(d.requestConfig.parameters.id){
sel=d.requestConfig.parameters.id;
origin=sel;
}
}else{
if(d.requestConfig.parameters.origin)origin=d.requestConfig.parameters.origin;
}
reqMeta.set(d.xhr,{kind:kind,sel:sel,origin:origin,gen:gen,seq:seq});
if(pendingAbort)try{pendingAbort.abort();}catch(err){}
pendingAbort=d.xhr;
});

htmx.on("htmx:beforeSwap",function(e){
var d=e.detail;
var xhr=d.xhr;
if(xhr&&liveXhrs.has(xhr)){
if(!authed||xhr.status<200||xhr.status>=300){e.preventDefault();return;}
var liveCT=xhr.getResponseHeader("Content-Type")||"";
if(liveCT.indexOf("text/html")===-1){e.preventDefault();return;}
if(liveHeader(xhr,"X-Live-Gen")!==String(liveGen)){e.preventDefault();return;}
try{
var liveURL=new URL(xhr.responseURL);
if(liveURL.origin!==location.origin){e.preventDefault();return;}
}catch(liveErr){e.preventDefault();return;}
return;
}
if(!authed){e.preventDefault();return;}
if(xhr.status<200||xhr.status>=300){e.preventDefault();return;}
var ct=xhr.getResponseHeader("Content-Type")||"";
if(ct.indexOf("text/html")===-1){e.preventDefault();return;}
var meta=reqMeta.get(xhr);
if(!meta){e.preventDefault();return;}
var fg=xhr.getResponseHeader("X-Generation");
var fs=xhr.getResponseHeader("X-Sequence");
var fk=xhr.getResponseHeader("X-Fragment-Kind");
var fsel=xhr.getResponseHeader("X-Selection");
var fo=xhr.getResponseHeader("X-Origin");
if(fk!==meta.kind||fg!==String(meta.gen)||fs!==String(seq)){e.preventDefault();return;}
if(fsel!==meta.sel||fo!==meta.origin){e.preventDefault();return;}
try{
var u=new URL(xhr.responseURL);
if(u.origin!==location.origin){e.preventDefault();return;}
}catch(err){e.preventDefault();return;}
});

htmx.on("htmx:afterSwap",function(e){
var d=e.detail;
var liveX=d&&d.xhr;
if(liveX&&liveXhrs.has(liveX)){
if(liveX.__epoch!==liveEpoch){
if(liveXhr===liveX)liveXhr=null;
return;
}
if(liveXhr===liveX)liveXhr=null;
var liveTarget=d.target;
var liveID=liveTarget&&liveTarget.id;
if(liveID==="live-ack")onLiveAck(doc.getElementById("live-ack"));
else onLiveBatch(liveX);
return;
}
pendingAbort=null;
statusOwnsFocus=false;
var fk=d.xhr.getResponseHeader("X-Fragment-Kind");
var region=resultsEl();
var dr=doc.getElementById("detail-region");
if(fk==="detail"){
if(region){region.hidden=true;region.setAttribute("inert","");}
var h=doc.getElementById("detail-heading");
if(h)h.focus();
}else{
if(dr)dr.innerHTML="";
if(region){region.hidden=false;region.removeAttribute("inert");}
var origin=d.xhr.getResponseHeader("X-Origin");
var btn=null;
if(origin){
btn=doc.getElementById("event-"+origin+"-open");
}
if(btn){btn.focus();}
else{
var rh=doc.getElementById("results-heading");
if(rh)rh.focus();
}
}
});

htmx.on("htmx:afterRequest",function(e){
var d=e.detail;
var xhr=d.xhr;
if(xhr.status===401||xhr.status===403){onAuthFail();return;}
if(liveXhrs.has(xhr)){
if(xhr.__epoch!==liveEpoch){
if(liveXhr===xhr)liveXhr=null;
return;
}
if(liveXhr===xhr)liveXhr=null;
onLiveHTTP(xhr);
return;
}
if(xhr.status<400)return;
var meta=reqMeta.get(xhr);
if(!meta||meta.gen!==gen||meta.seq!==seq)return;
var code=String(xhr.status);
if(guidance[code]){showGuide(code);return;}
var notice="";
try{notice=xhr.getResponseHeader("X-Notice")||"";}catch(err){notice="";}
if(notices[notice]){showNotice(notices[notice]);return;}
onErr(e);
});

htmx.on("htmx:sendError",function(e){
if(liveTransport(e))return;
onErr(e);
});
htmx.on("htmx:timeout",function(e){
if(liveTransport(e))return;
if(!authed)return;
var xhr=e&&e.detail&&e.detail.xhr;
var meta=xhr?reqMeta.get(xhr):null;
if(!meta||meta.gen!==gen||meta.seq!==seq)return;
if(pendingAbort===xhr){try{pendingAbort.abort();}catch(err){}pendingAbort=null;}
showGuide("504");
});

doc.addEventListener("click",function(e){
var t=e.target;
if(!t||!t.id)return;
if(t.id==="btn-live-pause"){pauseLive();return;}
if(t.id==="btn-live-resume"||t.id==="btn-live-reconnect"){resumeLive(t);return;}
if(t.id==="btn-search-gap"){searchGap(e);return;}
if(t.id!=="btn-copy")return;
var pre=doc.getElementById("redacted-json");
var note=doc.getElementById("status");
function say(text){
if(!note)return;
note.textContent=text;
focusStatus(note);
}
if(!pre||!navigator.clipboard||!navigator.clipboard.writeText){
say("Copy unavailable; select JSON below");
return;
}
navigator.clipboard.writeText(pre.textContent).then(function(){
say("JSON copied");
},function(){
say("Copy unavailable; select JSON below");
});
});

window.addEventListener("pagehide",function(){
liveStop();
clearLive();
clearProtected();
if(pendingAbort)try{pendingAbort.abort();}catch(e){}
pendingAbort=null;
});

window.addEventListener("pageshow",function(e){
if(e.persisted){
window.location.reload();
}
});

doc.addEventListener("submit",function(e){
var btn=e.submitter;
if(!btn||btn.tagName!=="BUTTON")return;
setTimeout(function(){
if(!btn.isConnected)return;
btn.disabled=true;
btn.setAttribute("aria-busy","true");
},0);
},true);

doc.addEventListener("reset",function(e){
var f=e.target;
if(!f||f.tagName!=="FORM"||!f.querySelectorAll)return;
var btns=f.querySelectorAll("button[type=submit]");
for(var i=0;i<btns.length;i++){
btns[i].disabled=false;
btns[i].removeAttribute("aria-busy");
}
});

htmx.on("htmx:afterSettle",function(){
var btns=doc.querySelectorAll("button[type=submit]");
for(var i=0;i<btns.length;i++){
btns[i].disabled=false;
btns[i].removeAttribute("aria-busy");
}
if(!statusOwnsFocus)return;
var st=statusEl();
if(st)st.focus();
});

var liveRun=false;
var liveWant=false;
var liveHold="";
var liveMs=1000;
var liveGen=1;
var liveIntentOnce="";
var liveXhr=null;
var liveXhrs=new WeakSet();
var liveEpoch=0;
var liveGapFrom="";
var liveGapTo="";
var liveRetryTimer=0;

function isLivePath(path){return !!path&&path.indexOf("/investigate/live")!==-1;}
function liveHeader(xhr,name){
if(!xhr||!xhr.getResponseHeader)return "";
try{return xhr.getResponseHeader(name)||"";}catch(err){return "";}
}
function setLiveStatus(text,state){
var st=doc.getElementById("live-status");
if(!st)return;
st.textContent=text;
if(state)st.setAttribute("data-state",state);
}
function showEl(id,on){
var el=doc.getElementById(id);
if(!el)return;
el.hidden=!on;
}
function syncLiveButtons(){
var user=liveHold==="user";
var bad=liveHold==="error"||liveHold==="gap"||liveHold==="throttled"||liveHold==="invalid";
showEl("btn-live-pause",liveWant&&!user&&!bad);
showEl("btn-live-resume",user);
showEl("btn-live-reconnect",liveHold==="error"||liveHold==="throttled");
showEl("btn-search-gap",liveHold==="gap");
showEl("btn-live-start",!liveWant);
}
function randomClient(){
var hex="";
var cryptoObj=(typeof crypto!=="undefined")?crypto:null;
if(cryptoObj&&cryptoObj.getRandomValues&&typeof Uint8Array==="function"){
var bytes=new Uint8Array(8);
cryptoObj.getRandomValues(bytes);
for(var i=0;i<bytes.length;i++){
var h=bytes[i].toString(16);
if(h.length<2)h="0"+h;
hex+=h;
}
return hex;
}
return "liveclient0000001";
}
// A duplicated tab copies sessionStorage, so both tabs share this client id.
// The server then throttles the second tab.
function initLiveClient(){
var el=doc.getElementById("live-client");
if(!el)return;
var saved="";
try{
if(typeof sessionStorage!=="undefined"&&sessionStorage)saved=sessionStorage.getItem("hl_live_client")||"";
}catch(err){saved="";}
if(/^[a-z0-9]{8,32}$/.test(saved))el.value=saved;
else if(!el.value)el.value=randomClient();
try{
if(typeof sessionStorage!=="undefined"&&sessionStorage&&el.value)sessionStorage.setItem("hl_live_client",el.value);
}catch(err){}
}
function applyAsOf(v){
if(v===null||v===undefined||v==="")return;
var el=doc.getElementById("live-as-of");
if(el)el.value=String(v);
}
function kickLive(){
if(!liveWant||liveHold||liveXhrOpen())return;
var el=doc.getElementById("live-trigger");
if(!el||typeof htmx==="undefined"||!htmx.trigger)return;
htmx.trigger(el,"live-now");
}
function liveStop(){
var client=doc.getElementById("live-client");
var csrf=doc.querySelector('#live-form input[name="csrf"]');
if(!client||!client.value||!csrf||!csrf.value)return;
var body="csrf="+encodeURIComponent(csrf.value)+"&intent=stop&client="+encodeURIComponent(client.value)+"&live_gen=1&cursor_received=0&cursor_id=0&severity=1";
try{
if(typeof fetch==="function"){
fetch("/investigate/live",{method:"POST",headers:{"Content-Type":"application/x-www-form-urlencoded"},body:body,keepalive:true,credentials:"same-origin"});
}
}catch(err){}
}
function scrollHeld(){
var rows=doc.getElementById("live-rows");
return !!(rows&&rows.scrollTop>1);
}
function hiddenHeld(){return doc.visibilityState==="hidden";}
function unmountTrigger(){
liveEpoch++;
liveRun=false;
var el=doc.getElementById("live-trigger");
if(el){
if(typeof htmx!=="undefined"&&htmx.remove)htmx.remove(el);
else if(el.remove)el.remove();
}
if(liveXhr){
var xhr=liveXhr;
liveXhr=null;
try{xhr.abort();}catch(err){}
}
}
function mountTrigger(){
if(!liveWant||liveHold)return;
var panel=doc.getElementById("live-panel");
if(!panel)return;
var every=String(liveMs);
var old=doc.getElementById("live-trigger");
if(old&&old.getAttribute("data-every")===every){liveRun=true;return;}
if(old){
if(htmx.remove)htmx.remove(old);
else if(old.remove)old.remove();
}
var el=doc.createElement("div");
el.id="live-trigger";
el.setAttribute("hx-post","/investigate/live");
el.setAttribute("hx-trigger","every "+every+"ms, live-now");
el.setAttribute("data-every",every);
el.setAttribute("hx-target","#live-incoming");
el.setAttribute("hx-swap","innerHTML");
el.setAttribute("hx-sync","#live-trigger:drop");
el.setAttribute("hx-include","#live-form");
var intent=liveIntentOnce||"poll";
liveIntentOnce="";
el.setAttribute("hx-vals","{\"intent\":\""+intent+"\"}");
el.hidden=true;
panel.appendChild(el);
liveRun=true;
if(htmx.process)htmx.process(el);
}
function freezeLiveFilters(){
var form=doc.getElementById("live-form");
var filters=doc.getElementById("filters");
if(!form||!filters||!form.querySelectorAll)return;
var old=form.querySelectorAll(".live-filter");
for(var i=old.length-1;i>=0;i--){if(old[i].remove)old[i].remove();}
function add(name,value){
var input=doc.createElement("input");
input.type="hidden";
input.name=name;
input.value=value;
input.className="live-filter";
form.appendChild(input);
}
var sel=doc.getElementById("source-select");
if(sel&&sel.options){
for(var s=0;s<sel.options.length;s++){
if(sel.options[s].selected)add("source",sel.options[s].value);
}
}
var sev=doc.getElementById("severity-select");
var sevInput=doc.getElementById("live-severity");
if(sev&&sevInput)sevInput.value=sev.value||"1";
var terms=filters.querySelectorAll("input[name=term]");
for(var t=0;t<terms.length;t++){if(terms[t].value)add("term",terms[t].value);}
var paths=filters.querySelectorAll("input[name=path]");
var ops=filters.querySelectorAll("select[name=op]");
var types=filters.querySelectorAll("select[name=ptype]");
var vals=filters.querySelectorAll("input[name=pval]");
for(var p=0;p<paths.length;p++){
if(!paths[p].value)continue;
add("path",paths[p].value);
add("op",ops[p]?ops[p].value:"eq");
add("ptype",types[p]?types[p].value:"string");
add("pval",vals[p]?vals[p].value:"");
}
}
function bumpLiveGen(){
liveGen=Math.min(liveGen+1,1000000000);
var g=doc.getElementById("live-gen");
if(g)g.value=String(liveGen);
}
function applyCursor(recv,id){
if(recv===null||recv===undefined||recv==="")return;
var a=doc.getElementById("live-cursor-received");
var b=doc.getElementById("live-cursor-id");
if(a)a.value=String(recv);
if(b&&id!==null&&id!==undefined&&id!=="")b.value=String(id);
}
function mergeLive(root){
var list=doc.getElementById("live-rows");
if(!list||!root||!root.querySelectorAll)return 0;
var items=root.querySelectorAll("li");
var added=0;
for(var i=0;i<items.length;i++){
var id=items[i].getAttribute("data-ingest-id");
if(!id||!/^[0-9]+$/.test(id))continue;
if(list.querySelector('li[data-ingest-id="'+id+'"]'))continue;
if(list.insertBefore)list.insertBefore(items[i],list.firstChild);
else list.appendChild(items[i]);
added++;
}
while(list.children&&list.children.length>1000)list.removeChild(list.lastChild);
return added;
}
function clearLive(){
liveWant=false;
liveHold="";
unmountTrigger();
var rows=doc.getElementById("live-rows");
if(rows){
if(rows.textContent!==undefined)rows.textContent="";
rows.innerHTML="";
}
setLiveStatus("","off");
syncLiveButtons();
}
function stopLiveTerminal(hold,text,state){
liveWant=false;
liveHold=hold;
unmountTrigger();
setLiveStatus(text,state);
syncLiveButtons();
}
function onLiveHTTP(xhr){
if(!xhr)return;
var status=xhr.status;
if(status>=200&&status<300)return;
if(status===410){
liveGapFrom=liveHeader(xhr,"X-Live-Gap-From");
liveGapTo=liveHeader(xhr,"X-Live-Gap-To");
stopLiveTerminal("gap","Live: Gap detected","gap");
return;
}
if(status===429||status===503){
liveWant=false;
liveHold="throttled";
unmountTrigger();
setLiveStatus("Live: Throttled","throttled");
syncLiveButtons();
var btn=doc.getElementById("btn-live-reconnect");
var sec=parseInt(liveHeader(xhr,"Retry-After"),10);
if(!(sec>=0&&sec<=86400))sec=5;
if(btn){
btn.disabled=true;
if(liveRetryTimer)clearTimeout(liveRetryTimer);
liveRetryTimer=setTimeout(function(){
var b=doc.getElementById("btn-live-reconnect");
if(b)b.disabled=false;
},sec*1000);
}
return;
}
if(status===400||status===422){
stopLiveTerminal("invalid","Live: Query invalid","invalid");
var src=doc.getElementById("source-select");
if(src&&src.focus)src.focus();
return;
}
if(status===504||status>=500){
stopLiveTerminal("error","Live: Disconnected","disconnected");
return;
}
stopLiveTerminal("error","Live: Error","error");
}
function onLiveAck(ack){
if(!ack)return;
setLiveStatus(ack.textContent||"Live: Receiving",ack.getAttribute("data-state")||"receiving");
applyCursor(ack.getAttribute("data-cursor-received"),ack.getAttribute("data-cursor-id"));
applyAsOf(ack.getAttribute("data-as-of"));
liveWant=true;
liveHold="";
if(hiddenHeld())liveHold="hidden";
else if(scrollHeld())liveHold="scroll";
if(liveHold)setLiveStatus("Live: Paused","paused");
liveMs=1000;
syncLiveButtons();
mountTrigger();
}
function onLiveBatch(xhr){
var incoming=doc.getElementById("live-incoming");
var root=incoming;
if(incoming&&incoming.querySelector){
var batch=incoming.querySelector("#live-batch");
if(batch)root=batch;
}
var added=mergeLive(root);
var recv=liveHeader(xhr,"X-Live-Cursor-Received");
var id=liveHeader(xhr,"X-Live-Cursor-Id");
if(!recv&&root&&root.getAttribute){
recv=root.getAttribute("data-cursor-received")||"";
id=root.getAttribute("data-cursor-id")||"";
}
applyCursor(recv,id);
var asOf=liveHeader(xhr,"X-Live-As-Of");
if(!asOf&&root&&root.getAttribute)asOf=root.getAttribute("data-as-of")||"";
applyAsOf(asOf);
var idle=liveHeader(xhr,"X-Live-Idle")==="1";
if(!liveHeader(xhr,"X-Live-Idle")&&root&&root.getAttribute)idle=root.getAttribute("data-idle")==="1";
var caughtHdr=liveHeader(xhr,"X-Live-Caught");
if(!caughtHdr&&root&&root.getAttribute)caughtHdr=root.getAttribute("data-caught")||"";
var caught=caughtHdr==="1"||(caughtHdr===""&&idle);
if(incoming)incoming.innerHTML="";
if(liveWant&&!liveHold){
if(!caught){
liveMs=1000;
mountTrigger();
kickLive();
}else{
if(added>0)liveMs=1000;
else if(idle)liveMs=Math.min(liveMs*2,2000);
mountTrigger();
}
}
}
function liveXhrOpen(){
if(!liveXhr)return false;
if(liveXhr.__epoch!==liveEpoch||liveXhr.readyState===4){
liveXhr=null;
return false;
}
return true;
}
function liveTransport(e){
var xhr=e&&e.detail&&e.detail.xhr;
if(!xhr||!liveXhrs.has(xhr))return false;
if(xhr.__epoch!==liveEpoch){
if(liveXhr===xhr)liveXhr=null;
return true;
}
if(liveXhr===xhr)liveXhr=null;
if(!liveWant||liveHold)return true;
stopLiveTerminal("error","Live: Disconnected","disconnected");
return true;
}
function pauseLive(){
if(!liveWant)return;
liveHold="user";
setLiveStatus("Live: Paused","paused");
unmountTrigger();
syncLiveButtons();
}
function resumeLive(t){
if(t&&t.disabled)return;
liveWant=true;
liveHold="";
unmountTrigger();
liveIntentOnce="resume";
liveMs=1000;
bumpLiveGen();
setLiveStatus("Live: Receiving","receiving");
syncLiveButtons();
mountTrigger();
var pause=doc.getElementById("btn-live-pause");
if(pause&&pause.focus)pause.focus();
}
function searchGap(e){
if(e&&e.preventDefault)e.preventDefault();
var from=doc.getElementById("f-from");
var to=doc.getElementById("f-to");
var preset=doc.getElementById("time-select");
if(from&&liveGapFrom)from.value=liveGapFrom;
if(to&&liveGapTo)to.value=liveGapTo;
if(preset)preset.value="custom";
var src=doc.getElementById("source-select");
if(src&&src.focus)src.focus();
}
function onLiveScroll(){
if(!liveWant||liveHold==="user"||liveHold==="gap"||liveHold==="error"||liveHold==="throttled"||liveHold==="invalid")return;
if(scrollHeld()){
if(!liveHold){
liveHold="scroll";
setLiveStatus("Live: Paused","paused");
unmountTrigger();
syncLiveButtons();
}
return;
}
if(liveHold==="scroll"){
liveHold="";
liveIntentOnce="resume";
liveMs=1000;
setLiveStatus("Live: Receiving","receiving");
syncLiveButtons();
mountTrigger();
}
}
doc.addEventListener("click",function(e){
var t=e.target;
if(!t||t.id!=="btn-live-start")return;
freezeLiveFilters();
bumpLiveGen();
liveWant=true;
liveRun=true;
liveHold="";
liveMs=1000;
},true);
doc.addEventListener("visibilitychange",function(){
if(!liveWant||liveHold==="user"||liveHold==="gap"||liveHold==="error"||liveHold==="throttled"||liveHold==="invalid")return;
if(hiddenHeld()){
liveHold="hidden";
setLiveStatus("Live: Paused","paused");
unmountTrigger();
syncLiveButtons();
return;
}
if(liveHold==="hidden"){
if(scrollHeld()){liveHold="scroll";return;}
liveHold="";
liveIntentOnce="resume";
liveMs=1000;
setLiveStatus("Live: Receiving","receiving");
syncLiveButtons();
mountTrigger();
}
});
doc.addEventListener("scroll",function(e){
var t=e.target;
if(!t||t.id!=="live-rows")return;
onLiveScroll();
},true);
initLiveClient();
})();