<script>
  import { t } from '../../lib/preferences.js';
  import { Button } from '$lib/components/ui/button/index.js';

  export let title = '';
  export let list = [];
  export let onAdd = () => {};
  export let onRemove = () => {};
</script>

<div class="list-editor full">
  <div class="list-head">
    <span>{title}</span>
    <Button variant="ghost" size="sm" type="button" on:click={onAdd}>{$t('common.add')}</Button>
  </div>
  {#each list as item, i (i)}
    <div class="inline-row">
      <input bind:value={list[i]} />
      <Button variant="ghost" size="sm" type="button" on:click={() => onRemove(i)}>{$t('common.remove')}</Button>
    </div>
  {/each}
</div>
