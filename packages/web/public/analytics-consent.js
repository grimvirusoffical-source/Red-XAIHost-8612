'use strict';

(()=>{
  let loaded=false;
  const current=document.currentScript;
  const load=()=>{
    if(loaded)return;
    loaded=true;
    const script=document.createElement('script');
    script.defer=true;
    script.src='/runable.js';
    script.dataset.hostname=current?.dataset.hostname||'localhost';
    script.dataset.url=current?.dataset.url||'https://r.lilstts.com/events';
    if(current?.dataset.debug)script.dataset.debug=current.dataset.debug;
    document.head.appendChild(script);
  };
  const choice=window.__redxaiAnalyticsConsent;
  if(choice==='yes')load();
  window.addEventListener('redxai-analytics-consent',event=>{
    if(event?.detail?.allowed===true)load();
  });
})();
