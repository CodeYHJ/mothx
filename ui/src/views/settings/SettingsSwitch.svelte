<script>
  import { Switch } from '$lib/components/ui/switch/index.js';

  export let title = '';
  export let description = '';
  export let checked = false;
  export let disabled = false;
</script>

<label class="settings-switch-row">
  <span class="settings-switch-text">
    <span class="settings-switch-title">{title}</span>
    {#if description}<span class="settings-switch-desc">{description}</span>{/if}
  </span>
  <span class="settings-switch-control">
    <Switch bind:checked {disabled} aria-label={title} />
  </span>
</label>
