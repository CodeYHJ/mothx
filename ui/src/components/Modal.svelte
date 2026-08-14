<script>
  import { createEventDispatcher, tick } from 'svelte';
  import { onDestroy } from 'svelte';

  export let open = false;
  export let title = '';
  export let labelledBy = '';
  export let closeOnOverlay = true;
  export let closeOnEscape = true;
  export let className = '';

  const dispatch = createEventDispatcher();
  let root;
  let previousActive = null;
  let previousOverflow = '';

  $: if (open) activate();
  $: if (!open && previousActive) deactivate();

  async function activate() {
    await tick();
    if (!root) return;
    previousActive ||= document.activeElement;
    previousOverflow ||= document.body.style.overflow;
    document.body.style.overflow = 'hidden';
    focusFirst();
  }

  function deactivate() {
    document.body.style.overflow = previousOverflow;
    previousOverflow = '';
    const target = previousActive;
    previousActive = null;
    if (target && typeof target.focus === 'function' && document.contains(target)) target.focus();
  }

  function close() {
    dispatch('close');
  }

  function handleKeydown(event) {
    if (event.key === 'Escape' && closeOnEscape) {
      event.preventDefault();
      close();
      return;
    }
    if (event.key !== 'Tab' || !root) return;
    const focusable = [...root.querySelectorAll('button, [href], input, select, textarea, [tabindex]:not([tabindex="-1"])')]
      .filter((item) => !item.disabled && item.offsetParent !== null);
    if (focusable.length === 0) return;
    const first = focusable[0];
    const last = focusable[focusable.length - 1];
    if (event.shiftKey && document.activeElement === first) {
      event.preventDefault();
      last.focus();
    } else if (!event.shiftKey && document.activeElement === last) {
      event.preventDefault();
      first.focus();
    }
  }

  function focusFirst() {
    const first = root?.querySelector('button, input, select, textarea, [tabindex]:not([tabindex="-1"])');
    first?.focus();
  }

  onDestroy(() => {
    if (open) deactivate();
  });
</script>

{#if open}
  <div
    bind:this={root}
    class="modal-overlay {className}"
    role="dialog"
    aria-modal="true"
    aria-label={labelledBy ? undefined : title}
    aria-labelledby={labelledBy || undefined}
    tabindex="-1"
    on:keydown={handleKeydown}
    on:click={(event) => closeOnOverlay && event.currentTarget === event.target && close()}
  >
    <slot />
  </div>
{/if}

<style>
  .modal-overlay { position: fixed; inset: 0; z-index: 50; }
</style>
