(function(){
"use strict";
var doc=document;
var main=doc.getElementById("investigation");
var results=doc.getElementById("results-region");
var status=doc.getElementById("status");
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

var guidance={
"422":"Query too broad. Narrow the time window, then run Search.",
"410":"Results expired. Rerun query.",
"504":"Query timed out. Narrow the time window, then run Search.",
"503":"Service busy. Wait, then run Search again."
};

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
if(results)results.innerHTML="";
var d=doc.getElementById("detail-region");
if(d)d.innerHTML="";
if(status)status.textContent="";
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
if(!authed||!status)return;
status.textContent=guidance[code];
if(code==="410"){
var cursors=doc.querySelectorAll('input[name="cursor"]');
for(var i=0;i<cursors.length;i++)cursors[i].value="";
if(results)results.textContent="";
}
if((code==="504"||code==="422"||code==="503")&&results){
results.setAttribute("data-stale","true");
}
enableButtons();
status.focus();
}

function onAuthFail(){
if(!authed)return;
authed=false;
if(pendingAbort)try{pendingAbort.abort();}catch(e){}
pendingAbort=null;
clearProtected();
if(status){
status.textContent="Session expired. ";
var a=doc.createElement("a");
a.href="/login";
a.textContent="Sign in";
status.appendChild(a);
status.focus();
}
}

function onErr(e){
if(!authed)return;
var xhr=e&&e.detail&&e.detail.xhr;
var meta=xhr?reqMeta.get(xhr):null;
if(!meta||meta.gen!==gen||meta.seq!==seq){return;}
if(pendingAbort===xhr){try{pendingAbort.abort();}catch(err){}pendingAbort=null;}
if(status){status.textContent="Request failed. Retry manually.";status.focus();}
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
if(path.indexOf("/search")!==-1&&status&&authed){
status.textContent="Searching...";
}
if(results&&path.indexOf("/search")!==-1){
results.setAttribute("data-stale","true");
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
var d=e.detail;
var fk=d.xhr.getResponseHeader("X-Fragment-Kind");
results=doc.getElementById("results-region");
var dr=doc.getElementById("detail-region");
if(fk==="detail"){
if(results){results.hidden=true;results.setAttribute("inert","");}
var h=doc.getElementById("detail-heading");
if(h)h.focus();
}else{
if(dr)dr.innerHTML="";
if(results){results.hidden=false;results.removeAttribute("inert");}
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
if(d.xhr.status===401||d.xhr.status===403){onAuthFail();return;}
if(guidance[String(d.xhr.status)]){
var xhr=d.xhr;
var meta=xhr?reqMeta.get(xhr):null;
if(!meta||meta.gen!==gen||meta.seq!==seq)return;
showGuide(String(d.xhr.status));
return;
}
if(d.xhr.status>=400){onErr(e);return;}
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
note.focus();
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
var f=e.target;
if(f&&f.tagName==="FORM"){
var btn=f.querySelector("button[type=submit]");
if(btn){btn.disabled=true;btn.setAttribute("aria-busy","true");}
}
},true);

doc.addEventListener("reset",function(e){
var f=e.target;
if(f&&f.tagName==="FORM"){
var btn=f.querySelector("button[type=submit]");
if(btn){btn.disabled=false;btn.removeAttribute("aria-busy");}
}
});

htmx.on("htmx:afterSettle",function(){
var btns=doc.querySelectorAll("button[type=submit]");
for(var i=0;i<btns.length;i++){
btns[i].disabled=false;
btns[i].removeAttribute("aria-busy");
}
});
})();