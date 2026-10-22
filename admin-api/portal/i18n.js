'use strict';
// Translation keys describe fixed UI copy only. Account and connection data never
// enter dictionaries, browser storage, URLs or analytics context.
window.PortalI18n=(()=>{
  const supported=['th','en','zh-CN','ja','ru','vi'];
  const labels={th:'ไทย',en:'English','zh-CN':'简体中文',ja:'日本語',ru:'Русский',vi:'Tiếng Việt'};
  const pathParts=location.pathname.split('/').filter(Boolean);
  let remembered='';try{remembered=localStorage.getItem('portal_locale')||'';}catch{}
  const locale=supported.includes(pathParts[1])?pathParts[1]:(supported.includes(remembered)?remembered:'th');
  let dictionary={};
  const page=()=>{
    const pathParts=location.pathname.split('/').filter(Boolean);
    const hashPage=location.hash.match(/^#\/?([a-z]+)/)?.[1];
    const pathPage=(supported.includes(pathParts[1])?pathParts[2]:pathParts[1])||'home';
    return hashPage||(pathPage==='index.html'?'home':pathPage);
  };
  const path=(name='home',language=locale)=>'/portal/'+(supported.includes(language)?language:'th')+'/'+(name==='home'?'':encodeURIComponent(name));
  const t=(key,params={})=>String(dictionary[key]??key).replace(/\{([a-zA-Z0-9_]+)\}/g,(_,name)=>String(params[name]??'{'+name+'}'));
  const ready=(async()=>{
    const controller=new AbortController(),timer=setTimeout(()=>controller.abort(),10000);
    try{
      const response=await fetch('/portal/locales/'+locale+'.json',{credentials:'omit',signal:controller.signal});
      if(!response.ok)throw new Error('locale_unavailable');
      const data=await response.json();
      if(!data||Array.isArray(data)||typeof data!=='object'||Object.values(data).some(v=>typeof v!=='string'))throw new Error('locale_invalid');
      dictionary=data;document.documentElement.lang=locale;document.documentElement.dir='ltr';
    }catch(error){
      const failure={th:'โหลดภาษาไม่สำเร็จ กรุณาตรวจการเชื่อมต่อแล้วลองอีกครั้ง',en:'Language files could not load. Check your connection and try again.','zh-CN':'无法加载语言文件，请检查网络并重试。',ja:'言語ファイルを読み込めません。接続を確認して再試行してください。',ru:'Не удалось загрузить язык. Проверьте соединение и повторите.',vi:'Không tải được ngôn ngữ. Kiểm tra kết nối và thử lại.'};
      const page=document.querySelector('#page');if(page){page.replaceChildren();const message=document.createElement('p');message.className='error';message.setAttribute('role','alert');message.textContent=failure[locale];const retry=document.createElement('button');retry.type='button';retry.className='secondary';retry.textContent={th:'ลองอีกครั้ง',en:'Try again','zh-CN':'重试',ja:'再試行',ru:'Повторить',vi:'Thử lại'}[locale];retry.addEventListener('click',()=>location.reload());page.append(message,retry);}throw error;
    }finally{clearTimeout(timer);}
  })();
  function apply(root=document){
    root.querySelectorAll('[data-i18n]').forEach(node=>{node.textContent=t(node.dataset.i18n);});
    root.querySelectorAll('[data-i18n-aria]').forEach(node=>node.setAttribute('aria-label',t(node.dataset.i18nAria)));
    document.querySelectorAll('[data-page]').forEach(link=>link.href=path(link.dataset.page));
  }
  function setLocale(language,name=page()){
    if(!supported.includes(language))return false;
    try{localStorage.setItem('portal_locale',language);}catch{}
    location.assign(path(name,language));return true;
  }
  return {supported,labels,locale,page,path,t,ready,apply,setLocale};
})();
