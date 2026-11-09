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
if(!authed){e.preventDefault();return;}
var d=e.detail;
var xhr=d.xhr;
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
pendingAbort=null;
statusOwnsFocus=false;
var d=e.detail;
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

htmx.on("htmx:sendError",function(e){onErr(e);});
htmx.on("htmx:timeout",function(e){
if(!authed)return;
var xhr=e&&e.detail&&e.detail.xhr;
var meta=xhr?reqMeta.get(xhr):null;
if(!meta||meta.gen!==gen||meta.seq!==seq)return;
if(pendingAbort===xhr){try{pendingAbort.abort();}catch(err){}pendingAbort=null;}
showGuide("504");
});

doc.addEventListener("click",function(e){
var t=e.target;
if(!t||t.id!=="btn-copy")return;
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
})();