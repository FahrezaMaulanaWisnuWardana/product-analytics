const {test}=require('node:test');
const assert=require('node:assert/strict');
const fs=require('node:fs');
const vm=require('node:vm');

const source=fs.readFileSync(__dirname+'/app.js','utf8');
const decisionSource=source.slice(source.indexOf('function decisionBrief'),source.indexOf('\nfunction spark'));

function renderDecision(data,lang='en'){
 const sandbox={data,lang,result:'',Intl};
 vm.runInNewContext(`
  const state={lang};
  const labels={en:{collecting:'Collecting evidence',directional:'Directional signal',ready:'Stronger signal',decision_brief:'DECISION BRIEF',sample_readiness:'Sample readiness',active_coverage:'Active coverage',top_event_share:'Top-event share',period_comparison:'Period comparison',what_means:'What it means',next_action:'Recommended next action',no_baseline:'No prior baseline',events:'Events',inspect:'Inspect',inspect_stream:'Inspect event stream',heuristic:'A conservative heuristic based on unique people and active days; not a statistical significance test',pageview:'Page viewed',identify:'User identified',new_value:'New'},id:{collecting:'Mengumpulkan bukti',directional:'Sinyal awal',ready:'Sinyal lebih kuat',decision_brief:'RINGKASAN KEPUTUSAN',sample_readiness:'Kesiapan sampel',active_coverage:'Cakupan aktif',top_event_share:'Porsi event teratas',period_comparison:'Perbandingan periode',what_means:'Artinya',next_action:'Tindakan berikutnya',no_baseline:'Belum ada data pembanding',events:'Event',inspect:'Periksa',inspect_stream:'Periksa aliran event',heuristic:'Heuristik konservatif berdasarkan pengguna unik dan hari aktif; bukan uji signifikansi statistik',pageview:'Halaman dilihat',identify:'Pengguna dikenali',new_value:'Baru'}};
  const t=key=>labels[state.lang][key];
  const esc=v=>String(v??'').replace(/[&<>"']/g,c=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
  const fmt=n=>new Intl.NumberFormat(state.lang==='id'?'id-ID':'en-US').format(n||0);
  const pretty=n=>n==='$pageview'?t('pageview'):n==='$identify'?t('identify'):n.replace(/_/g,' ').replace(/^./,c=>c.toUpperCase());
  const percentage=(value,previous)=>{if(!previous)return value?t('new_value'):'—';const p=(value-previous)/previous*100;return (p>=0?'↗':'↘')+' '+Math.abs(p).toFixed(1)+'%'};
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

test('decision brief renders decision guidance in Indonesian',()=>{
 const html=renderDecision({
  metrics:{users:8,events:90},previous:{events:0},
  decision:{readiness:'collecting',active_days:2,top_event:{name:'feature_used',count:50},top_event_share:55.6,top_person_share:10},
  top_events:[{name:'feature_used',count:50}],series:Array.from({length:30},()=>({events:1}))
 },'id');
 assert.match(html,/RINGKASAN KEPUTUSAN/);
 assert.match(html,/Gunakan periode ini sebagai sinyal awal/);
 assert.match(html,/Kesiapan sampel/);
 assert.match(html,/Periksa Feature used/);
});
