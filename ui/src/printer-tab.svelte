<script lang="ts">
  import { describeFault, type FaultText } from '@prusa/connect-ui/i18n';
  import { FaultAlert, Skeleton } from '@prusa/connect-ui/primitives';

  import { hawkOn, type StatusPayload } from './api';
  import { heldHost } from './host';
  import PrinterView from './printer-view.svelte';

  let { deviceId }: { deviceId: string } = $props();

  const host = heldHost();
  const t = host.t;
  const service = hawkOn(host);

  let status = $state<StatusPayload | undefined>();
  let fault = $state<FaultText | undefined>();

  const load = async () => {
    try {
      status = await service.status(deviceId, host.signal);
    } catch (err: unknown) {
      if (!host.signal.aborted) fault = describeFault(err);
    }
  };

  $effect(() => {
    void load();
    const timer = setInterval(() => {
      if (!document.hidden) void load();
    }, 4000);
    return () => clearInterval(timer);
  });

  const printer = $derived(status?.printers.find((p) => p.id === deviceId));
</script>

{#if fault}
  <FaultAlert {fault} />
{:else if !status}
  <Skeleton class="h-40 w-full" />
{:else if !printer}
  <p class="text-sm text-muted-foreground">
    {t('printerHawk.labels.noPrinters')}
  </p>
{:else}
  <PrinterView {printer} history={status.history} onChanged={load} />
{/if}
