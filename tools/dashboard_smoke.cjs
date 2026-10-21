// Browser acceptance against the real local admin API, seeded SQLite and fake
// broker. Uses playwright-core directly when the cached CLI daemon cannot run
// in this Windows sandbox. No @playwright/test or production credentials.
const fs = require('node:fs');
const path = require('node:path');
const assert = require('node:assert/strict');
const {spawn} = require('node:child_process');
const root = path.resolve(__dirname, '..');
const out = path.join(root, 'output', 'playwright');
fs.mkdirSync(out, {recursive: true});
for(const name of [process.argv.includes('--isp-only')?'isp-acceptance.json':'acceptance.json','failure.txt'])fs.rmSync(path.join(out,name),{force:true});
function findCore() {
  if (process.env.PLAYWRIGHT_CORE_PATH) return require(process.env.PLAYWRIGHT_CORE_PATH);
  const cache = path.join(process.env.LOCALAPPDATA, 'npm-cache', '_npx');
  for (const name of fs.readdirSync(cache)) {
    const dir = path.join(cache, name, 'node_modules', 'playwright-core');
    if (fs.existsSync(path.join(dir, 'package.json'))) return require(dir);
  }
  throw Error('Set PLAYWRIGHT_CORE_PATH to an installed playwright-core directory');
}
function chromePath() {
  if (process.env.DASHBOARD_CHROME) return process.env.DASHBOARD_CHROME;
  const cache = path.join(process.env.LOCALAPPDATA, 'ms-playwright');
  for (const name of fs.readdirSync(cache).filter(n => /^chromium-/.test(n)).sort().reverse()) {
    for (const rel of ['chrome-win64/chrome.exe', 'chrome-win/chrome.exe']) {
      const exe = path.join(cache, name, rel);
      if (fs.existsSync(exe)) return exe;
    }
  }
  throw Error('Set DASHBOARD_CHROME to a local Chromium executable');
}
const pause = ms => new Promise(r => setTimeout(r, ms));
const checks = [];
let browser, fixture, fixtureMeta;
function ok(name) { checks.push(name); console.log('PASS ' + name); }
async function snapshot(page, name) {
  fs.writeFileSync(path.join(out, name + '.txt'), await page.locator('body').innerText());
}
async function noPageOverflow(page) {
  const result = await page.evaluate(() => ({viewport: innerWidth, width: document.documentElement.scrollWidth}));
  assert.ok(result.width <= result.viewport + 1, JSON.stringify(result));
}
(async () => {
  const log = fs.openSync(path.join(out, 'fixture-launch.log'), 'w');
  const started = Date.now();
  fixture = spawn(process.execPath, [path.join(__dirname, 'dashboard_fixture.cjs')], {
    cwd: root, windowsHide: true, stdio: ['ignore', log, log]
  });
  fs.closeSync(log);
  let meta;
  for (let i = 0; i < 150; i++) {
    if (fixture.exitCode !== null) throw Error('Fixture exited: see fixture-launch.log');
    try {
      const file = path.join(out, 'fixture.json');
      if (fs.statSync(file).mtimeMs >= started) { meta = JSON.parse(fs.readFileSync(file)); break; }
    } catch {}
    await pause(100);
  }
  assert.ok(meta, 'Fixture startup timed out');
  fixtureMeta=meta;
  const headers = {Authorization: 'Bearer ' + meta.token};
  const data = await fetch(meta.url + '/api/devices', {headers}).then(r => r.json());
  assert.equal(data.length, 8);
  assert.ok(data.some(n => n.chip === 'esp32c3' && n.max_users === 1));
  const history = await fetch(meta.url + '/api/metrics?entity=fleet&range=30d&step=1d&fields=bytes_down,bytes_up', {headers}).then(r => r.json());
  assert.ok(history.series.bytes_down.some(v => v > 0));
  ok('real API, isolated migrated SQLite, C3 seat and nonzero history');
  browser = await findCore().chromium.launch({executablePath: chromePath(), headless: true});
  const page = await browser.newPage({viewport: {width: 1440, height: 900}, deviceScaleFactor: 1});
  const errors = [];
  page.on('pageerror', error => errors.push(error.message));
  await page.goto(meta.url);
  await snapshot(page, 'login');
  await page.locator('#tok').fill(meta.token);
  await page.locator('#loginBtn').click();
  await page.locator('#ov[data-loaded="true"]').waitFor();
  assert.equal(await page.locator('.stat').count(), 4);
  await page.locator('#trafficChart .chart').waitFor();
  await page.locator('#trafficChart .chart').focus();
  await page.keyboard.press('ArrowRight');
  assert.match(await page.locator('.chart-readout').first().innerText(), /Downloaded/);
  await page.locator('.head h2').click();
  await page.screenshot({path: path.join(out, 'overview-desktop.png'), fullPage: true});
  await noPageOverflow(page);
  ok('Overview four KPIs, nonzero chart, keyboard chart inspection');
  const chartCases=await page.evaluate(()=>{
    const host=document.createElement('div');document.body.appendChild(host);
    const series=(down,up)=>({from:1700000000,step_sec:300,n:down.length,series:{bytes_down:down,bytes_up:up}});
    renderChart(host,series([null,null],[null,null]));const empty=host.textContent.includes('No history yet')&&!host.querySelector('svg');
    renderChart(host,series([0],[0]));const onePoint=host.querySelectorAll('circle').length===2;
    renderChart(host,series([1,null,3],[2,null,4]));const gaps=host.querySelectorAll('polyline').length===4;
    host.remove();return {empty,onePoint,gaps};
  });
  assert.deepEqual(chartCases,{empty:true,onePoint:true,gaps:true});
  ok('chart distinguishes no history, zero single sample and real observation gaps');
  await page.locator('nav a[data-view="devices"]').click();
  await page.locator('#dtbl[data-loaded="true"]').waitFor();
  await snapshot(page, 'devices-desktop');
  assert.equal(await page.locator('#dtbl thead th').count(), 7);
  assert.equal(await page.locator('#dtbl tbody tr').count(), 8);
  await page.waitForFunction(()=>[...document.querySelectorAll('.isp-logo')].every(img=>img.complete&&img.naturalWidth>0));
  const brandIDs=await page.locator('[data-isp]').evaluateAll(items=>[...new Set(items.map(el=>el.dataset.isp))].sort());
  assert.deepEqual(brandIDs,['3bb','ais','dtac','nt','true']);
  assert.ok((await page.locator('#dtbl').innerText()).includes('Example Independent ISP'));
  const hints=await page.evaluate(()=>({
    old:healthCell({mac:'old-c3',firmware:'2.4.22-c3',online:true,uptime_sec:22}),
    current:healthCell({mac:'new-c3',firmware:'2.4.23-c3',online:true,uptime_sec:22,link_since:new Date().toISOString()}),
    unknown:ispCell('<img src=x onerror=invalid>'),
    awn:ispBrand('Advanced Wireless Network Company Limited').id,
    similar:ispBrand('NTT Communications')
  }));
  assert.match(hints.old,/Update to 2\.4\.23/);
  assert.match(hints.old,/data-act="ota"/);
  assert.match(hints.current,/Waiting for RAM \/ CPU report/);
  assert.ok(!hints.current.includes('does not report'));
  assert.ok(!hints.unknown.includes('<img'));
  assert.equal(hints.awn,'ais');assert.equal(hints.similar,null);
  const fallback=await page.evaluate(async()=>{
    const box=document.createElement('span');box.innerHTML=ispCell('Advanced Wireless Network Company Limited');document.body.appendChild(box);
    const img=box.querySelector('img');const failed=new Promise(resolve=>img.addEventListener('error',resolve,{once:true}));img.src='/isp/not-present.svg';await failed;
    const result={name:box.textContent,hidden:getComputedStyle(img).display==='none'};box.remove();return result;
  });
  assert.equal(fallback.name,'AIS');assert.equal(fallback.hidden,true);
  ok('five local ISP logos, legal-name aliases, escaped unknown fallback and firmware-aware health hints');
  if(process.argv.includes('--isp-only')){
    await page.screenshot({path:path.join(out,'devices-isp-desktop.png'),fullPage:true});
    fs.writeFileSync(path.join(out,'isp-acceptance.json'),JSON.stringify({passed:checks,browser:await browser.version()},null,2));return;
  }
  await page.screenshot({path: path.join(out, 'devices-desktop.png'), fullPage: true});
  await noPageOverflow(page);
  const first = page.locator('#dtbl tbody tr').first();
  await first.locator('button[data-act="more"]').click();
  await page.locator('#rowmenu button').first().focus();
  await page.evaluate(() => { window.__qaRow = document.querySelector('#dtbl tbody tr'); window.__qaFocus = document.activeElement; });
  await pause(11000);
  assert.equal(await page.locator('#rowmenu').isVisible(), true);
  assert.equal(await page.evaluate(() => window.__qaRow === document.querySelector('#dtbl tbody tr') && window.__qaFocus === document.activeElement), true);
  await page.keyboard.press('Escape');
  ok('real background tick preserves keyed row, menu and focused menu item');
  await first.locator('.device-title').click();
  await page.locator('#ddrawer:not([hidden])').waitFor();
  await page.locator('#tab-ddHistory').click();
  await page.locator('#ddHistory .chart').waitFor();
  await page.locator('#tab-ddDanger').click();
  await page.locator('#ddAssign').focus();
  await page.keyboard.press('Tab');
  assert.equal(await page.evaluate(() => document.activeElement.id), 'ddEdit');
  await snapshot(page, 'drawer-desktop');
  await page.screenshot({path: path.join(out, 'drawer-desktop.png'), fullPage: true});
  await page.locator('#ddEdit').click();
  await page.locator('#dmName').fill('งานค้างทดสอบ refresh');
  await pause(11000);
  assert.equal(await page.locator('#dmName').inputValue(), 'งานค้างทดสอบ refresh');
  assert.equal(await page.locator('#dmodal').isVisible(), true);
  await page.keyboard.press('Escape');
  assert.equal(await page.locator('#ddrawer').isVisible(), true);
  assert.equal(await page.evaluate(() => document.activeElement.id), 'ddEdit');
  await page.locator('#ddEdit').click();
  await page.locator('#dmTabLocation').click();
  await page.locator('#dmClearPos').click();
  await page.locator('#dmSave').click();
  await page.locator('#dmodal').waitFor({state:'hidden'});
  const cleared=await fetch(meta.url+'/api/devices',{headers}).then(r=>r.json());
  const clearedMAC=await page.evaluate(()=>drawerMAC);
  assert.equal(cleared.find(n=>n.mac===clearedMAC).lat,null);
  assert.equal(cleared.find(n=>n.mac===clearedMAC).lon,null);
  await page.keyboard.press('Escape');
  assert.equal(await page.locator('#ddrawer').isVisible(), false);
  ok('drawer/editor stack, unsaved form survives tick, paired coordinates clear, Escape restores focus');
  await page.locator('#devicesSearch').fill('ร้านกาแฟ');
  assert.equal(await page.locator('#dtbl tbody tr:visible').count(), 1);
  await page.locator('#devicesSearch').fill('');
  await page.locator('#dtbl .device-title').nth(1).click();
  await page.locator('#ddrawer:not([hidden])').waitFor();
  await page.goBack();
  await page.waitForFunction(() => document.querySelector('#ddrawer').hidden);
  await page.goForward();
  await page.waitForFunction(() => !document.querySelector('#ddrawer').hidden);
  await page.reload();
  await page.locator('#tab-ddHistory').click();
  await page.locator('#ddHistory .chart').waitFor();
  ok('Thai search, hash Back/Forward and deep-link reload');
  await page.locator('#ddClose').click();
  await page.setViewportSize({width: 375, height: 812});
  await noPageOverflow(page);
  await page.screenshot({path: path.join(out, 'devices-mobile.png')});
  await snapshot(page, 'devices-mobile');
  await page.locator('#dtbl tbody tr').last().locator('button.more').scrollIntoViewIfNeeded();
  await pause(200); // let the scroll event finish before opening the popover
  await page.locator('#dtbl tbody tr').last().locator('button.more').click();
  await page.locator('#rowmenu').waitFor({state: 'visible'});
  const menu = await page.locator('#rowmenu').boundingBox();
  assert.ok(menu.x >= 0 && menu.x + menu.width <= 376 && menu.y >= 0 && menu.y + menu.height <= 813, JSON.stringify(menu));
  await page.keyboard.press('Escape');
  await page.locator('#dtbl .device-title').first().click();
  await page.locator('#tab-ddHistory').click();
  await page.locator('#ddHistory .chart').waitFor();
  await noPageOverflow(page);
  await page.screenshot({path: path.join(out, 'drawer-mobile.png')});
  await page.keyboard.press('Escape');
  await page.locator('nav a[data-view="users"]').click();
  await page.locator('#utbl[data-loaded="true"]').waitFor();
  await noPageOverflow(page);
  await page.screenshot({path: path.join(out, 'users-mobile.png')});
  await page.locator('nav a[data-view="overview"]').click();
  await page.locator('#ov[data-loaded="true"]').waitFor();
  await noPageOverflow(page);
  await page.screenshot({path: path.join(out, 'overview-mobile.png')});
  ok('375×812 cards, full-screen drawer, menu edge clamp and no page overflow');
  await page.emulateMedia({reducedMotion: 'reduce'});
  assert.equal(await page.evaluate(() => matchMedia('(prefers-reduced-motion: reduce)').matches), true);
  await page.locator('nav a[data-view="devices"]').click();
  await page.locator('#dtbl[data-loaded="true"]').waitFor();
  const before = await page.locator('#dtbl tbody').innerText();
  await fetch(meta.url + '/__fixture/control?apiFailures=1');
  await page.locator('[data-refresh]').click();
  await page.waitForFunction(() => document.querySelector('#updatedLabel').classList.contains('stale'));
  assert.equal(await page.locator('#dtbl tbody').innerText(), before);
  await fetch(meta.url + '/__fixture/control?apiFailures=0');
  await page.locator('[data-refresh]').click();
  await page.waitForFunction(() => !document.querySelector('#updatedLabel').classList.contains('stale'));
  ok('reduced motion preference, API error keeps rows, retry recovers');
  await fetch(meta.url + '/__fixture/control?unauthorized=1');
  await page.locator('[data-refresh]').click();
  await page.locator('#login').waitFor({state: 'visible'});
  assert.equal(await page.evaluate(() => localStorage.getItem('adm_tok')), null);
  const countBefore = await fetch(meta.url + '/__fixture/control').then(r => r.json());
  await pause(11000);
  const countAfter = await fetch(meta.url + '/__fixture/control').then(r => r.json());
  assert.equal(countBefore.apiRequests, countAfter.apiRequests);
  assert.deepEqual(errors, []);
  ok('401 clears auth and stops polling; no uncaught browser exceptions');
  fs.writeFileSync(path.join(out, 'acceptance.json'), JSON.stringify({passed: checks, browser: await browser.version(), fixture: 'real admin-api + isolated seeded SQLite + fake broker/geo', screenshots: fs.readdirSync(out).filter(n => n.endsWith('.png'))}, null, 2));
})().catch(error => {
  console.error(error.stack || error);
  fs.writeFileSync(path.join(out, 'failure.txt'), String(error.stack || error));
  process.exitCode = 1;
}).finally(async () => {
  if(browser)await browser.close();
  if(fixtureMeta){try{await fetch(fixtureMeta.url+'/__fixture/shutdown',{signal:AbortSignal.timeout(3000)});}catch{}}
  if(fixture){await pause(300);if(fixture.exitCode===null)fixture.kill();}
});
