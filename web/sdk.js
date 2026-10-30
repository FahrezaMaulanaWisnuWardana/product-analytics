/* Pulse browser SDK. No dependencies. No autocapture or cookies. */
(function(global){
 'use strict';
 var config,queue=[],timer,flushing=false,optedOut=false,memoryID,memorySession;
 function read(key){try{return localStorage.getItem(key)}catch(e){return null}}
 function write(key,value){try{localStorage.setItem(key,value)}catch(e){}}
 function uuid(){return global.crypto&&crypto.randomUUID?crypto.randomUUID():'p_'+Date.now().toString(36)+'_'+Math.random().toString(36).slice(2)}
 function browser(){var u=navigator.userAgent;return /Edg\//.test(u)?'Edge':/Firefox\//.test(u)?'Firefox':/Chrome\//.test(u)?'Chrome':/Safari\//.test(u)?'Safari':'Other'}
 function storageKey(){return 'pulse:'+config.key}
 function legacyStorageKey(){return 'productAnalytics:'+config.key}
 function stored(suffix){return read(storageKey()+suffix)||read(legacyStorageKey()+suffix)}
 function session(){var key=storageKey()+':session',s;try{s=JSON.parse(sessionStorage.getItem(key)||sessionStorage.getItem(legacyStorageKey()+':session')||'null')}catch(e){};if(!s)s=memorySession;if(!s||Date.now()-s.at>1800000)s={id:uuid(),at:0};s.at=Date.now();memorySession=s;try{sessionStorage.setItem(key,JSON.stringify(s))}catch(e){};return s.id}
 function isDisabled(){return optedOut||stored(':optout')==='true'||(config.respectDNT!==false&&navigator.doNotTrack==='1')}
 function flush(){if(!config||!queue.length||flushing||isDisabled())return Promise.resolve();flushing=true;var batch=queue.splice(0,100);return fetch(config.host+'/capture',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({api_key:config.key,events:batch}),keepalive:true}).then(function(r){if(!r.ok){if(r.status===429||r.status>=500)throw new Error('Retry');console.warn('Pulse rejected events:',r.status)}}).catch(function(){queue=batch.concat(queue).slice(-500)}).finally(function(){flushing=false})}
 function capture(name,properties){if(!config)throw new Error('Call pulse.init first');if(isDisabled())return;var id=read(storageKey()+':id')||memoryID;queue.push({id:uuid(),event:name,distinct_id:id,session_id:session(),timestamp:new Date().toISOString(),properties:Object.assign({$pathname:location.pathname,$host:location.host,$referrer:document.referrer?new URL(document.referrer).origin:'',$browser:browser(),$device:/Mobi|Android/i.test(navigator.userAgent)?'Mobile':'Desktop'},properties||{})});if(queue.length>500)queue.shift();if(queue.length>=20)flush()}
 var sdk={init:function(key,options){if(config){console.warn('Pulse is already initialized');return}config=Object.assign({key:key,host:location.origin,respectDNT:true,pageview:true},options||{});config.host=config.host.replace(/\/$/,'');memoryID=stored(':id')||uuid();write(storageKey()+':id',memoryID);timer=setInterval(flush,5000);if(config.pageview)capture('$pageview');return sdk},capture:capture,identify:function(id,properties){if(!config||!id)return;memoryID=String(id);write(storageKey()+':id',memoryID);capture('$identify',properties)},reset:function(){if(!config)return;memoryID=uuid();write(storageKey()+':id',memoryID);memorySession=null;try{sessionStorage.removeItem(storageKey()+':session')}catch(e){}},page:function(properties){capture('$pageview',properties)},flush:flush,optOut:function(){if(!config)return;optedOut=true;queue=[];write(storageKey()+':optout','true')},optIn:function(){if(!config)return;optedOut=false;write(storageKey()+':optout','false')}};
 global.addEventListener('pagehide',function(){if(!config||!queue.length||isDisabled())return;var data=JSON.stringify({api_key:config.key,events:queue.slice(0,100)});if(navigator.sendBeacon(config.host+'/capture',new Blob([data],{type:'text/plain'})))queue.splice(0,100)});
 global.pulse=sdk;
})(window);
