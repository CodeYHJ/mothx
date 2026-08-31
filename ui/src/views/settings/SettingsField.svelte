<script>
  export let label = '';
  export let hint = '';
  export let className = '';
  let classProp = '';
  export { classProp as class };
</script>

<label class="settings-field {className || classProp}">
  <span class="settings-field-label">{label}</span>
  <slot />
  {#if hint}<span class="settings-field-hint">{hint}</span>{/if}
</label>
