// Local-only browser fixture: real admin API + isolated SQLite; fake broker/geo.
// Build output/playwright/admin-fixture.exe first, then run this script.
const http = require('node:http');
const fs = require('node:fs');
const path = require('node:path');
const os = require('node:os');
const {spawn} = require('node:child_process');
const root = path.resolve(__dirname, '..');
const output = path.join(root, 'output', 'playwright');
fs.mkdirSync(output,{recursive:true});
const work = fs.mkdtempSync(path.join(os.tmpdir(),'exitnode-dashboard-'));
const token='dashboard-fixture-local-only';
const created=Date.now();
const iso=age=>new Date(Date.now()-age).toISOString();
const macs=Array.from({length:8},(_,i)=>`aa:bb:cc:dd:ee:${String(i+1).padStart(2,'0')}`);
let unauthorized=false, apiRequests=0, apiFailures=false, sameIP=false;
let admin;
function nodes(){return macs.map((mac,i)=>({mac,online:i!==2,blocked:i===3,remote_ip:sameIP?'198.51.100.10':`198.51.100.${i+10}`,firmware:i===6?'2.4.22-c3':`2.4.23-${i===1?'c3':'s3'}`,ssid:i===1?'Cafe guest':'Office WiFi',chip:(i===1||i===6)?'esp32c3':'esp32s3',uptime_sec:3600+i*43200,first_seen:iso(30*86400000),last_seen:iso(i===2?900000:0),idle_conns:0,mux_links:i===2?0:1,max_streams:(i===1||i===6)?4:40,link_since:i===2?'':iso(400000),slot:i+1,pool:true,active_dials:i===1?3:2,dials_ok:1200+i*13,dials_err:3,bytes_up:10000000+Math.floor((Date.now()-created)*25),bytes_down:70000000+Math.floor((Date.now()-created)*80),ota_status:i===4?'sent':i===5?'failed':i===7?'queued':'',ota_at:iso(20000),ota_target:'2.4.24-s3',stats:i===6?null:{at:iso(i===7?600000:0),uptime_sec:3000,heap_total:i===1?292000:320000,heap_free:i===1?26000:210000,heap_min:i===1?18000:180000,cpu_pct:i===1?91:12,psram_total:i===1?0:8388608,psram_free:i===1?0:7500000,app_bytes:1200000,slot_bytes:2031616,flash_bytes:i===1?4194304:16777216,streams:2}}));}
const json=(res,data,status=200)=>{res.writeHead(status,{'content-type':'application/json'});res.end(JSON.stringify(data));};
const backend=http.createServer((req,res)=>{
 const p=new URL(req.url,'http://localhost').pathname;
 if(p==='/nodes')return json(res,nodes());
 if(p==='/status')return json(res,{nodes_online:7,nodes_total:8,mux_links:7,tls:true,uptime_sec:Math.floor((Date.now()-created)/1000)+40000,bytes_up:100000000+Math.floor((Date.now()-created)*25),bytes_down:700000000+Math.floor((Date.now()-created)*80),dials_ok:7000,dials_err:9,dials_no_node:0,dials_pinned_offline:2});
 if(p.startsWith('/nodes/'))return json(res,{ok:true});
 if(p.startsWith('/geo/')){
  const providers=['Advanced Wireless Network Company Limited','Triple T Broadband Public Company Limited','True Internet Corporation Co., Ltd.','Total Access Communication Public Company Limited','National Telecom Public Company Limited','Example Independent ISP','ADVANCED WIRELESS NETWORK COMPANY LIMITED','AIS Fibre'];
  const index=Number(p.split('.').at(-1))-10;
  return json(res,{status:'success',country:'Thailand',city:p.endsWith('11')?'Chiang Mai':'Bangkok',regionName:'Bangkok',isp:providers[index]||'Unknown fixture ISP',lat:13.7563,lon:100.5018});
 }
 return json(res,{error:'fixture endpoint not found'},404);
});
function listen(server){return new Promise(r=>server.listen(0,'127.0.0.1',()=>r(server.address().port)));}
const sleep=ms=>new Promise(r=>setTimeout(r,ms));
async function freePort(){const s=http.createServer();const port=await listen(s);await new Promise(r=>s.close(r));return port;}
async function start(port,brokerPort){
 const log=fs.openSync(path.join(output,'fixture-admin.log'),'a');
 admin=spawn(process.argv[2]||path.join(output,'admin-fixture.exe'),[],{cwd:root,windowsHide:true,stdio:['ignore',log,log],env:{...process.env,ADMIN_TOKEN:token,DATA_FILE:path.join(work,'fixture.db'),LISTEN_ADDR:`127.0.0.1:${port}`,BROKER_STATUS_URL:`http://127.0.0.1:${brokerPort}`,GEOIP_URL:`http://127.0.0.1:${brokerPort}/geo/`,POLL_INTERVAL:'1s',GOST_API_URL:'',PROXY_PUBLIC_HOST:'proxy.example.test'}});
 fs.closeSync(log);
 for(let i=0;i<100;i++){try{if((await fetch(`http://127.0.0.1:${port}/healthz`)).ok)return;}catch{}await sleep(100);}
 throw Error('fixture admin failed to start; see fixture-admin.log');
}
async function stop(){if(!admin||admin.exitCode!==null)return;await new Promise(r=>{admin.once('exit',r);admin.kill();});}
(async()=>{
 const brokerPort=await listen(backend),adminPort=await freePort();
 await start(adminPort,brokerPort);await stop();
 await new Promise((resolve,reject)=>{
  const seed=spawn(process.env.PYTHON||'python',[path.join(__dirname,'dashboard_seed.py'),path.join(work,'fixture.db')],{stdio:'inherit',windowsHide:true});
  seed.once('error',reject);seed.once('exit',code=>code===0?resolve():reject(Error(`seed failed: ${code}`)));
 });
 await start(adminPort,brokerPort);
 const front=http.createServer((req,res)=>{
  const p=new URL(req.url,'http://localhost').pathname;
  if(p==='/__fixture/shutdown'){
   json(res,{ok:true});setImmediate(async()=>{front.close();backend.close();await stop();process.exit();});return;
  }
  if(p==='/__fixture/control'){
   const u=new URL(req.url,'http://localhost');
   if(u.searchParams.has('unauthorized'))unauthorized=u.searchParams.get('unauthorized')==='1';
   if(u.searchParams.has('apiFailures'))apiFailures=u.searchParams.get('apiFailures')==='1';
   if(u.searchParams.has('sameIP'))sameIP=u.searchParams.get('sameIP')==='1';
   return json(res,{unauthorized,apiRequests,apiFailures,sameIP});
  }
  if(p.startsWith('/api/')){apiRequests++;if(unauthorized)return json(res,{error:'unauthorized'},401);if(apiFailures)return json(res,{error:'simulated temporary API failure'},503);}
  if(p==='/'||p==='/index.html'){res.writeHead(200,{'content-type':'text/html; charset=utf-8'});return fs.createReadStream(path.join(root,'admin-api','dashboard','index.html')).pipe(res);}
  const upstream=http.request({host:'127.0.0.1',port:adminPort,path:req.url,method:req.method,headers:req.headers},r=>{res.writeHead(r.statusCode,r.headers);r.pipe(res);});
  upstream.on('error',()=>json(res,{error:'fixture upstream unavailable'},502));req.pipe(upstream);
 });
 const port=await listen(front),meta={url:`http://127.0.0.1:${port}`,token,db:path.join(work,'fixture.db'),adminPort,brokerPort,adminPID:admin.pid,fixturePID:process.pid};
 fs.writeFileSync(path.join(output,'fixture.json'),JSON.stringify(meta,null,2));console.log(JSON.stringify(meta));
 const shutdown=async()=>{front.close();backend.close();await stop();process.exit();};process.on('SIGTERM',shutdown);process.on('SIGINT',shutdown);
})().catch(async e=>{console.error(e);await stop();backend.close();process.exit(1);});
