<script>
  import { Card, CardHeader, CardTitle, CardDescription, CardContent } from '$lib/components/ui/card/index.js';

  export let title = '';
  export let description = '';
  let className = '';
  export { className as class };
</script>

<Card class="settings-card {className}">
  <CardHeader>
    <CardTitle>{title}</CardTitle>
    {#if description}<CardDescription>{description}</CardDescription>{/if}
  </CardHeader>
  <CardContent>
    <slot />
  </CardContent>
</Card>
