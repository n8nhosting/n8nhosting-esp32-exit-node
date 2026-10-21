const test=require('node:test'),assert=require('node:assert/strict'),fs=require('node:fs/promises'),os=require('node:os'),path=require('node:path');
const {nodeSample,safeURL,runRecorder}=require('./hardware_soak.cjs');
test('allowlist excludes credentials, notes, names and unknown RSSI',()=>{
  const sample=nodeSample({mac:'aa:bb:cc:dd:ee:01',online:true,name:'private',ssid:'private',wifi_password:'secret',users:['customer'],notes:'private',firmware:'2.4.25-c3',stats:{heap_free:120000,cpu_pct:-1,rssi:-128,password:'secret'}});
  assert.deepEqual(sample,{mac:'aa:bb:cc:dd:ee:01',online:true,firmware:'2.4.25-c3',stats:{heap_free:120000}});
  assert.equal(nodeSample({mac:'aa:bb:cc:dd:ee:01',stats:{rssi:0}}).stats.rssi,0);
  assert.equal(nodeSample({mac:'aa:bb:cc:dd:ee:01',stats:{cpu_pct:101}}).stats.cpu_pct,undefined);assert.equal(nodeSample({mac:'aa:bb:cc:dd:ee:01'}).online,undefined);
  assert.throws(()=>safeURL('https://admin:secret@example.test'),/credentials/);assert.throws(()=>safeURL('https://example.test/?token=secret'),/credentials/);assert.throws(()=>safeURL('http://example.test'),/HTTPS/);
});
test('records read-only samples, detects resets, avoids false offline on polling failure and never overwrites',async()=>{
  const work=await fs.mkdtemp(path.join(os.tmpdir(),'hardware-soak-test-')),output=path.join(work,'run');let inventoryCalls=0;
  const fetcher=async(url,options)=>{assert.equal(options.method,undefined);assert.equal(options.headers.Authorization,'Bearer TEST_SECRET_DO_NOT_RECORD');if(url.endsWith('/api/overview'))return {ok:true,json:async()=>({broker:{nodes_online:1,nodes_total:1}})};inventoryCalls++;if(inventoryCalls===2)return {ok:false,status:503};return {ok:true,json:async()=>[{mac:'aa:bb:cc:dd:ee:01',online:true,uptime_sec:inventoryCalls===1?100:2,bytes_up:inventoryCalls===1?1000:2,ssid:'DO_NOT_RECORD',wifi_password:'DO_NOT_RECORD',stats:{rssi:-67,heap_free:120000,cpu_pct:12}}]};};
  let virtualNow=0;const result=await runRecorder({url:'https://fixture.test',output,token:'TEST_SECRET_DO_NOT_RECORD',durationMs:140,intervalMs:30,fetcher,now:()=>virtualNow,wait:async ms=>{virtualNow+=ms;}});
  assert.ok(result.completed);assert.equal(result.samples,5);assert.equal(result.expected_samples,5);assert.equal(result.failed_samples,1);assert.equal(result.boards['aa:bb:cc:dd:ee:01'].uptime_resets,1);
  const log=await fs.readFile(path.join(output,'samples.jsonl'),'utf8'),records=log.trim().split('\n').map(JSON.parse);assert.ok(!log.includes('TEST_SECRET'));assert.ok(!log.includes('DO_NOT_RECORD'));assert.ok(!records.flatMap(record=>record.events).some(event=>event.kind==='node.offline'));
  await assert.rejects(runRecorder({url:'https://fixture.test',output,token:'TEST_SECRET_DO_NOT_RECORD',durationMs:1,intervalMs:1,fetcher}),/already exists/);assert.equal(await fs.readFile(path.join(output,'samples.jsonl'),'utf8'),log);
  const stopped=new AbortController();stopped.abort();const interrupted=await runRecorder({url:'https://fixture.test',output:path.join(work,'interrupted'),token:'TEST_SECRET_DO_NOT_RECORD',durationMs:140,intervalMs:30,fetcher,signal:stopped.signal});assert.equal(interrupted.completed,false);assert.equal(interrupted.samples,0);
  assert.ok(path.resolve(work).startsWith(path.resolve(os.tmpdir())+path.sep));await fs.rm(work,{recursive:true,force:true});
});
