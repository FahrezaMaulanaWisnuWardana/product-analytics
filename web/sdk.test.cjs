const {test}=require('node:test');
const assert=require('node:assert/strict');
const fs=require('node:fs');
const vm=require('node:vm');
function environment(dnt='0',initialStorage={}){
 const storage=new Map(Object.entries(initialStorage)),requests=[];
 const localStorage={getItem:k=>storage.get(k)||null,setItem:(k,v)=>storage.set(k,v),removeItem:k=>storage.delete(k)};
 const listeners={};let serial=0;
 const sandbox={localStorage,sessionStorage:localStorage,location:{origin:'https://app.example',pathname:'/dashboard',host:'app.example'},document:{referrer:'https://search.example/?secret=private'},navigator:{userAgent:'Chrome/140',doNotTrack:dnt,sendBeacon:()=>false},crypto:{randomUUID:()=>`id-${++serial}`},setInterval:()=>1,URL,Blob,console,addEventListener:(n,f)=>listeners[n]=f,fetch:async(url,req)=>{requests.push({url,body:JSON.parse(req.body)});return {ok:true,status:202}}};
 sandbox.window=sandbox;vm.runInNewContext(fs.readFileSync(__dirname+'/sdk.js','utf8'),sandbox);
 return {pulse:sandbox.pulse,requests,storage,sandbox};
}
test('tracks a page, identifies future activity, excludes URL query and referrer details',async()=>{
 const e=environment();e.pulse.init('pk_test',{host:'https://analytics.example/'});e.pulse.identify('u1',{plan:'Pro'});e.pulse.capture('project_created');await e.pulse.flush();
 assert.equal(e.requests.length,1);assert.equal(e.requests[0].url,'https://analytics.example/capture');
 const events=e.requests[0].body.events;assert.equal(events.length,3);assert.equal(events[0].event,'$pageview');assert.equal(events[0].distinct_id,'id-1');assert.equal(events[1].distinct_id,'u1');assert.equal(events[2].distinct_id,'u1');assert.equal(events[0].properties.$referrer,'https://search.example');assert.equal(events[0].properties.$pathname,'/dashboard');
});
test('preserves the distinct ID stored by the pre-Pulse SDK',async()=>{
 const e=environment('0',{'productAnalytics:pk_legacy:id':'legacy-user'});e.pulse.init('pk_legacy',{pageview:false});e.pulse.capture('continued');await e.pulse.flush();
 assert.equal(e.requests[0].body.events[0].distinct_id,'legacy-user');assert.equal(e.storage.get('pulse:pk_legacy:id'),'legacy-user');
});
test('respects Do Not Track and opt out clears queued activity',async()=>{
 const dnt=environment('1');dnt.pulse.init('key');dnt.pulse.capture('click');await dnt.pulse.flush();assert.equal(dnt.requests.length,0);
 const e=environment();e.pulse.init('key');e.pulse.capture('click');e.pulse.optOut();await e.pulse.flush();assert.equal(e.requests.length,0);e.pulse.optIn();e.pulse.capture('consented');await e.pulse.flush();assert.deepEqual(e.requests[0].body.events.map(e=>e.event),['consented']);
});
test('retries network errors using stable event IDs and resets user identity',async()=>{
 const e=environment();let attempts=0,firstID;e.sandbox.fetch=async(url,req)=>{const event=JSON.parse(req.body).events[0];if(attempts++===0){firstID=event.id;throw Error('offline')}assert.equal(event.id,firstID);return {ok:true,status:202}};
 e.pulse.init('key');await e.pulse.flush();await e.pulse.flush();assert.equal(attempts,2);
 const f=environment();f.pulse.init('key',{pageview:false});f.pulse.identify('signed-in');f.pulse.reset();f.pulse.capture('signed-out');await f.pulse.flush();const events=f.requests[0].body.events;assert.equal(events[0].distinct_id,'signed-in');assert.notEqual(events[1].distinct_id,'signed-in');assert.notEqual(events[0].session_id,events[1].session_id);
});
