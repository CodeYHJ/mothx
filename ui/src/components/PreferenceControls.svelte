<script>
  import { language, themeMode, t, setLanguage, setThemeMode } from '../lib/preferences.js';

  const themeModes = ['light', 'dark', 'auto'];

  function toggleLanguage() {
    setLanguage($language === 'zh' ? 'en' : 'zh');
  }

  function toggleTheme() {
    const currentIndex = themeModes.indexOf($themeMode);
    setThemeMode(themeModes[(currentIndex + 1) % themeModes.length]);
  }
</script>

<div class="pref-controls" aria-label={$t('prefs.preferences')}>
  <button
    type="button"
    class="pref-icon"
    aria-label={$t('prefs.language')}
    title={`${$t('prefs.language')}: ${$t($language === 'zh' ? 'prefs.en' : 'prefs.zh')}`}
    on:click={toggleLanguage}
  >
    <svg aria-hidden="true" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round">
      <circle cx="12" cy="12" r="9"></circle>
      <path d="M3 12h18M12 3a14 14 0 0 1 0 18M12 3a14 14 0 0 0 0 18"></path>
    </svg>
    <span class="pref-icon-value">{$t($language === 'zh' ? 'prefs.zh' : 'prefs.en')}</span>
  </button>

  <button
    type="button"
    class="pref-icon"
    aria-label={$t('prefs.theme')}
    title={`${$t('prefs.theme')}: ${$t(`prefs.${$themeMode}`)}`}
    on:click={toggleTheme}
  >
    <svg aria-hidden="true" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round">
      <circle cx="12" cy="12" r="3.5"></circle>
      <path d="M12 2.5v2M12 19.5v2M21.5 12h-2M4.5 12h-2M18.7 5.3l-1.4 1.4M6.7 17.3l-1.4 1.4M18.7 18.7l-1.4-1.4M6.7 6.7 5.3 5.3"></path>
    </svg>
    <span class="pref-icon-value">{$t(`prefs.${$themeMode}`)}</span>
  </button>
</div>
