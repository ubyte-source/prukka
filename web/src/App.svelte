<script lang="ts">
  import Toasts from "./lib/components/Toasts.svelte";
  import { i18n } from "./lib/i18n/index.svelte";
  import { daemon } from "./lib/state/daemon.svelte";
  import { isControlToken, token } from "./lib/state/token.svelte";
  import Doctor from "./sections/Doctor.svelte";
  import EventLog from "./sections/EventLog.svelte";
  import Footer from "./sections/Footer.svelte";
  import Header from "./sections/Header.svelte";
  import Languages from "./sections/Languages.svelte";
  import Sessions from "./sections/Sessions.svelte";
  import Settings from "./sections/Settings.svelte";
  import Wizard from "./sections/Wizard.svelte";

  const controlToken = $derived(isControlToken(token.value) ? token.value : "");

  $effect(() => daemon.start(controlToken));
</script>

<a
  href="#main-content"
  class="fixed top-2 left-2 z-50 -translate-y-20 rounded-lg bg-brand-dim px-4 py-2
         text-sm font-semibold text-white transition-transform focus:translate-y-0"
>
  {i18n.m.skipToContent}
</a>

<div class="mx-auto flex min-h-screen max-w-6xl flex-col gap-6 px-4 pb-8 sm:px-6">
  <Header />

  <main id="main-content" tabindex="-1" class="flex scroll-mt-20 flex-col gap-6">
    <Wizard />
    <Sessions />
    <Settings />
    <Languages />
    <div class="grid grid-cols-1 gap-6 lg:grid-cols-2">
      <Doctor />
      <EventLog />
    </div>
  </main>

  <Footer />
  <Toasts />
</div>
