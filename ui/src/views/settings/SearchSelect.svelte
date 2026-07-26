<script>
  import { createEventDispatcher, tick } from 'svelte';

  export let value = '';
  export let options = [];
  export let placeholder = '';
  export let ariaLabel = '';
  export let disabled = false;
  export let className = '';

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
    tick().then(() => input?.focus());
  }

  function closePicker() {
    open = false;
    query = '';
  }

  function choose(option) {
    value = option.value;
    dispatch('change', value);
    closePicker();
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
  <input
    bind:this={input}
    class="search-select-input"
    value={displayValue}
    placeholder={placeholder}
    aria-label={ariaLabel}
    aria-expanded={open}
    aria-haspopup="listbox"
    autocomplete="off"
    {disabled}
    on:focus={openPicker}
    on:input={(event) => { query = event.currentTarget.value; open = true; }}
    on:keydown={handleKeydown}
  />
  <button type="button" class="search-select-chevron" aria-label={open ? 'Close' : 'Open'} disabled={disabled} on:click|stopPropagation={() => (open ? closePicker() : openPicker())}>⌄</button>
  {#if open}
    <div class="search-select-menu" role="listbox" tabindex="-1">
      {#if filteredOptions.length === 0}
        <div class="search-select-empty">No matching options</div>
      {:else}
        {#each filteredOptions as option (option.value)}
          <button
            type="button"
            class:active={option.value === value}
            role="option"
            aria-selected={option.value === value}
            on:click={() => choose(option)}
          >{option.label}</button>
        {/each}
      {/if}
    </div>
  {/if}
</div>

<svelte:window on:click={handleWindowClick} />
