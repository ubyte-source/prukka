<script lang="ts">
  import { i18n } from "../lib/i18n/index.svelte";
</script>

<footer class="mt-auto flex flex-col gap-2 border-t border-line pt-4 text-xs text-ink-dim">
  <p class="text-sm">{i18n.m.footer.privacy}</p>
  <nav aria-label={i18n.m.footer.resources} class="flex flex-wrap gap-x-3 gap-y-1">
    <a
      href="https://github.com/ubyte-source/prukka/blob/main/docs/GDPR.md"
      rel="noreferrer"
      class="text-brand underline-offset-2 hover:underline"
    >{i18n.m.footer.dataProtection}</a>
    <a
      href="https://github.com/ubyte-source/prukka/blob/main/docs/ACCESSIBILITY.md"
      rel="noreferrer"
      class="text-brand underline-offset-2 hover:underline"
    >{i18n.m.footer.accessibility}</a>
    <a
      href="https://github.com/ubyte-source/prukka"
      rel="noreferrer"
      class="text-brand underline-offset-2 hover:underline"
    >{i18n.m.footer.source}</a>
  </nav>
</footer>
