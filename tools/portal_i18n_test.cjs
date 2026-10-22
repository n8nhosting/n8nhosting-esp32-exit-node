'use strict';
// Focused contract checks for locale completeness, parameter safety and routing.
const assert=require('node:assert/strict'),fs=require('node:fs'),path=require('node:path'),vm=require('node:vm');
const base=path.join(__dirname,'..','admin-api','portal'),languages=['th','en','zh-CN','ja','ru','vi'];
const dictionaries=Object.fromEntries(languages.map(lang=>[lang,JSON.parse(fs.readFileSync(path.join(base,'locales',lang+'.json'),'utf8'))]));
const source=fs.readFileSync(path.join(base,'portal.js'),'utf8'),shell=fs.readFileSync(path.join(base,'index.html'),'utf8');
const keys=Object.keys(dictionaries.en).sort();let groups=0;
for(const language of languages){
  assert.deepEqual(Object.keys(dictionaries[language]).sort(),keys,language+' key parity');
  for(const key of keys){
    const value=dictionaries[language][key];assert.ok(typeof value==='string'&&value.trim(),language+' '+key);
    assert.deepEqual([...value.matchAll(/\{([a-z_]+)\}/g)].map(x=>x[1]).sort(),[...dictionaries.en[key].matchAll(/\{([a-z_]+)\}/g)].map(x=>x[1]).sort(),language+' '+key+' placeholders');
    assert.ok(!/[<>]/.test(value),'Translations must be plain text: '+key);
  }
}groups++;
const used=[...source.matchAll(/(?<![\w.])t\((['"])(.*?)\1/g)].map(match=>match[2]).filter(key=>!key.endsWith('.'));
used.push(...[...shell.matchAll(/data-i18n(?:-aria)?="([^"]+)"/g)].map(match=>match[1]));
for(const key of used)assert.ok(keys.includes(key),'Missing UI key '+key);groups++;
for(const language of languages)for(const page of ['home','plans','help'])for(const field of ['title','description','keywords','h1','intro','body','faq.question','faq.answer'])assert.ok(dictionaries[language]['seo.'+page+'.'+field]);groups++;
async function boot(pathname,stored=''){
  const storage=new Map(stored?[['portal_locale',stored]]:[]),calls=[],assigned=[];
  const sandbox={window:{},location:{pathname,hash:'',assign:value=>assigned.push(value)},document:{documentElement:{},querySelectorAll:()=>[]},localStorage:{getItem:key=>storage.get(key),setItem:(key,value)=>storage.set(key,value)},fetch:async url=>{calls.push(url);const lang=url.split('/').at(-1).replace('.json','');return {ok:true,json:async()=>dictionaries[lang]};},AbortController,setTimeout,clearTimeout};
  vm.createContext(sandbox);vm.runInContext(fs.readFileSync(path.join(base,'i18n.js'),'utf8'),sandbox);await sandbox.window.PortalI18n.ready;
  return {i:sandbox.window.PortalI18n,sandbox,storage,calls,assigned};
}
(async()=>{
  for(const language of languages){const {i,sandbox,calls,assigned,storage}=await boot('/portal/'+language+'/help','th');assert.equal(i.locale,language);assert.equal(i.page(),'help');assert.equal(i.path('home'),'/portal/'+language+'/');assert.equal(i.path('plans'),'/portal/'+language+'/plans');assert.equal(sandbox.document.documentElement.lang,language);assert.deepEqual(calls,['/portal/locales/'+language+'.json']);assert.ok(!i.t('ui.count.days',{count:'42'}).includes('{count}'));sandbox.location.pathname='/portal/'+language+'/orders';assert.equal(i.page(),'orders','current location must be read after History API navigation');i.setLocale('en');assert.equal(assigned.at(-1),'/portal/en/orders');assert.equal(storage.get('portal_locale'),'en');assert.equal(i.setLocale('xx'),false);}
  groups++;
  const remembered=await boot('/portal/','ru');assert.equal(remembered.i.locale,'ru');const fallback=await boot('/portal/','unsupported');assert.equal(fallback.i.locale,'th');groups++;
  assert.match(source,/state\.customer\|\|!state\.authKnown\|\|privacyBlocked/);assert.match(source,/history\.replaceState\(null,'',I18n\.path\(initialPage\)\)/);assert.match(source,/sessionStorage\.setItem\('portal_plan_choice',state\.selection\.plan_id\)/);assert.ok(!/sessionStorage\.setItem\([^\n]*(?:password|email|csrf)/.test(source));groups++;
  console.log(JSON.stringify({ok:true,groups,languages,keys_per_locale:keys.length,total_translations:keys.length*languages.length,referenced_ui_keys:new Set(used).size}));
})().catch(error=>{console.error(error);process.exitCode=1;});
