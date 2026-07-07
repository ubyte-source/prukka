<script lang="ts">
  import { daemon } from "./lib/state/daemon.svelte";
  import Doctor from "./sections/Doctor.svelte";
  import EventLog from "./sections/EventLog.svelte";
  import Footer from "./sections/Footer.svelte";
  import Header from "./sections/Header.svelte";
  import Sessions from "./sections/Sessions.svelte";
  import Settings from "./sections/Settings.svelte";
  import Wizard from "./sections/Wizard.svelte";

  $effect(() => daemon.start());
</script>

<div class="mx-auto flex min-h-screen max-w-6xl flex-col gap-6 px-4 pb-8 sm:px-6">
  <Header />

  <main class="flex flex-col gap-6">
    <Wizard />
    <Sessions />
    <Settings />
    <div class="grid gap-6 lg:grid-cols-2">
      <Doctor />
      <EventLog />
    </div>
  </main>

  <Footer />
</div>
