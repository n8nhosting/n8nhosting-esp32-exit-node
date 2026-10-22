'use strict';
// Only public, signed-out documents may load vendor code. Account transitions
// reload the document; SDK listeners cannot follow a customer into their account.
(() => {
  const storageKey = 'portal_marketing_consent_v1';
  const publicPages = new Set(['home', 'plans', 'help']);
  let config = null, context = null, loaded = false, preferencesOpen = false;
  const i18n = () => window.PortalI18n;
  const text = key => i18n()?.t('consent.' + key) || key;
  function choice() {
    try {
      const value = JSON.parse(localStorage.getItem(storageKey));
      if (value && value.version === config.config_version && Number.isFinite(value.at) &&
          Date.now() - value.at < 90 * 86400000 && value.at <= Date.now() &&
          typeof value.analytics === 'boolean' && typeof value.advertising === 'boolean') return value;
    } catch {}
    return null;
  }
  function eligible() {
    if (!context || context.authenticated !== false || !publicPages.has(context.page)) return false;
    // Legacy verify links are never eligible, including before token scrubbing.
    if (location.search || location.hash) return false;
    return /^\/portal\/(th|en|zh-CN|ja|ru|vi)\/(?:plans\/?|help\/?)?$/.test(location.pathname);
  }
  function configured() {
    return config?.enabled && Object.values(config.analytics || {}).some(Boolean);
  }
  function addScript(src, onload) {
    const script = document.createElement('script');
    script.src = src; script.async = true;
    if (onload) script.onload = onload;
    script.onerror = () => { document.documentElement.dataset.measurement = 'unavailable'; };
    document.head.appendChild(script);
  }
  const cleanLocation = () => location.origin + location.pathname;
  function denyLoaded() {
    if (typeof window.gtag === 'function') window.gtag('consent', 'update', {
      analytics_storage: 'denied', ad_storage: 'denied', ad_user_data: 'denied', ad_personalization: 'denied'
    });
    if (typeof window.fbq === 'function') window.fbq('consent', 'revoke');
    if (window.ttq?.revokeConsent) window.ttq.revokeConsent();
  }
  function clearVendorCookies() {
    // Only known optional marketing cookie names; never touch auth/CSRF cookies.
    const host = location.hostname.split('.');
    const domains = ['', ...host.map((_, index) => '.' + host.slice(index).join('.'))];
    for (const cookie of document.cookie.split(';')) {
      const name = cookie.trim().split('=')[0];
      if (!/^(?:_ga(?:_.+)?|_gid|_gat(?:_.+)?|_gcl_.+|_fbp|_fbc|_ttp|ttcsid(?:_.+)?)$/.test(name)) continue;
      for (const domain of domains) for (const path of ['/', '/portal/'])
        document.cookie = name + '=; Max-Age=0; Path=' + path + '; SameSite=Lax; Secure' + (domain ? '; Domain=' + domain : '');
    }
  }
  function save(analytics, advertising) {
    denyLoaded();
    clearVendorCookies();
    try { localStorage.setItem(storageKey, JSON.stringify({version: config.config_version, at: Date.now(), analytics, advertising})); } catch {}
    document.querySelector('#marketingConsent')?.remove();
    preferencesOpen = false;
    if (loaded) {
      // Removing a script does not remove a vendor's timers/listeners.
      location.replace(location.pathname + location.hash);
      return;
    }
    activate({analytics, advertising});
  }
  function panel(expanded = false) {
    if (!configured()) return;
    const old = choice(); preferencesOpen = expanded;
    document.querySelector('#marketingConsent')?.remove();
    const box = document.createElement('section');
    box.id = 'marketingConsent'; box.className = 'marketing-consent';
    box.setAttribute('aria-labelledby', 'marketingConsentTitle');
    const title = document.createElement('h2'); title.id = 'marketingConsentTitle'; title.textContent = text('title');
    const body = document.createElement('p'); body.textContent = text('body'); box.append(title, body);
    if (expanded) {
      for (const group of ['analytics', 'advertising']) {
        const label = document.createElement('label'); label.className = 'consent-option';
        const input = document.createElement('input'); input.type = 'checkbox'; input.name = group; input.checked = !!old?.[group];
        label.append(input, document.createTextNode(text(group))); box.append(label);
      }
    }
    const actions = document.createElement('div'); actions.className = 'actions';
    const button = (label, action, secondary = true) => {
      const node = document.createElement('button'); node.type = 'button'; node.textContent = text(label);
      if (secondary) node.className = 'secondary'; node.addEventListener('click', action); actions.append(node);
    };
    button('reject', () => save(false, false));
    if (expanded) button('save', () => save(box.querySelector('[name="analytics"]').checked, box.querySelector('[name="advertising"]').checked));
    else button('settings', () => panel(true));
    button('accept', () => save(true, true));
    box.append(actions); document.body.append(box);
    if (expanded) box.querySelector('input')?.focus();
  }
  function installControl() {
    if (document.querySelector('#marketingPreferences') || !configured()) return;
    const button = document.createElement('button'); button.type = 'button'; button.id = 'marketingPreferences';
    button.className = 'secondary marketing-preferences'; button.textContent = text('settings');
    button.addEventListener('click', () => panel(true));
    (document.querySelector('footer') || document.body).append(button);
  }
  function activate(consent) {
    if (loaded || !eligible() || !configured() || (!consent.analytics && !consent.advertising)) return;
    loaded = true; document.documentElement.dataset.measurement = 'consented';
    const ids = config.analytics;
    const page = {page_location: cleanLocation(), page_referrer: '', page_title: i18n().t('seo.' + context.page + '.title'), language: context.locale};
    window.dataLayer = [];
    window.gtag = function() { window.dataLayer.push(arguments); };
    window.gtag('consent', 'default', {analytics_storage:'denied',ad_storage:'denied',ad_user_data:'denied',ad_personalization:'denied'});
    window.gtag('consent', 'update', {analytics_storage:consent.analytics?'granted':'denied',ad_storage:consent.advertising?'granted':'denied',ad_user_data:'denied',ad_personalization:'denied'});
    window.gtag('set', 'ads_data_redaction', true);
    window.gtag('set', 'url_passthrough', false);
    if (ids.gtm_id) {
      // GTM can execute operator-managed tags; require BOTH purposes and use no
      // custom HTML input in this product. Operator must enforce consent in GTM.
      if (consent.analytics && consent.advertising) {
        window.dataLayer.push({'gtm.start':Date.now(),event:'gtm.js'});
        addScript('https://www.googletagmanager.com/gtm.js?id=' + encodeURIComponent(ids.gtm_id));
        window.dataLayer.push({event:'portal_public_view',page_path:location.pathname,language:context.locale});
      }
      return;
    }
    const googleID = consent.analytics && ids.ga4_id || consent.advertising && ids.google_ads_id;
    if (googleID) {
      window.gtag('js', new Date());
      addScript('https://www.googletagmanager.com/gtag/js?id=' + encodeURIComponent(googleID));
      if (consent.analytics && ids.ga4_id) {
        window.gtag('config', ids.ga4_id, {...page,send_page_view:false,allow_google_signals:false,allow_ad_personalization_signals:false});
        window.gtag('event', 'page_view', {...page,send_to:ids.ga4_id});
      }
      if (consent.advertising && ids.google_ads_id)
        window.gtag('config', ids.google_ads_id, {...page,send_page_view:false,allow_enhanced_conversions:false,allow_ad_personalization_signals:false});
    }
    if (consent.advertising && ids.meta_pixel_id) {
      const fbq = function() { fbq.callMethod ? fbq.callMethod.apply(fbq, arguments) : fbq.queue.push(arguments); };
      fbq.queue=[]; fbq.loaded=true; fbq.version='2.0'; fbq.push=fbq; fbq.disablePushState=true;
      window.fbq=fbq; window._fbq=fbq;
      fbq('set','autoConfig',false,ids.meta_pixel_id);
      fbq('consent','grant'); fbq('init',ids.meta_pixel_id);
      addScript('https://connect.facebook.net/en_US/fbevents.js');
      fbq('trackSingle',ids.meta_pixel_id,'PageView',{page_path:location.pathname,language:context.locale});
    }
    if (consent.advertising && ids.tiktok_pixel_id) {
      window.TiktokAnalyticsObject='ttq'; const ttq=[]; window.ttq=ttq;
      ttq.methods=['page','track','identify','instances','debug','on','off','once','ready','alias','group','enableCookie','disableCookie','holdConsent','revokeConsent','grantConsent'];
      ttq.setAndDefer=(target,method)=>{ target[method]=function() { target.push([method,...arguments]); }; };
      for (const method of ttq.methods) ttq.setAndDefer(ttq,method);
      ttq._i={}; ttq._t={}; ttq._o={};
      ttq.instance=id=>{ const instance=ttq._i[id]||[]; for (const method of ttq.methods) ttq.setAndDefer(instance,method); return instance; };
      ttq.load=(id,options={})=>{
        const source='https://analytics.tiktok.com/i18n/pixel/events.js';
        ttq._i[id]=[]; ttq._i[id]._u=source; ttq._t[id]=Date.now(); ttq._o[id]=options;
        addScript(source+'?sdkid='+encodeURIComponent(id)+'&lib=ttq');
      };
      ttq.grantConsent();
      ttq.load(ids.tiktok_pixel_id);
      ttq.page({page_path:location.pathname,language:context.locale});
    }
  }
  function update() {
    if (!config || !context || !i18n()) return;
    installControl();
    if (!eligible()) {
      document.querySelector('#marketingConsent')?.remove();
      if (loaded) { denyLoaded(); location.replace(i18n().path(context.page)); }
      return;
    }
    const saved=choice();
    if (!saved) panel(); else if (!preferencesOpen) activate(saved);
  }
  addEventListener('portal:context', event => { context=event.detail; update(); });
  addEventListener('storage', event => { if (event.key===storageKey && loaded) { denyLoaded(); location.reload(); } });
  fetch('/portal/api/marketing', {credentials:'omit',cache:'no-store',headers:{Accept:'application/json'}})
    .then(response => { if (!response.ok) throw new Error(); return response.json(); })
    .then(value => { config=value; update(); }).catch(() => {}); // Consent/config failure stays untracked.
})();
