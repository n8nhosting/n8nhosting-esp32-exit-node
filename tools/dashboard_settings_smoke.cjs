// Functional browser acceptance for device settings, board-first assignment and
// the contained details drawer. All mutations use an isolated local fixture.
const fs = require('node:fs');
const path = require('node:path');
const assert = require('node:assert/strict');
const {spawn} = require('node:child_process');
const root = path.resolve(__dirname, '..');
const out = path.join(root, 'output', 'playwright');
fs.mkdirSync(out, {recursive: true});
for(const name of ['settings-acceptance.json','settings-failure.txt'])fs.rmSync(path.join(out,name),{force:true});
function findCore() {
  if(process.env.PLAYWRIGHT_CORE_PATH)return require(process.env.PLAYWRIGHT_CORE_PATH);
  const cache=path.join(process.env.LOCALAPPDATA,'npm-cache','_npx');
  for(const name of fs.readdirSync(cache)){
    const dir=path.join(cache,name,'node_modules','playwright-core');
    if(fs.existsSync(path.join(dir,'package.json')))return require(dir);
  }
  throw Error('Set PLAYWRIGHT_CORE_PATH to an installed playwright-core directory');
}
function chromePath() {
  if(process.env.DASHBOARD_CHROME)return process.env.DASHBOARD_CHROME;
  const cache=path.join(process.env.LOCALAPPDATA,'ms-playwright');
  for(const name of fs.readdirSync(cache).filter(n=>/^chromium-/.test(n)).sort().reverse()){
    for(const rel of ['chrome-win64/chrome.exe','chrome-win/chrome.exe']){
      const exe=path.join(cache,name,rel);if(fs.existsSync(exe))return exe;
    }
  }
  throw Error('Set DASHBOARD_CHROME to a local Chromium executable');
}
const pause=ms=>new Promise(resolve=>setTimeout(resolve,ms));
const checks=[],screenshots=[];
let fixture,meta,browser;
function ok(name){checks.push(name);console.log('PASS '+name);}
async function screenshot(page,name){const filename='settings-'+name+'.png';await page.screenshot({path:path.join(out,filename),fullPage:false});screenshots.push(filename);}
async function noOverflow(page){const dimensions=await page.evaluate(()=>({width:document.documentElement.scrollWidth,viewport:innerWidth}));assert.ok(dimensions.width<=dimensions.viewport+1,JSON.stringify(dimensions));}
async function getJSON(url,opts={}){const response=await fetch(meta.url+url,{...opts,headers:{Authorization:'Bearer '+meta.token,'Content-Type':'application/json',...opts.headers}});assert.ok(response.ok,`${opts.method||'GET'} ${url}: ${response.status} ${await (!response.ok?response.text():Promise.resolve(''))}`);return response.json();}
async function node(mac){return (await getJSON('/api/devices')).find(d=>d.mac===mac);}
function row(page,mac){return page.locator('#dtbl tbody tr').filter({has:page.locator('[data-device-link="'+mac+'"]')});}
async function edit(page,mac){await row(page,mac).locator('[data-act="edit"]').click();await page.locator('#dmCapacity').waitFor();}
async function openDrawer(page,mac){await row(page,mac).locator('.device-title').click();await page.locator('#ddrawer:not([hidden])').waitFor();await page.waitForFunction(expected=>drawerMAC===expected,mac);}
async function saveEditor(page){await page.locator('#dmSave').click();await page.locator('#dmodal').waitFor({state:'hidden'});await page.waitForFunction(()=>!document.querySelector('#dmSave').disabled);}
async function tab(page,id){await page.locator('#ddTabs [data-panel="'+id+'"]').click();await page.locator('#'+id).waitFor();}
async function drawerFrame(page){
  const size=await page.locator('.drawer-sheet').boundingBox();
  const viewport=page.viewportSize();assert.ok(size.x>=-1&&size.y>=-1&&size.x+size.width<=viewport.width+1&&size.y+size.height<=viewport.height+1,JSON.stringify(size));
  const scroll=await page.locator('#ddBody').evaluate(el=>({horizontal:el.scrollWidth>el.clientWidth+1,scrollbar:getComputedStyle(el).scrollbarWidth,webkit:getComputedStyle(el,'::-webkit-scrollbar').display}));
  assert.equal(scroll.horizontal,false);assert.ok(scroll.scrollbar==='none'||scroll.webkit==='none',JSON.stringify(scroll));
  await noOverflow(page);
}
(async()=>{
  const started=Date.now(),log=fs.openSync(path.join(out,'settings-fixture-launch.log'),'w');
  fixture=spawn(process.execPath,[path.join(__dirname,'dashboard_fixture.cjs')],{cwd:root,windowsHide:true,stdio:['ignore',log,log]});fs.closeSync(log);
  for(let i=0;i<150;i++){
    if(fixture.exitCode!==null)throw Error('Fixture exited: see settings-fixture-launch.log');
    try{const file=path.join(out,'fixture.json');if(fs.statSync(file).mtimeMs>=started){meta=JSON.parse(fs.readFileSync(file));break;}}catch{}
    await pause(100);
  }
  assert.ok(meta,'Fixture startup timed out');
  const boards=await getJSON('/api/devices'),mac=boards[0].mac,emptyMAC=boards[6].mac;
  browser=await findCore().chromium.launch({executablePath:chromePath(),headless:true});
  const page=await browser.newPage({viewport:{width:1440,height:900},deviceScaleFactor:1}),errors=[];
  page.on('pageerror',error=>errors.push(error.message));
  await page.goto(meta.url);await page.locator('#tok').fill(meta.token);await page.locator('#loginBtn').click();await page.locator('#ov[data-loaded="true"]').waitFor();
  await page.locator('nav a[data-view="devices"]').click();await page.locator('#dtbl[data-loaded="true"]').waitFor();

  await edit(page,mac);
  let writes=0;const countWrites=request=>{if(request.method()==='PUT'&&new URL(request.url()).pathname.startsWith('/api/devices/'))writes++;};page.on('request',countWrites);
  for(const value of ['-1','100001','']){
    await page.locator('#dmCapacity').fill(value);await page.locator('#dmSave').click();assert.match(await page.locator('#dmErr').innerText(),/0 to 100000/);assert.equal(await page.locator('#dmodal').isVisible(),true);
  }
  assert.equal(writes,0,'Invalid speed must not mutate a board');page.off('request',countWrites);
  await page.locator('#dmCapacity').fill('17.25');await screenshot(page,'editor-desktop');await saveEditor(page);
  assert.equal((await node(mac)).capacity_mbps,17.25);assert.equal((await node(mac)).capacity_source,'manual');
  await edit(page,mac);assert.equal(await page.locator('#dmCapacity').inputValue(),'17.25');await page.locator('#dmCapacity').fill('0');await saveEditor(page);
  assert.equal((await node(mac)).capacity_mbps,0);assert.equal((await node(mac)).capacity_source,'unmeasured');
  await edit(page,mac);await page.locator('#dmCapacity').fill('15');await saveEditor(page);
  await openDrawer(page,mac);await tab(page,'ddCapacity');assert.match(await page.locator('#ddCapacity').innerText(),/15 Mbps.*manual/s);assert.match(await page.locator('#ddCapacity').innerText(),/Set manually/);await page.locator('#ddClose').click();
  ok('manual speed rejects invalid input, persists positive values and source, and zero resets capacity');

  await edit(page,emptyMAC);await page.locator('#dmName').fill('');await saveEditor(page);
  const autoName='Board-'+emptyMAC.replace(/[^a-f0-9]/gi,'').slice(-4).toUpperCase();assert.equal((await node(emptyMAC)).name,autoName);assert.equal(await row(page,emptyMAC).locator('.device-title').innerText(),autoName);
  await openDrawer(page,emptyMAC);assert.equal(await page.locator('#ddTitle').innerText(),autoName);await page.locator('#ddEdit').click();assert.equal(await page.locator('#dmName').inputValue(),autoName);assert.equal(await page.locator('#dmName').getAttribute('placeholder'),autoName);
  await page.locator('#dmName').fill('บอร์ดทดสอบ · ชื่อที่กำหนดเอง');await saveEditor(page);assert.equal(await page.locator('#ddTitle').innerText(),'บอร์ดทดสอบ · ชื่อที่กำหนดเอง');await page.locator('#ddClose').click();
  assert.equal(await row(page,emptyMAC).locator('.device-title').innerText(),'บอร์ดทดสอบ · ชื่อที่กำหนดเอง');
  ok('blank names derive Board-XXXX from the MAC while custom Thai names remain intact');

  await row(page,emptyMAC).locator('[data-act="assign-board"]').click();await page.locator('#baSlot option[value="9"]').waitFor({state:'attached'});
  assert.match(await page.locator('#baSummary').innerText(),new RegExp(emptyMAC));assert.equal(await page.locator('#baUser').inputValue(),'');
  await page.locator('#baSlot').selectOption('9');await screenshot(page,'assign-desktop');await page.locator('#baSave').click();await page.locator('#bamodal').waitFor({state:'hidden'});
  assert.equal((await node(emptyMAC)).slot,9);assert.equal((await node(emptyMAC)).active_users,0);
  const unassigned='settings-fixture-user';await getJSON('/api/users',{method:'POST',body:JSON.stringify({username:unassigned,password:'local-fixture-password',quota_gb:0,rate_mbps:0})});
  await row(page,mac).locator('[data-act="assign-board"]').click();await page.locator('#baUser option[value="'+unassigned+'"]').waitFor({state:'attached'});
  await page.locator('#baSlot').selectOption('10');await page.locator('#baUser').selectOption(unassigned);
  let assignmentBody;
  const assignPattern='**/api/users/'+unassigned+'/assign';
  await page.route(assignPattern,async route=>{assignmentBody=route.request().postDataJSON();await route.fulfill({status:409,contentType:'application/json',body:JSON.stringify({error:'capacity',reason:'Capacity changed during assignment'})});});
  await page.locator('#baSave').click();await page.waitForFunction(()=>document.querySelector('#baErr').textContent.includes('Slot saved; user assignment failed'));
  assert.deepEqual(assignmentBody,{device_mac:mac});assert.equal((await node(mac)).slot,10);assert.match(await page.locator('#baErr').innerText(),/Capacity changed/);assert.equal(await page.locator('#bamodal').isVisible(),true);
  await page.unroute(assignPattern);await page.locator('#baSave').click();await page.locator('#bamodal').waitFor({state:'hidden'});
  assert.equal((await getJSON('/api/users')).find(u=>u.username===unassigned).device_mac,mac);
  ok('board-first Assign locks the clicked MAC, saves a slot alone, and retains partial success on rejected user assignment');

  await row(page,mac).locator('[data-act="assign-board"]').click();await page.locator('#baUser option[value="'+unassigned+'"]').waitFor({state:'attached'});
  await page.locator('#baSlot').selectOption('11');await page.locator('#baUser').selectOption(unassigned);
  let releaseWrite,observeWrite,posts=0;
  const heldWrite=new Promise(resolve=>{releaseWrite=resolve;}),writeObserved=new Promise(resolve=>{observeWrite=resolve;});
  const devicePattern='**/api/devices/'+encodeURIComponent(mac);
  const countAssignments=request=>{if(request.method()==='POST'&&new URL(request.url()).pathname.endsWith('/assign'))posts++;};page.on('request',countAssignments);
  await page.route(devicePattern,async route=>{if(route.request().method()!=='PUT')return route.continue();observeWrite();await heldWrite;await route.continue();});
  await page.locator('#baSave').click();await writeObserved;
  for(const id of ['baSlot','baUser','baSave'])assert.equal(await page.locator('#'+id).isDisabled(),true,'Pending assignment must lock '+id);
  await page.keyboard.press('Escape');
  await row(page,emptyMAC).locator('[data-act="assign-board"]').click();await page.locator('#baSlot option[value="9"]').waitFor({state:'attached'});assert.match(await page.locator('#baSummary').innerText(),new RegExp(emptyMAC));
  releaseWrite();await page.waitForFunction(()=>document.querySelector('#toast').textContent.includes('User assignment was cancelled'));
  assert.equal((await node(mac)).slot,11);assert.equal(posts,0,'Cancelling a delayed slot write must not continue to user assignment');assert.equal(await page.locator('#bamodal').isVisible(),true);assert.match(await page.locator('#baSummary').innerText(),new RegExp(emptyMAC));assert.equal(await page.locator('#baSlot').inputValue(),'9');
  await page.unroute(devicePattern);page.off('request',countAssignments);await page.locator('#baCancel').click();
  ok('cancelling a delayed slot save preserves the newly opened board modal and does not submit the cancelled user assignment');

  const events=Array.from({length:50},(_,i)=>({id:1000+i,ts:new Date(Date.now()-i*1000).toISOString(),kind:'device.updated',severity:'info',actor:'local browser fixture',message:'Preserved activity '+String(i+1).padStart(2,'0')}));
  const eventRoute='**/api/events?subject=*';await page.route(eventRoute,route=>route.fulfill({status:200,contentType:'application/json',body:JSON.stringify({events})}));
  await openDrawer(page,mac);await page.waitForFunction(()=>document.querySelector('#ddActivity').textContent.includes('Preserved activity'));
  const panels=['ddHealth','ddHistory','ddCapacity','ddRouting','ddLocation','ddWifi','ddUsers','ddNotes','ddActivity','ddDanger'];
  assert.equal(await page.locator('#ddTabs [role="tab"]').count(),10);
  for(const id of panels){await tab(page,id);assert.equal(await page.locator('#ddBody [role="tabpanel"]:visible').count(),1);assert.equal(await page.locator('#ddTabs [data-panel="'+id+'"]').getAttribute('aria-selected'),'true');assert.equal(await page.locator('#'+id).getAttribute('aria-labelledby'),'tab-'+id);await drawerFrame(page);}
  await page.locator('#tab-ddDanger').focus();await page.keyboard.press('Home');assert.equal(await page.evaluate(()=>document.activeElement.id),'tab-ddHealth');await page.keyboard.press('ArrowRight');assert.equal(await page.evaluate(()=>document.activeElement.id),'tab-ddHistory');await page.keyboard.press('End');assert.equal(await page.evaluate(()=>document.activeElement.id),'tab-ddDanger');
  await tab(page,'ddActivity');const seen=[];
  for(;;){seen.push(...await page.locator('#ddActivity .msg').evaluateAll(items=>items.map(el=>el.firstChild.textContent)));const next=page.locator('#ddActivity [data-activity-page="1"]');if(await next.isDisabled())break;await next.click();}
  assert.deepEqual(seen,events.map(e=>e.message));
  await tab(page,'ddRouting');await page.locator('#ddBody').focus();await pause(11000);assert.equal(await page.locator('#tab-ddRouting').getAttribute('aria-selected'),'true');assert.equal(await page.locator('#ddRouting').isVisible(),true);assert.equal(await page.evaluate(()=>document.activeElement.id),'ddBody');
  await screenshot(page,'drawer-desktop');await page.locator('#ddClose').click();await screenshot(page,'devices-desktop');
  ok('all ten drawer sections stay contained with hidden inactive panels, keyboard tabs, preserved refresh state and all 50 activities accessible');

  for(const width of [1024,800]){
    await page.setViewportSize({width,height:900});await noOverflow(page);
    assert.equal(await row(page,mac).locator('td:visible').count(),7,'Responsive rows must retain all seven fields');
    const action=row(page,mac).locator('[data-act="assign-board"]');await action.scrollIntoViewIfNeeded();
    const hit=await action.evaluate(button=>{const r=button.getBoundingClientRect(),cell=button.closest('td').getBoundingClientRect(),target=document.elementFromPoint(r.right-2,r.top+r.height/2),label=document.createRange();label.selectNodeContents(button);return {left:r.left,right:r.right,cellRight:cell.right,viewport:innerWidth,labelLines:label.getClientRects().length,clickable:target===button||button.contains(target)};});
    assert.ok(hit.left>=0&&hit.right<=hit.viewport&&hit.right<=hit.cellRight+1&&hit.clickable,JSON.stringify(hit));assert.equal(hit.labelLines,1,'Primary Assign label must remain on one line');await screenshot(page,width===1024?'devices-narrow-desktop':'devices-tablet');
  }
  ok('1024px and 800px responsive rows retain all seven fields and an unclipped, directly clickable primary Assign button');

  await page.setViewportSize({width:375,height:812});await noOverflow(page);await screenshot(page,'devices-mobile');
  await row(page,mac).locator('[data-act="assign-board"]').click();await page.locator('#baSlot option[value="11"]').waitFor({state:'attached'});await noOverflow(page);await screenshot(page,'assign-mobile');await page.keyboard.press('Escape');
  await openDrawer(page,mac);
  for(const id of panels){await tab(page,id);await drawerFrame(page);}
  await tab(page,'ddCapacity');await screenshot(page,'drawer-mobile');await page.locator('#ddEdit').click();await noOverflow(page);await screenshot(page,'editor-mobile');
  for(const name of ['General','Location','WiFi','Notes']){await page.locator('[data-editor-tab="'+name+'"]').click();assert.equal(await page.locator('#dmodal .editor-panel:visible').count(),1);await noOverflow(page);}
  await page.keyboard.press('Escape');assert.equal(await page.locator('#ddrawer').isVisible(),true);await page.keyboard.press('Escape');assert.equal(await page.locator('#ddrawer').isVisible(),false);
  assert.deepEqual(errors,[]);
  ok('375x812 mobile Devices, Assign, settings and all drawer panels have no horizontal overflow or uncaught browser exceptions');
  fs.writeFileSync(path.join(out,'settings-acceptance.json'),JSON.stringify({passed:checks,browser:await browser.version(),fixture:'real admin API + isolated seeded SQLite + fake broker/geo; simulated 409 and 50-event activity response',screenshots},null,2));
})().catch(error=>{console.error(error.stack||error);fs.writeFileSync(path.join(out,'settings-failure.txt'),String(error.stack||error));process.exitCode=1;}).finally(async()=>{
  if(browser)await browser.close();
  if(meta){try{await fetch(meta.url+'/__fixture/shutdown',{signal:AbortSignal.timeout(3000)});}catch{}}
  if(fixture){await pause(300);if(fixture.exitCode===null)fixture.kill();}
});
