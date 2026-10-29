const {test}=require('node:test');
const assert=require('node:assert/strict');
const fs=require('node:fs');
const vm=require('node:vm');
function environment(dnt='0'){
 const storage=new Map(),requests=[];
 const localStorage={getItem:k=>storage.get(k)||null,setItem:(k,v)=>storage.set(k,v),removeItem:k=>storage.delete(k)};
 const listeners={};let serial=0;
 const sandbox={localStorage,sessionStorage:localStorage,location:{origin:'https://app.example',pathname:'/dashboard',host:'app.example'},document:{referrer:'https://search.example/?secret=private'},navigator:{userAgent:'Chrome/140',doNotTrack:dnt,sendBeacon:()=>false},crypto:{randomUUID:()=>`id-${++serial}`},setInterval:()=>1,URL,Blob,console,addEventListener:(n,f)=>listeners[n]=f,fetch:async(url,req)=>{requests.push({url,body:JSON.parse(req.body)});return {ok:true,status:202}}};
 sandbox.window=sandbox;vm.runInNewContext(fs.readFileSync(__dirname+'/sdk.js','utf8'),sandbox);
 return {productAnalytics:sandbox.productAnalytics,requests,storage,sandbox};
}
test('tracks a page, identifies future activity, excludes URL query and referrer details',async()=>{
 const e=environment();e.productAnalytics.init('pk_test',{host:'https://analytics.example/'});e.productAnalytics.identify('u1',{plan:'Pro'});e.productAnalytics.capture('project_created');await e.productAnalytics.flush();
 assert.equal(e.requests.length,1);assert.equal(e.requests[0].url,'https://analytics.example/capture');
 const events=e.requests[0].body.events;assert.equal(events.length,3);assert.equal(events[0].event,'$pageview');assert.equal(events[0].distinct_id,'id-1');assert.equal(events[1].distinct_id,'u1');assert.equal(events[2].distinct_id,'u1');assert.equal(events[0].properties.$referrer,'https://search.example');assert.equal(events[0].properties.$pathname,'/dashboard');
});
test('respects Do Not Track and opt out clears queued activity',async()=>{
 const dnt=environment('1');dnt.productAnalytics.init('key');dnt.productAnalytics.capture('click');await dnt.productAnalytics.flush();assert.equal(dnt.requests.length,0);
 const e=environment();e.productAnalytics.init('key');e.productAnalytics.capture('click');e.productAnalytics.optOut();await e.productAnalytics.flush();assert.equal(e.requests.length,0);e.productAnalytics.optIn();e.productAnalytics.capture('consented');await e.productAnalytics.flush();assert.deepEqual(e.requests[0].body.events.map(e=>e.event),['consented']);
});
test('retries network errors using stable event IDs and resets user identity',async()=>{
 const e=environment();let attempts=0,firstID;e.sandbox.fetch=async(url,req)=>{const event=JSON.parse(req.body).events[0];if(attempts++===0){firstID=event.id;throw Error('offline')}assert.equal(event.id,firstID);return {ok:true,status:202}};
 e.productAnalytics.init('key');await e.productAnalytics.flush();await e.productAnalytics.flush();assert.equal(attempts,2);
 const f=environment();f.productAnalytics.init('key',{pageview:false});f.productAnalytics.identify('signed-in');f.productAnalytics.reset();f.productAnalytics.capture('signed-out');await f.productAnalytics.flush();const events=f.requests[0].body.events;assert.equal(events[0].distinct_id,'signed-in');assert.notEqual(events[1].distinct_id,'signed-in');assert.notEqual(events[0].session_id,events[1].session_id);
});
