const {test}=require('node:test');
const assert=require('node:assert/strict');
const fs=require('node:fs');
const vm=require('node:vm');

const source=fs.readFileSync(__dirname+'/app.js','utf8');
const decisionSource=source.slice(source.indexOf('function decisionBrief'),source.indexOf('\nfunction spark'));

function renderDecision(data){
 const sandbox={data,result:'',Intl};
 vm.runInNewContext(`
  const esc=v=>String(v??'').replace(/[&<>"']/g,c=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
  const fmt=n=>new Intl.NumberFormat('en-US').format(n||0);
  const pretty=n=>n==='$pageview'?'Page viewed':n==='$identify'?'User identified':n.replace(/_/g,' ').replace(/^./,c=>c.toUpperCase());
  const percentage=(value,previous)=>{if(!previous)return value?'New':'—';const p=(value-previous)/previous*100;return (p>=0?'↗':'↘')+' '+Math.abs(p).toFixed(1)+'%'};
  ${decisionSource}
  result=decisionBrief(data);
 `,sandbox);
 return sandbox.result;
}

test('decision brief warns when the sample is narrow and links to the dominant event',()=>{
 const html=renderDecision({
  metrics:{users:5,events:201},previous:{events:0},
  decision:{readiness:'collecting',active_days:1,top_event:{name:'detail_viewed',count:115,users:1},top_event_share:57.2,top_person_share:58},
  top_events:[{name:'detail_viewed',count:115,users:1}],series:Array.from({length:30},()=>({events:0}))
 });
 assert.match(html,/Treat this period as an early signal/);
 assert.match(html,/5 people have been observed across 1 active day/);
 assert.match(html,/One person generated 58% of activity/);
 assert.match(html,/data-filter-event="detail_viewed"/);
});

test('decision brief compares a broader sample with its previous period',()=>{
 const html=renderDecision({
  metrics:{users:40,events:120},previous:{events:100},
  decision:{readiness:'ready',active_days:12,top_event:{name:'feature_used',count:30,users:20},top_event_share:25,top_person_share:5},
  top_events:[{name:'feature_used',count:30,users:20}],series:Array.from({length:30},()=>({events:4}))
 });
 assert.match(html,/Activity is 20% higher than the previous period/);
 assert.match(html,/Stronger signal/);
 assert.match(html,/not statistical proof/);
});
