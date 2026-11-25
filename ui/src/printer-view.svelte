<script lang="ts">
  import { describeFault, type FaultText } from '@prusa/connect-ui/i18n';
  import {
    Badge,
    Button,
    Card,
    CardContent,
    CardHeader,
    CardTitle,
    FaultAlert,
    Input,
    Select,
    SelectContent,
    SelectItem,
    SelectTrigger,
    SelectValue,
    Table,
    TableBody,
    TableCell,
    TableHead,
    TableHeader,
    TableRow,
  } from '@prusa/connect-ui/primitives';
  import { onDestroy } from 'svelte';

  import {
    type CheckRecord,
    hawkOn,
    type PrinterSummary,
    type StageResult,
  } from './api';
  import { heldHost } from './host';

  let {
    printer,
    history,
    onChanged,
  }: {
    printer: PrinterSummary;
    history: CheckRecord[];
    onChanged: () => void | Promise<void>;
  } = $props();

  const host = heldHost();
  const t = host.t;
  const service = hawkOn(host);

  let camMode = $state<'live' | 'ai'>('live');
  let refreshRate = $state('2000');
  let customRateSec = $state('10');
  let frameSrc = $state<string | undefined>();
  let frameTimestamp = $state('');
  let busy = $state(false);
  let notice = $state('');
  let fault = $state<FaultText | undefined>();
  let inspectedRecord = $state<CheckRecord | undefined>();
  let inspectedSrc = $state<string | undefined>();

  let timer: ReturnType<typeof setTimeout> | undefined;

  const replaceFrameSrc = (nextUrl: string | undefined) => {
    if (frameSrc) URL.revokeObjectURL(frameSrc);
    frameSrc = nextUrl;
  };

  const replaceInspectedSrc = (nextUrl: string | undefined) => {
    if (inspectedSrc) URL.revokeObjectURL(inspectedSrc);
    inspectedSrc = nextUrl;
  };

  const clearTimer = () => {
    if (timer !== undefined) {
      clearTimeout(timer);
      timer = undefined;
    }
  };

  const resolveFrameUrl = async (
    mode: 'live' | 'ai',
    targetId: string,
    frameId?: string,
  ): Promise<string | undefined> => {
    if (mode === 'live') {
      return service.liveFrameUrl(targetId, host.signal);
    }
    if (frameId) {
      return service.storedFrameUrl(targetId, frameId, host.signal);
    }
    return undefined;
  };

  const inputValue = (e: Event): string =>
    (e.currentTarget as HTMLInputElement).value;

  const effectiveRefreshMs = $derived.by(() => {
    if (refreshRate === 'custom') {
      const secs = Number(customRateSec);
      if (!Number.isFinite(secs) || secs <= 0) return 0;
      return Math.max(500, Math.round(secs * 1000));
    }
    return Number(refreshRate);
  });

  const loadCameraFrame = async (
    mode: 'live' | 'ai',
    intervalMs: number,
    targetId: string,
    lastFrameId?: string,
  ) => {
    clearTimer();
    if (host.signal.aborted || document.hidden) return;
    try {
      const url = await resolveFrameUrl(mode, targetId, lastFrameId);
      if (url) {
        replaceFrameSrc(url);
        frameTimestamp = new Date().toLocaleTimeString();
      }
    } catch {
      // Keep last visible frame on transient poll failure.
    }
    if (mode === 'live' && intervalMs > 0) {
      timer = setTimeout(() => {
        void loadCameraFrame(mode, intervalMs, targetId, lastFrameId);
      }, intervalMs);
    }
  };

  $effect(() => {
    void loadCameraFrame(
      camMode,
      effectiveRefreshMs,
      printer.id,
      printer.last_check?.frame_id,
    );
    return () => clearTimer();
  });

  onDestroy(() => {
    clearTimer();
    replaceFrameSrc(undefined);
    replaceInspectedSrc(undefined);
  });

  const runAction = async (fn: () => Promise<unknown>) => {
    busy = true;
    fault = undefined;
    notice = '';
    try {
      await fn();
      notice = t('printerHawk.labels.actionDone');
      await onChanged();
      await loadCameraFrame(
        camMode,
        effectiveRefreshMs,
        printer.id,
        printer.last_check?.frame_id,
      );
    } catch (err: unknown) {
      if (!host.signal.aborted) fault = describeFault(err);
    } finally {
      busy = false;
    }
  };

  const openStoredFrame = async (rec: CheckRecord) => {
    if (!rec.frame_id) return;
    inspectedRecord = rec;
    try {
      const url = await service.storedFrameUrl(
        rec.printer_id,
        rec.frame_id,
        host.signal,
      );
      replaceInspectedSrc(url);
    } catch (err: unknown) {
      if (!host.signal.aborted) fault = describeFault(err);
    }
  };

  const stageBadgeVariant = (st?: StageResult) => {
    if (!st) return 'secondary' as const;
    if (st.status === 'failure') return 'destructive' as const;
    if (st.status === 'warning') return 'warning' as const;
    return 'success' as const;
  };

  const rateItems = $derived([
    { value: '1000', label: t('printerHawk.rates.r1s') },
    { value: '2000', label: t('printerHawk.rates.r2s') },
    { value: '5000', label: t('printerHawk.rates.r5s') },
    { value: 'custom', label: t('printerHawk.rates.custom') },
    { value: '0', label: t('printerHawk.rates.paused') },
  ]);

  const printerChecks = $derived(
    history.filter((h) => h.printer_id === printer.id),
  );
  const emptyDash = $derived(t('printerHawk.labels.emptyDash'));
  const zText = $derived(
    typeof printer.z === 'number'
      ? `${printer.z.toFixed(2)} ${t('printerHawk.labels.mmUnit')}`
      : emptyDash,
  );
  const layerStr = $derived(String(printer.tracker.layer_count));
  const baseText = $derived(
    typeof printer.tracker.confirmed_z === 'number'
      ? ` (${t('printerHawk.labels.layerPrefix')} ${layerStr}, ${t('printerHawk.labels.basePrefix')} ${printer.tracker.confirmed_z.toFixed(2)} ${t('printerHawk.labels.mmUnit')})`
      : ` (${t('printerHawk.labels.layerPrefix')} ${layerStr})`,
  );
</script>

<div class="flex flex-col gap-4">
  {#if fault}
    <FaultAlert {fault} />
  {/if}
  {#if notice}
    <div
      class="rounded-md border border-border bg-card px-3 py-2 text-sm text-foreground"
    >
      {notice}
    </div>
  {/if}

  <div class="flex flex-wrap items-center gap-2">
    <Button
      disabled={busy}
      onclick={() =>
        runAction(() => service.inspect(printer.id, false, host.signal))}
    >
      {t('printerHawk.actions.inspect')}
    </Button>
    <Button
      variant="secondary"
      disabled={busy}
      onclick={() =>
        runAction(() => service.inspect(printer.id, true, host.signal))}
    >
      {t('printerHawk.actions.verify')}
    </Button>
    <Button
      variant="secondary"
      disabled={busy}
      onclick={() =>
        runAction(() => service.falseAlarm(printer.id, host.signal))}
    >
      {t('printerHawk.actions.falseAlarm')}
    </Button>
    <Button
      variant="secondary"
      disabled={busy}
      onclick={() =>
        runAction(() => service.clearAlert(printer.id, host.signal))}
    >
      {t('printerHawk.actions.clearAlert')}
    </Button>
    <Button
      variant="destructive"
      disabled={busy || !printer.connected}
      onclick={() =>
        runAction(() => service.pausePrinter(printer.id, host.signal))}
    >
      {t('printerHawk.actions.pause')}
    </Button>
  </div>

  <div class="grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-4">
    <Card>
      <CardContent class="pt-4">
        <div class="text-xs text-muted-foreground uppercase">
          {t('printerHawk.labels.state')}
        </div>
        <div class="mt-1 flex items-center gap-2">
          <Badge variant={printer.connected ? 'stateActive' : 'stateOffline'}>
            {printer.state}
          </Badge>
          {#if printer.alert_active}
            <Badge variant="destructive">
              {t('printerHawk.labels.alertActive')}
            </Badge>
          {/if}
        </div>
      </CardContent>
    </Card>

    <Card>
      <CardContent class="pt-4">
        <div class="text-xs text-muted-foreground uppercase">
          {t('printerHawk.labels.zLayer')}
        </div>
        <div class="mt-1 font-mono text-sm font-medium">{zText}{baseText}</div>
      </CardContent>
    </Card>

    <Card>
      <CardContent class="pt-4">
        <div class="text-xs text-muted-foreground uppercase">
          {t('printerHawk.labels.monitoring')}
        </div>
        <div class="mt-1 flex items-center gap-2">
          <Badge variant={printer.config.enabled ? 'success' : 'secondary'}>
            {printer.config.enabled
              ? t('printerHawk.labels.enabled')
              : t('printerHawk.labels.disabled')}
          </Badge>
          <span class="text-xs text-muted-foreground uppercase">
            {printer.config.action}
          </span>
        </div>
      </CardContent>
    </Card>

    <Card>
      <CardContent class="pt-4">
        <div class="text-xs text-muted-foreground uppercase">
          {t('printerHawk.labels.cameraSource')}
        </div>
        <div class="mt-1 text-sm font-medium">
          {printer.config.camera_mode === 'url'
            ? t('printerHawk.labels.liveSourceUrl')
            : t('printerHawk.labels.liveSourceConnect')}
        </div>
      </CardContent>
    </Card>
  </div>

  {#if printer.last_error}
    <div
      class="rounded-md border border-destructive/50 bg-destructive/10 px-3 py-2 text-xs text-destructive"
    >
      {printer.last_error}
    </div>
  {/if}

  <div class="grid grid-cols-1 gap-4 lg:grid-cols-12">
    <Card class="lg:col-span-5">
      <CardHeader
        class="flex flex-row flex-wrap items-center justify-between gap-2 pb-2"
      >
        <div class="flex items-center gap-2">
          <Button
            size="compact"
            variant={camMode === 'live' ? 'default' : 'secondary'}
            onclick={() => {
              camMode = 'live';
            }}
          >
            {t('printerHawk.actions.liveCamera')}
          </Button>
          <Button
            size="compact"
            variant={camMode === 'ai' ? 'default' : 'secondary'}
            onclick={() => {
              camMode = 'ai';
            }}
          >
            {t('printerHawk.actions.lastAiFrame')}
          </Button>
        </div>
        {#if camMode === 'live'}
          <div class="flex items-center gap-2">
            <Select items={rateItems} bind:value={refreshRate}>
              <SelectTrigger size="sm" class="w-28">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {#each rateItems as rateOpt (rateOpt.value)}
                  <SelectItem value={rateOpt.value}>{rateOpt.label}</SelectItem>
                {/each}
              </SelectContent>
            </Select>
            {#if refreshRate === 'custom'}
              <div class="flex items-center gap-1">
                <Input
                  type="number"
                  min="0.5"
                  step="0.5"
                  size="sm"
                  class="w-20"
                  value={customRateSec}
                  oninput={(e: Event) => {
                    customRateSec = inputValue(e);
                  }}
                />
                <span class="text-xs text-muted-foreground">
                  {t('printerHawk.rates.secUnit')}
                </span>
              </div>
            {/if}
          </div>
        {/if}
      </CardHeader>
      <CardContent>
        {#if frameSrc}
          <img
            src={frameSrc}
            alt={t('printerHawk.actions.liveCamera')}
            class="aspect-video w-full rounded-md bg-muted object-cover"
          />
        {:else}
          <div
            class="flex aspect-video w-full items-center justify-center rounded-md bg-muted text-xs text-muted-foreground"
          >
            {t('printerHawk.labels.frameUnavailable')}
          </div>
        {/if}
        {#if frameTimestamp}
          <div class="mt-2 text-right font-mono text-xs text-muted-foreground">
            {frameTimestamp}
          </div>
        {/if}
      </CardContent>
    </Card>

    <Card class="lg:col-span-7">
      <CardHeader class="pb-2">
        <CardTitle class="text-sm">
          {t('printerHawk.labels.recentChecks')}
        </CardTitle>
      </CardHeader>
      <CardContent>
        {#if printerChecks.length === 0}
          <p class="py-4 text-sm text-muted-foreground">
            {t('printerHawk.labels.noChecks')}
          </p>
        {:else}
          <Table class="text-xs">
            <TableHeader>
              <TableRow>
                <TableHead>{t('printerHawk.table.time')}</TableHead>
                <TableHead>{t('printerHawk.table.trigger')}</TableHead>
                <TableHead>{t('printerHawk.table.stage1')}</TableHead>
                <TableHead>{t('printerHawk.table.stage2')}</TableHead>
                <TableHead>{t('printerHawk.table.action')}</TableHead>
                <TableHead>{t('printerHawk.table.frame')}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {#each printerChecks as rec (rec.id)}
                <TableRow>
                  <TableCell class="font-mono">
                    {rec.ts.slice(11, 19)}
                  </TableCell>
                  <TableCell>
                    <div>{rec.trigger}</div>
                    <div class="text-muted-foreground">{rec.image_source}</div>
                  </TableCell>
                  <TableCell>
                    <Badge variant={stageBadgeVariant(rec.stage1)}>
                      {rec.stage1.status}
                    </Badge>
                    <div class="mt-1 font-mono text-muted-foreground">
                      {rec.stage1.provider} · {rec.stage1.model}
                      {#if rec.stage1.fallback_from}
                        ({t('printerHawk.labels.fallbackTag')}
                        {rec.stage1.fallback_from})
                      {/if}
                    </div>
                    <div class="mt-1 text-foreground">{rec.stage1.reason}</div>
                  </TableCell>
                  <TableCell>
                    {#if rec.stage2}
                      <Badge variant={stageBadgeVariant(rec.stage2)}>
                        {rec.stage2.status}
                      </Badge>
                      <div class="mt-1 font-mono text-muted-foreground">
                        {rec.stage2.provider} · {rec.stage2.model}
                      </div>
                    {:else}
                      {emptyDash}
                    {/if}
                  </TableCell>
                  <TableCell class="font-mono">{rec.action}</TableCell>
                  <TableCell>
                    {#if rec.frame_id}
                      <Button
                        size="compact"
                        variant="secondary"
                        onclick={() => openStoredFrame(rec)}
                      >
                        {t('printerHawk.actions.viewFrame')}
                      </Button>
                    {:else}
                      {emptyDash}
                    {/if}
                  </TableCell>
                </TableRow>
              {/each}
            </TableBody>
          </Table>
        {/if}
      </CardContent>
    </Card>
  </div>

  {#if inspectedRecord}
    <Card>
      <CardHeader class="flex flex-row items-center justify-between pb-2">
        <CardTitle class="text-sm">
          {t('printerHawk.labels.selectFrameHint')} ({inspectedRecord.ts})
        </CardTitle>
        <Button
          size="compact"
          variant="secondary"
          onclick={() => {
            inspectedRecord = undefined;
            replaceInspectedSrc(undefined);
          }}
        >
          {t('printerHawk.actions.closeFrame')}
        </Button>
      </CardHeader>
      <CardContent>
        {#if inspectedSrc}
          <img
            src={inspectedSrc}
            alt={t('printerHawk.labels.selectFrameHint')}
            class="max-h-96 rounded-md object-contain"
          />
        {/if}
        <p class="mt-2 text-xs text-muted-foreground">
          {inspectedRecord.stage2?.reason ?? inspectedRecord.stage1.reason}
        </p>
      </CardContent>
    </Card>
  {/if}
</div>
