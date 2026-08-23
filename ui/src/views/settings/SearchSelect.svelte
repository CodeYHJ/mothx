<script>
  import { createEventDispatcher, tick } from 'svelte';
  import { t } from '../../lib/preferences.js';
  import { Input } from '$lib/components/ui/input/index.js';
  import { Button } from '$lib/components/ui/button/index.js';
  import { ChevronsUpDown } from '@lucide/svelte';

  export let value = '';
  export let options = [];
  export let placeholder = '';
  export let ariaLabel = '';
  export let disabled = false;
  export let className = '';
  export let closeLabel = '';
  export let openLabel = '';
  export let noOptionsLabel = '';

  const dispatch = createEventDispatcher();
  let query = '';
  let open = false;
  let input;
  let root;

  $: selected = options.find((option) => option.value === value);
  $: displayValue = open ? query : (selected?.label || value || '');
  $: normalizedQuery = query.trim().toLowerCase();
  $: filteredOptions = normalizedQuery
    ? options.filter((option) => `${option.label} ${option.value}`.toLowerCase().includes(normalizedQuery))
    : options;

  function openPicker() {
    if (disabled) return;
    query = '';
    open = true;
    tick().then(() => {
      if (open) input?.focus();
    });
  }

  function closePicker() {
    open = false;
    query = '';
  }

  function choose(option) {
    value = option.value;
    closePicker();
    dispatch('change', value);
  }

  function handleWindowClick(event) {
    if (!root?.contains(event.target)) closePicker();
  }

  function handleKeydown(event) {
    if (event.key === 'Escape') {
      closePicker();
      return;
    }
    if (event.key === 'Enter' && filteredOptions.length > 0) {
      event.preventDefault();
      choose(filteredOptions[0]);
    }
  }
</script>

<div bind:this={root} class={`search-select ${className}`} class:open class:disabled>
  <Input
    bind:this={input}
    value={displayValue}
    {placeholder}
    aria-label={ariaLabel}
    aria-expanded={open}
    aria-haspopup="listbox"
    autocomplete="off"
    {disabled}
    onfocus={openPicker}
    oninput={(event) => { query = event.currentTarget.value; open = true; }}
    onkeydown={handleKeydown}
  />
  <Button
    type="button"
    variant="ghost"
    size="icon-xs"
    class="search-select-chevron"
    aria-label={open ? (closeLabel || $t('common.close')) : (openLabel || $t('common.open'))}
    {disabled}
    onclick={() => { open ? closePicker() : openPicker(); }}
  >
    <ChevronsUpDown size={14} aria-hidden="true" />
  </Button>
  {#if open}
    <div class="search-select-menu" role="listbox" tabindex="-1">
      {#if filteredOptions.length === 0}
        <div class="search-select-empty">{noOptionsLabel || $t('common.noMatchingOptions')}</div>
      {:else}
        {#each filteredOptions as option (option.value)}
          <button
            type="button"
            class:active={option.value === value}
            role="option"
            aria-selected={option.value === value}
            on:mousedown={(event) => { event.preventDefault(); choose(option); }}
          >{option.label}</button>
        {/each}
      {/if}
    </div>
  {/if}
</div>

<svelte:window on:click={handleWindowClick} />
