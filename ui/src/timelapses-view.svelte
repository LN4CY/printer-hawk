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
    Label,
    Progress,
    Select,
    SelectContent,
    SelectItem,
    SelectTrigger,
    SelectValue,
    Skeleton,
    Slider,
  } from '@prusa/connect-ui/primitives';
  import { onDestroy } from 'svelte';

  import { hawkOn, type TimelapseSession } from './api';
  import { heldHost } from './host';

  const host = heldHost();
  const t = host.t;
  const service = hawkOn(host);

  let sessions = $state<TimelapseSession[] | undefined>();
  let selectedId = $state('');
  let currentFrame = $state(1);
  let playing = $state(false);
  let fpsStr = $state('24');
  let fault = $state<FaultText | undefined>();
  let notice = $state('');
  let exporting = $state(false);
  let exportPct = $state(0);
  let thumbs = $state<Record<string, string | undefined>>({});

  let canvasEl = $state<HTMLCanvasElement | undefined>();
  let playTimer: ReturnType<typeof setInterval> | undefined;
  let frameCache: Record<string, HTMLImageElement> = {};
  let cacheOrder: string[] = [];

  const formatFrameName = (idx: number): string =>
    `${String(idx).padStart(6, '0')}.jpg`;

  const sessionKey = (s: TimelapseSession): string => `${s.printer_id}:${s.id}`;

  const selectedSession = $derived(
    sessions?.find((s) => sessionKey(s) === selectedId) ?? sessions?.[0],
  );

  const maxFrames = $derived(Math.max(1, selectedSession?.frame_count ?? 1));

  const fpsItems = $derived([
    { value: '10', label: '10 fps' },
    { value: '15', label: '15 fps' },
    { value: '24', label: '24 fps' },
    { value: '30', label: '30 fps' },
    { value: '60', label: '60 fps' },
  ]);

  const stopPlayback = () => {
    playing = false;
    if (playTimer !== undefined) {
      clearInterval(playTimer);
      playTimer = undefined;
    }
  };

  const loadSessions = async () => {
    try {
      const payload = await service.listTimelapses(undefined, host.signal);
      sessions = payload.sessions;
      const first = payload.sessions[0];
      if (!selectedId && first) {
        selectedId = sessionKey(first);
        fpsStr = String(first.fps || 24);
        currentFrame = Math.max(1, first.frame_count || 1);
      }
      for (const s of payload.sessions) {
        const key = sessionKey(s);
        if (s.thumbnail_frame && !thumbs[key]) {
          const thumb = s.thumbnail_frame;
          void service
            .timelapseFrameUrl(s.printer_id, s.id, thumb, host.signal)
            .then((url) => {
              if (url && !host.signal.aborted) {
                thumbs = { ...thumbs, [key]: url };
              }
            })
            .catch(() => {
              // Ignore transient thumbnail errors.
            });
        }
      }
    } catch (err: unknown) {
      if (!host.signal.aborted) fault = describeFault(err);
    }
  };

  const loadBitmapImage = async (
    s: TimelapseSession,
    frameNum: number,
  ): Promise<HTMLImageElement | undefined> => {
    const cacheKey = `${s.printer_id}/${s.id}/${String(frameNum)}`;
    const existing = frameCache[cacheKey];
    if (existing) return existing;

    const blob = await service.timelapseFrameBlob(
      s.printer_id,
      s.id,
      formatFrameName(frameNum),
      host.signal,
    );
    if (!blob) return undefined;

    const objUrl = URL.createObjectURL(blob);
    try {
      const img = new Image();
      img.decoding = 'async';
      img.src = objUrl;
      await img.decode();
      frameCache[cacheKey] = img;
      cacheOrder.push(cacheKey);
      if (cacheOrder.length > 400) {
        const oldestKey = cacheOrder.shift();
        if (oldestKey) {
          frameCache = Object.fromEntries(
            Object.entries(frameCache).filter(([k]) => k !== oldestKey),
          );
        }
      }
      return img;
    } finally {
      URL.revokeObjectURL(objUrl);
    }
  };

  const drawFrameToCanvas = async (s: TimelapseSession, frameNum: number) => {
    const canvas = canvasEl;
    if (!canvas || s.frame_count < 1) return;
    const clamped = Math.max(1, Math.min(frameNum, s.frame_count));
    const img = await loadBitmapImage(s, clamped);
    if (!img || host.signal.aborted) return;
    if (
      canvas.width !== img.naturalWidth ||
      canvas.height !== img.naturalHeight
    ) {
      canvas.width = img.naturalWidth;
      canvas.height = img.naturalHeight;
    }
    const ctx = canvas.getContext('2d');
    if (ctx) {
      ctx.drawImage(img, 0, 0);
    }
  };

  const openSession = (s: TimelapseSession) => {
    stopPlayback();
    selectedId = sessionKey(s);
    fpsStr = String(s.fps || 24);
    currentFrame = 1;
    void drawFrameToCanvas(s, 1);
  };

  const advanceFrame = () => {
    const cur = selectedSession;
    if (!cur || cur.frame_count < 1) {
      stopPlayback();
      return;
    }
    const next = currentFrame >= cur.frame_count ? 1 : currentFrame + 1;
    currentFrame = next;
    void drawFrameToCanvas(cur, next);
  };

  const togglePlay = () => {
    const s = selectedSession;
    if (!s || s.frame_count < 1) return;
    if (playing) {
      stopPlayback();
      return;
    }
    if (currentFrame >= s.frame_count) {
      currentFrame = 1;
    }
    playing = true;
    const fps = Math.max(1, Number(fpsStr) || 24);
    playTimer = setInterval(advanceFrame, Math.round(1000 / fps));
  };

  const stepFrame = (delta: number) => {
    const s = selectedSession;
    if (!s || s.frame_count < 1) return;
    stopPlayback();
    const next = Math.max(1, Math.min(s.frame_count, currentFrame + delta));
    currentFrame = next;
    void drawFrameToCanvas(s, next);
  };

  const removeSession = async (s: TimelapseSession) => {
    stopPlayback();
    fault = undefined;
    notice = '';
    try {
      await service.deleteTimelapse(s.printer_id, s.id, host.signal);
      notice = t('printerHawk.timelapses.deleted');
      if (selectedId === sessionKey(s)) {
        selectedId = '';
      }
      await loadSessions();
    } catch (err: unknown) {
      if (!host.signal.aborted) fault = describeFault(err);
    }
  };

  const pickRecorderMime = (): { mime: string; ext: string } | undefined => {
    if (typeof MediaRecorder === 'undefined') return undefined;
    const candidates = [
      { mime: 'video/mp4;codecs=avc1.42E01E', ext: 'mp4' },
      { mime: 'video/mp4', ext: 'mp4' },
      { mime: 'video/webm;codecs=vp9', ext: 'webm' },
      { mime: 'video/webm;codecs=vp8', ext: 'webm' },
      { mime: 'video/webm', ext: 'webm' },
    ];
    for (const c of candidates) {
      if (MediaRecorder.isTypeSupported(c.mime)) return c;
    }
    return undefined;
  };

  const exportVideo = async () => {
    const s = selectedSession;
    if (!s || s.frame_count < 1 || exporting) return;
    const picked = pickRecorderMime();
    if (!picked) {
      notice = t('printerHawk.timelapses.exportUnsupported');
      return;
    }

    stopPlayback();
    exporting = true;
    exportPct = 0;
    fault = undefined;
    notice = '';

    try {
      const firstImg = await loadBitmapImage(s, 1);
      if (!firstImg) return;

      const off = document.createElement('canvas');
      off.width = firstImg.naturalWidth;
      off.height = firstImg.naturalHeight;
      const ctx = off.getContext('2d');
      if (!ctx) return;

      const fps = Math.max(1, Number(fpsStr) || 24);
      const stream = off.captureStream(fps);
      const chunks: BlobPart[] = [];
      const recorder = new MediaRecorder(stream, {
        mimeType: picked.mime,
        videoBitsPerSecond: 6_000_000,
      });
      recorder.ondataavailable = (ev: BlobEvent) => {
        if (ev.data.size > 0) chunks.push(ev.data);
      };

      const donePromise = new Promise<void>((resolve) => {
        recorder.onstop = () => resolve();
      });

      recorder.start();
      const frameDelay = Math.max(16, Math.round(1000 / fps));
      for (let i = 1; i <= s.frame_count; i++) {
        if (host.signal.aborted) break;
        const img = await loadBitmapImage(s, i);
        if (img) {
          ctx.drawImage(img, 0, 0, off.width, off.height);
        }
        exportPct = Math.round((i / s.frame_count) * 100);
        await new Promise((r) => setTimeout(r, frameDelay));
      }
      recorder.stop();
      await donePromise;

      const outBlob = new Blob(chunks, { type: picked.mime });
      const dlUrl = URL.createObjectURL(outBlob);
      const a = document.createElement('a');
      a.href = dlUrl;
      a.download = `${s.id}.${picked.ext}`;
      document.body.appendChild(a);
      a.click();
      a.remove();
      setTimeout(() => URL.revokeObjectURL(dlUrl), 1000);
    } catch (err: unknown) {
      if (!host.signal.aborted) fault = describeFault(err);
    } finally {
      exporting = false;
    }
  };

  $effect(() => {
    void loadSessions();
    const timer = setInterval(() => {
      if (!document.hidden) void loadSessions();
    }, 8000);
    return () => clearInterval(timer);
  });

  $effect(() => {
    const s = selectedSession;
    if (s && canvasEl && s.frame_count > 0 && !playing) {
      void drawFrameToCanvas(s, currentFrame);
    }
  });

  onDestroy(() => {
    stopPlayback();
    frameCache = {};
    cacheOrder = [];
    for (const u of Object.values(thumbs)) {
      if (u) URL.revokeObjectURL(u);
    }
  });

  const statusBadgeVariant = (status: string) => {
    if (status === 'recording') return 'warning' as const;
    if (status === 'completed') return 'success' as const;
    return 'secondary' as const;
  };

  const statusText = (status: string) => {
    if (status === 'recording') {
      return t('printerHawk.timelapses.statusRecording');
    }
    if (status === 'completed') {
      return t('printerHawk.timelapses.statusCompleted');
    }
    return t('printerHawk.timelapses.statusStopped');
  };

  const modeText = (mode: string) =>
    mode === 'interval'
      ? t('printerHawk.timelapses.modeInterval')
      : t('printerHawk.timelapses.modeLayer');

  const formatMB = (bytes: number): string =>
    `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
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

  {#if !sessions}
    <Skeleton class="h-48 w-full" />
  {:else if sessions.length === 0}
    <Card>
      <CardHeader class="flex flex-row items-center justify-between pb-2">
        <CardTitle class="text-sm"
          >{t('printerHawk.timelapses.title')}</CardTitle
        >
        <Button size="compact" variant="secondary" onclick={loadSessions}>
          {t('printerHawk.timelapses.refresh')}
        </Button>
      </CardHeader>
      <CardContent class="text-sm text-muted-foreground">
        {t('printerHawk.timelapses.empty')}
      </CardContent>
    </Card>
  {:else}
    <div class="grid grid-cols-1 gap-4 lg:grid-cols-12">
      <Card class="lg:col-span-7">
        <CardHeader
          class="flex flex-row flex-wrap items-center justify-between gap-2 pb-2"
        >
          <CardTitle class="text-sm">
            {selectedSession?.job_name ?? t('printerHawk.timelapses.title')}
          </CardTitle>
          {#if selectedSession}
            <div class="flex items-center gap-2 text-xs text-muted-foreground">
              <Badge variant={statusBadgeVariant(selectedSession.status)}>
                {statusText(selectedSession.status)}
              </Badge>
              <span class="font-mono">
                {`${String(currentFrame)} / ${String(selectedSession.frame_count)}`}
              </span>
            </div>
          {/if}
        </CardHeader>
        <CardContent class="flex flex-col gap-3">
          <canvas
            bind:this={canvasEl}
            class="aspect-video w-full rounded-md bg-muted object-contain"
          ></canvas>

          <div class="px-1">
            <Slider
              type="single"
              min={1}
              max={maxFrames}
              step={1}
              bind:value={currentFrame}
              onValueChange={(next: number) => {
                stopPlayback();
                if (selectedSession)
                  void drawFrameToCanvas(selectedSession, next);
              }}
            />
          </div>

          <div class="flex flex-wrap items-center justify-between gap-2">
            <div class="flex items-center gap-2">
              <Button
                size="compact"
                disabled={!selectedSession || selectedSession.frame_count < 1}
                onclick={togglePlay}
              >
                {playing
                  ? t('printerHawk.timelapses.pause')
                  : t('printerHawk.timelapses.play')}
              </Button>
              <Button
                size="compact"
                variant="secondary"
                disabled={!selectedSession || currentFrame <= 1}
                onclick={() => stepFrame(-1)}
              >
                {t('printerHawk.timelapses.prev')}
              </Button>
              <Button
                size="compact"
                variant="secondary"
                disabled={!selectedSession ||
                  currentFrame >= selectedSession.frame_count}
                onclick={() => stepFrame(1)}
              >
                {t('printerHawk.timelapses.next')}
              </Button>
            </div>

            <div class="flex items-center gap-2">
              <Label variant="hint" class="text-xs">
                {t('printerHawk.timelapses.fps')}
              </Label>
              <Select
                items={fpsItems}
                bind:value={fpsStr}
                onValueChange={() => {
                  if (playing) {
                    stopPlayback();
                    togglePlay();
                  }
                }}
              >
                <SelectTrigger size="sm" class="min-w-28 shrink-0">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {#each fpsItems as opt (opt.value)}
                    <SelectItem value={opt.value}>{opt.label}</SelectItem>
                  {/each}
                </SelectContent>
              </Select>
              <Button
                size="compact"
                variant="secondary"
                disabled={exporting ||
                  !selectedSession ||
                  selectedSession.frame_count < 1}
                onclick={exportVideo}
              >
                {exporting
                  ? t('printerHawk.timelapses.exporting')
                  : t('printerHawk.timelapses.export')}
              </Button>
            </div>
          </div>

          {#if exporting}
            <Progress value={exportPct} max={100} />
          {/if}
        </CardContent>
      </Card>

      <Card class="lg:col-span-5">
        <CardHeader class="flex flex-row items-center justify-between pb-2">
          <CardTitle class="text-sm">
            {t('printerHawk.timelapses.title')}
          </CardTitle>
          <Button size="compact" variant="secondary" onclick={loadSessions}>
            {t('printerHawk.timelapses.refresh')}
          </Button>
        </CardHeader>
        <CardContent class="flex max-h-96 flex-col gap-2 overflow-y-auto">
          {#each sessions as s (sessionKey(s))}
            {@const key = sessionKey(s)}
            <div
              class={`flex items-center gap-3 rounded-md border p-2 ${
                selectedSession && sessionKey(selectedSession) === key
                  ? 'border-primary bg-muted/40'
                  : 'border-border'
              }`}
            >
              {#if thumbs[key]}
                <img
                  src={thumbs[key]}
                  alt={s.job_name}
                  class="rounded h-14 w-24 shrink-0 bg-muted object-cover"
                />
              {:else}
                <div class="rounded h-14 w-24 shrink-0 bg-muted"></div>
              {/if}
              <div class="min-w-0 flex-1 text-xs">
                <div class="truncate font-medium text-foreground">
                  {s.job_name}
                </div>
                <div class="truncate text-muted-foreground">
                  {`${s.printer_name} · ${modeText(s.mode)}`}
                </div>
                <div class="mt-1 flex flex-wrap items-center gap-2">
                  <Badge variant={statusBadgeVariant(s.status)}>
                    {statusText(s.status)}
                  </Badge>
                  <span class="font-mono text-muted-foreground">
                    {`${String(s.frame_count)} ${t('printerHawk.timelapses.framesLabel')} · ${formatMB(s.bytes_total)}`}
                  </span>
                </div>
              </div>
              <div class="flex shrink-0 flex-col gap-1">
                <Button
                  size="compact"
                  variant="secondary"
                  onclick={() => openSession(s)}
                >
                  {t('printerHawk.timelapses.open')}
                </Button>
                <Button
                  size="compact"
                  variant="destructive"
                  onclick={() => removeSession(s)}
                >
                  {t('printerHawk.timelapses.delete')}
                </Button>
              </div>
            </div>
          {/each}
        </CardContent>
      </Card>
    </div>
  {/if}
</div>
