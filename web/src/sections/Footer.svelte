<script lang="ts">
  // Static privacy footer: states exactly what leaves the machine.
  import { i18n } from "../lib/i18n/index.svelte";
</script>

<footer class="mt-auto border-t border-line pt-4 text-xs text-ink-dim">
  {i18n.m.footer.privacy}
  <a
    href="https://github.com/ubyte-source/prukka"
    class="text-brand hover:underline">{i18n.m.footer.source}</a
  >
</footer>
