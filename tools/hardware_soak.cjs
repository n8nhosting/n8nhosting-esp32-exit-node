// Read-only recorder. Never provision, assign, measure, flash, or mutate boards.
const fs=require('node:fs/promises'),path=require('node:path'),{performance}=require('node:perf_hooks');
const numeric=value=>typeof value==='number'&&Number.isFinite(value)&&value>=0?value:undefined;
function nodeSample(node){
  if(!/^(?:[a-f0-9]{2}:){5}[a-f0-9]{2}$/i.test(node.mac||''))return null;
  const sample={mac:node.mac.toLowerCase()};if(typeof node.online==='boolean')sample.online=node.online;
  if(/^[a-zA-Z0-9.+_-]{1,80}$/.test(node.firmware||''))sample.firmware=node.firmware;
  for(const key of ['uptime_sec','mux_links','active_dials','max_streams','bytes_up','bytes_down','dials_ok','dials_err','dials_pinned_offline'])if(numeric(node[key])!==undefined)sample[key]=node[key];
  if(['queued','sent','ok','failed','timeout','rolled_back'].includes(node.ota_status))sample.ota_status=node.ota_status;
  const stats=node.stats;if(stats&&typeof stats==='object'){
    sample.stats={};if(Number.isFinite(Date.parse(stats.at)))sample.stats.at=new Date(stats.at).toISOString();
    for(const key of ['heap_free','heap_min','heap_total','psram_free','psram_total','streams','app_bytes','slot_bytes','flash_bytes'])if(numeric(stats[key])!==undefined)sample.stats[key]=stats[key];
    if(numeric(stats.cpu_pct)!==undefined&&stats.cpu_pct<=100)sample.stats.cpu_pct=stats.cpu_pct;
    if(Number.isInteger(stats.rssi)&&stats.rssi>=-127&&stats.rssi<=0)sample.stats.rssi=stats.rssi;
  }
  return sample;
}
function summarySample(value,source){const broker=source==='admin'?value?.broker:value,summary={};for(const key of ['nodes_online','nodes_total','uptime_sec','bytes_up','bytes_down','dials_ok','dials_err','dials_pinned_offline'])if(numeric(broker?.[key])!==undefined)summary[key]=broker[key];return summary;}
function safeURL(raw){let url;try{url=new URL(raw);}catch{throw Error('Supply a valid status service URL.');}if(url.username||url.password||url.search||url.hash)throw Error('Service URLs must not contain credentials, query parameters, or fragments.');if(url.protocol!=='https:'&&!(url.protocol==='http:'&&['localhost','127.0.0.1','[::1]'].includes(url.hostname)))throw Error('Use HTTPS, or HTTP on localhost for a private/local collector.');return url.href.replace(/\/$/,'');}
function sleep(ms,signal){return new Promise(resolve=>{if(signal?.aborted)return resolve();let timer;const done=()=>{clearTimeout(timer);signal?.removeEventListener('abort',done);resolve();};timer=setTimeout(done,ms);signal?.addEventListener('abort',done,{once:true});});}
async function readJSON(fetcher,url,headers,signal,timeoutMs){
  const controller=new AbortController(),abort=()=>controller.abort(),timer=setTimeout(abort,timeoutMs);if(signal?.aborted)abort();else signal?.addEventListener('abort',abort,{once:true});
  try{const response=await fetcher(url,{headers,signal:controller.signal,redirect:'error'});if(!response.ok)return {error:{kind:'http_error',status:response.status}};return {value:await response.json()};}
  catch{return {error:{kind:controller.signal.aborted?'request_cancelled':'request_failed'}};}
  finally{clearTimeout(timer);signal?.removeEventListener('abort',abort);}
}
async function runRecorder(options){
  const {source='admin',token='',durationMs=86400000,intervalMs=60000,signal,fetcher=fetch}=options;
  const now=options.now||(()=>performance.now()),wait=options.wait||sleep;
  if(!['admin','broker'].includes(source))throw Error('Source must be admin or broker.');
  if(source==='admin'&&!token)throw Error('Set the admin token in the selected environment variable; do not pass it on the command line.');
  if(!Number.isFinite(durationMs)||durationMs<=0||!Number.isFinite(intervalMs)||intervalMs<=0)throw Error('Duration and interval must be positive.');
  const baseURL=safeURL(options.url),output=path.resolve(options.output);
  await fs.mkdir(path.dirname(output),{recursive:true});try{await fs.mkdir(output);}catch(error){if(error.code==='EEXIST')throw Error('Output directory already exists; choose a new directory.');throw error;}
  const started=now(),startedAt=new Date().toISOString(),deadline=started+durationMs,previous=new Map(),boards={};
  const metadata={schema:1,pid:process.pid,source,service:baseURL,started_at:startedAt,duration_sec:durationMs/1000,interval_sec:intervalMs/1000,acceptance:'Recording is evidence collection, not a hardware acceptance verdict.'};
  await fs.writeFile(path.join(output,'run.json'),JSON.stringify(metadata,null,2),{flag:'wx'});
  const log=await fs.open(path.join(output,'samples.jsonl'),'wx'),headers=source==='admin'?{Authorization:'Bearer '+token}:{},paths=source==='admin'?['/api/devices','/api/overview']:['/nodes','/status'];
  let samples=0,failedSamples=0,next=started;
  try{
    while(!signal?.aborted&&now()<deadline){
      await wait(Math.max(0,Math.min(next,deadline)-now()),signal);if(signal?.aborted||now()>=deadline)break;
      const ts=new Date().toISOString(),elapsed=(now()-started)/1000,timeout=Math.min(15000,Math.max(100,intervalMs*.8));
      const [inventory,status]=await Promise.all(paths.map(endpoint=>readJSON(fetcher,baseURL+endpoint,headers,signal,timeout)));
      if(signal?.aborted)break;
      const errors=[...(inventory.error?[{endpoint:'inventory',...inventory.error}]:[]),...(status.error?[{endpoint:'summary',...status.error}]:[])],events=[];
      const devices=Array.isArray(inventory.value)?inventory.value.map(nodeSample).filter(Boolean).filter(node=>!options.macs?.length||options.macs.includes(node.mac)):[];
      if(!inventory.error&&!Array.isArray(inventory.value))errors.push({endpoint:'inventory',kind:'invalid_shape'});
      if(!status.error&&(!status.value||typeof status.value!=='object'||Array.isArray(status.value)))errors.push({endpoint:'summary',kind:'invalid_shape'});
      if(Array.isArray(inventory.value)){
        const present=new Set(devices.map(node=>node.mac));
        for(const node of devices){
          const prior=previous.get(node.mac);if(prior){if(prior.present===false)events.push({kind:'node.returned',mac:node.mac});else if(typeof prior.online==='boolean'&&typeof node.online==='boolean'&&prior.online!==node.online)events.push({kind:node.online?'node.online':'node.offline',mac:node.mac});if(numeric(node.uptime_sec)!==undefined&&numeric(prior.uptime_sec)!==undefined&&node.uptime_sec<prior.uptime_sec)events.push({kind:'node.uptime_reset',mac:node.mac});if(['bytes_up','bytes_down'].some(key=>numeric(node[key])!==undefined&&numeric(prior[key])!==undefined&&node[key]<prior[key]))events.push({kind:'node.counter_reset',mac:node.mac});}
          previous.set(node.mac,{...node,present:true});const total=boards[node.mac]||(boards[node.mac]={samples:0,online_samples:0,offline_samples:0,unknown_samples:0,uptime_resets:0,counter_resets:0});total.samples++;total[typeof node.online==='boolean'?(node.online?'online_samples':'offline_samples'):'unknown_samples']++;
          if(numeric(node.stats?.heap_free)!==undefined)total.min_heap_free=total.min_heap_free===undefined?node.stats.heap_free:Math.min(total.min_heap_free,node.stats.heap_free);
          if(numeric(node.stats?.cpu_pct)!==undefined)total.max_cpu_pct=Math.max(total.max_cpu_pct||0,node.stats.cpu_pct);
        }
        for(const [mac,prior] of previous)if(!present.has(mac)&&prior.present){events.push({kind:'node.missing',mac});previous.set(mac,{...prior,present:false});}
      }
      for(const event of events){const total=boards[event.mac];if(event.kind==='node.uptime_reset')total.uptime_resets++;if(event.kind==='node.counter_reset')total.counter_resets++;}
      const record={ts,elapsed_sec:Math.round(elapsed*1000)/1000,devices,summary:summarySample(status.value,source),errors,events};
      await log.appendFile(JSON.stringify(record)+'\n');await log.sync();samples++;if(errors.length)failedSamples++;
      next+=intervalMs;if(next<now())next=now()+intervalMs;
    }
  }finally{await log.close();}
  const result={...metadata,finished_at:new Date().toISOString(),elapsed_sec:(now()-started)/1000,completed:!signal?.aborted&&now()>=deadline,expected_samples:Math.ceil(durationMs/intervalMs),samples,failed_samples:failedSamples,boards};
  await fs.writeFile(path.join(output,'result.json'),JSON.stringify(result,null,2),{flag:'wx'});return result;
}
async function main(){
  const args={};for(let i=2;i<process.argv.length;i+=2){const key=process.argv[i],value=process.argv[i+1];if(!['--source','--url','--hours','--interval','--out','--token-env','--mac'].includes(key)||!value)throw Error('Use --source admin|broker --url URL --out NEW_DIRECTORY [--hours 24 --interval 60 --token-env ADMIN_TOKEN --mac MAC,MAC].');args[key]=value;}
  const envName=args['--token-env']||'ADMIN_TOKEN';if(!/^[A-Za-z_][A-Za-z0-9_]*$/.test(envName))throw Error('Token environment variable name is invalid.');
  const hours=Number(args['--hours']||24),interval=Number(args['--interval']||60);if(!(hours>0&&hours<=168&&interval>=5&&interval<=3600))throw Error('Hours must be >0 and <=168; interval must be 5..3600 seconds.');if(!args['--url']||!args['--out'])throw Error('Supply --url and a new --out directory.');
  const macs=args['--mac']?.split(',').map(value=>value.toLowerCase());if(macs?.some(value=>! /^(?:[a-f0-9]{2}:){5}[a-f0-9]{2}$/.test(value)))throw Error('MAC filters must contain complete colon-separated MAC addresses.');
  const controller=new AbortController();process.once('SIGINT',()=>controller.abort());process.once('SIGTERM',()=>controller.abort());
  const result=await runRecorder({source:args['--source']||'admin',url:args['--url'],output:args['--out'],durationMs:hours*3600000,intervalMs:interval*1000,token:process.env[envName]||'',macs,signal:controller.signal});
  console.log(JSON.stringify({completed:result.completed,samples:result.samples,failed_samples:result.failed_samples,acceptance:'Review collected evidence; this is not an automatic hardware pass.'}));
}
module.exports={nodeSample,summarySample,safeURL,runRecorder};
if(require.main===module)main().catch(error=>{console.error(['Output directory already exists; choose a new directory.','Set the admin token in the selected environment variable; do not pass it on the command line.'].includes(error.message)?error.message:'Recorder could not start or finish. Existing files were preserved; verify options, connectivity and output permissions.');process.exitCode=1;});
