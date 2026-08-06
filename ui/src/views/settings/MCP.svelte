<script>
  import MCPConfigEditor from '../../components/MCPConfigEditor.svelte';
  import { t } from '../../lib/preferences.js';
</script>

<MCPConfigEditor
  endpoint="/api/mcp"
  title={$t('settings.mcp.globalTitle')}
  hint={$t('settings.mcp.globalHint')}
/>
