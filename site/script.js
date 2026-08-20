(() => {
  const storageKey = 'piktak-language';
  const descriptions = {
    en: 'PIK.TAK pairs remote clients with services running on your machine.',
    zh: 'PIK.TAK 把远程客户端接到运行在电脑上的本地服务。',
  };
  const titles = {
    en: 'PIK.TAK — Pair with localhost',
    zh: 'PIK.TAK — 连接本地服务',
  };

  function preferredLanguage() {
    const stored = localStorage.getItem(storageKey);
    if (stored === 'en' || stored === 'zh') return stored;
    return navigator.language.toLowerCase().startsWith('zh') ? 'zh' : 'en';
  }

  function setLanguage(language) {
    const lang = language === 'zh' ? 'zh' : 'en';
    document.documentElement.lang = lang === 'zh' ? 'zh-CN' : 'en';
    document.documentElement.dataset.language = lang;
    document.title = titles[lang];
    const description = document.querySelector('meta[name="description"]');
    if (description) description.content = descriptions[lang];
    const toggle = document.querySelector('[data-language-toggle]');
    if (toggle) {
      toggle.textContent = lang === 'zh' ? 'EN' : '中文';
      toggle.setAttribute('aria-label', lang === 'zh' ? 'Switch to English' : '切换到中文');
    }
    localStorage.setItem(storageKey, lang);
  }

  setLanguage(preferredLanguage());
  document.querySelector('[data-language-toggle]')?.addEventListener('click', () => {
    setLanguage(document.documentElement.dataset.language === 'zh' ? 'en' : 'zh');
  });
})();
