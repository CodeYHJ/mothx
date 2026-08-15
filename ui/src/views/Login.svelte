<script>
  import { createEventDispatcher } from 'svelte';
  import { request, jsonBody } from '../lib/api.js';
  import { t } from '../lib/preferences.js';

  const dispatch = createEventDispatcher();
  let password = '';
  let submitting = false;
  let error = '';

  async function submit() {
    if (submitting) return;
    error = '';
    if (!password) {
      error = $t('login.required');
      return;
    }
    submitting = true;
    try {
      await request('/api/auth/login', { method: 'POST', ...jsonBody({ password }) });
      password = '';
      dispatch('authenticated');
    } catch (err) {
      error = err instanceof Error ? err.message : $t('login.failed');
    } finally {
      submitting = false;
    }
  }
</script>

<main class="login-page">
  <section class="login-card" aria-labelledby="login-title">
    <div class="login-brand" aria-hidden="true">M</div>
    <p class="login-eyebrow">MothX Serve</p>
    <h1 id="login-title">{$t('login.title')}</h1>
    <p class="login-hint">{$t('login.hint')}</p>

    <form on:submit|preventDefault={submit}>
      <label for="serve-password">
        <span>{$t('login.password')}</span>
        <input
          id="serve-password"
          type="password"
          bind:value={password}
          autocomplete="current-password"
          disabled={submitting}
        />
      </label>
      {#if error}
        <p class="login-error" role="alert">{error}</p>
      {/if}
      <button class="primary login-submit" type="submit" disabled={submitting}>
        {submitting ? $t('login.submitting') : $t('login.submit')}
      </button>
    </form>
  </section>
</main>
