<script lang="ts">
  import { describeFault, type FaultText } from '@prusa/connect-ui/i18n';
  import {
    Badge,
    Button,
    Card,
    CardContent,
    CardHeader,
    CardTitle,
    Checkbox,
    FaultAlert,
    Input,
    Label,
    PageHeader,
    PasswordInput,
    Select,
    SelectContent,
    SelectItem,
    SelectTrigger,
    SelectValue,
    Skeleton,
    Tabs,
    TabsContent,
    TabsList,
    TabsTrigger,
    Textarea,
  } from '@prusa/connect-ui/primitives';

  import {
    hawkOn,
    type ProviderSlot,
    type ProviderTestResult,
    type RuntimeConfig,
    type StatusPayload,
  } from './api';
  import { heldHost } from './host';
  import PrinterView from './printer-view.svelte';

  const host = heldHost();
  const t = host.t;
  const service = hawkOn(host);

  let activeTab = $state('monitor');
  let status = $state<StatusPayload | undefined>();
  let settings = $state<RuntimeConfig | undefined>();
  let selectedPrinterId = $state('');
  let targetPrinterKey = $state('__default__');
  let fault = $state<FaultText | undefined>();
  let saveMsg = $state('');
  let saving = $state(false);
  let testResults = $state<Record<string, ProviderTestResult | undefined>>({});
  let testingSlot = $state('');

  const loadStatus = async () => {
    try {
      const s = await service.status(undefined, host.signal);
      status = s;
      const first = s.printers[0];
      if (!selectedPrinterId && first) {
        selectedPrinterId = first.id;
      }
    } catch (err: unknown) {
      if (!host.signal.aborted) fault = describeFault(err);
    }
  };

  const loadSettings = async () => {
    try {
      settings = await service.getSettings(host.signal);
    } catch (err: unknown) {
      if (!host.signal.aborted) fault = describeFault(err);
    }
  };

  $effect(() => {
    void loadStatus();
    void loadSettings();
    const interval = setInterval(() => {
      if (!document.hidden) void loadStatus();
    }, 4000);
    return () => clearInterval(interval);
  });

  const saveAllSettings = async () => {
    if (!settings) return;
    saving = true;
    saveMsg = '';
    fault = undefined;
    try {
      settings = await service.updateSettings(settings, host.signal);
      saveMsg = t('printerHawk.labels.saved');
      await loadStatus();
    } catch (err: unknown) {
      if (!host.signal.aborted) fault = describeFault(err);
    } finally {
      saving = false;
    }
  };

  const testSlot = async (slotKey: string, slot: ProviderSlot) => {
    testingSlot = slotKey;
    fault = undefined;
    try {
      const res = await service.testProvider(slotKey, slot, host.signal);
      testResults = { ...testResults, [slotKey]: res };
    } catch (err: unknown) {
      if (!host.signal.aborted) fault = describeFault(err);
    } finally {
      testingSlot = '';
    }
  };

  const ensurePrinterEntry = (key: string) => {
    if (!settings || key === '__default__') return;
    if (!(key in settings.printers)) {
      settings.printers = {
        ...settings.printers,
        [key]: { ...settings.default_printer },
      };
    }
  };

  const inputValue = (e: Event): string =>
    (e.currentTarget as HTMLInputElement).value;

  const providerItems = $derived([
    { value: 'none', label: t('printerHawk.providers.optNone') },
    { value: 'ollama', label: t('printerHawk.providers.optOllama') },
    { value: 'gemini', label: t('printerHawk.providers.optGemini') },
    { value: 'anthropic', label: t('printerHawk.providers.optAnthropic') },
    { value: 'openai_compat', label: t('printerHawk.providers.optOpenAI') },
  ]);

  const actionItems = $derived([
    { value: 'notify', label: t('printerHawk.settings.actNotify') },
    { value: 'pause', label: t('printerHawk.settings.actPause') },
  ]);

  const cameraModeItems = $derived([
    { value: 'connect', label: t('printerHawk.settings.camConnect') },
    { value: 'url', label: t('printerHawk.settings.camUrl') },
  ]);

  const targetPrinterItems = $derived([
    { value: '__default__', label: t('printerHawk.settings.useDefault') },
    ...(status?.printers ?? []).map((p) => ({
      value: p.id,
      label: `${p.name} (${p.id})`,
    })),
  ]);

  const selectedPrinter = $derived(
    status?.printers.find((p) => p.id === selectedPrinterId) ??
      status?.printers[0],
  );

  const editedPrinterConfig = $derived.by(() => {
    if (!settings) return undefined;
    if (targetPrinterKey === '__default__') return settings.default_printer;
    return settings.printers[targetPrinterKey] ?? settings.default_printer;
  });
</script>

<div class="flex flex-col gap-4">
  <PageHeader title={t('printerHawk.page.title')} />
  <p class="text-sm text-muted-foreground">{t('printerHawk.page.subtitle')}</p>

  {#if fault}
    <FaultAlert {fault} />
  {/if}

  <Tabs bind:value={activeTab}>
    <TabsList>
      <TabsTrigger value="monitor">{t('printerHawk.tabs.monitor')}</TabsTrigger>
      <TabsTrigger value="providers">
        {t('printerHawk.tabs.providers')}
      </TabsTrigger>
      <TabsTrigger value="settings">
        {t('printerHawk.tabs.settings')}
      </TabsTrigger>
    </TabsList>

    <TabsContent value="monitor" class="mt-4 flex flex-col gap-4">
      {#if !status}
        <Skeleton class="h-48 w-full" />
      {:else if status.printers.length === 0}
        <Card>
          <CardContent class="py-6 text-sm text-muted-foreground">
            {t('printerHawk.labels.noPrinters')}
          </CardContent>
        </Card>
      {:else}
        {#if status.printers.length > 1}
          <div class="flex flex-wrap gap-2">
            {#each status.printers as p (p.id)}
              <Button
                size="compact"
                variant={selectedPrinter?.id === p.id ? 'default' : 'secondary'}
                onclick={() => {
                  selectedPrinterId = p.id;
                }}
              >
                {p.name}
              </Button>
            {/each}
          </div>
        {/if}

        {#if selectedPrinter}
          <PrinterView
            printer={selectedPrinter}
            history={status.history}
            onChanged={loadStatus}
          />
        {/if}
      {/if}
    </TabsContent>

    <TabsContent value="providers" class="mt-4 flex flex-col gap-4">
      {#if !settings}
        <Skeleton class="h-48 w-full" />
      {:else}
        {#if !settings.page_setup}
          <div
            class="rounded-md border border-border bg-card p-3 text-sm text-muted-foreground"
          >
            {t('printerHawk.labels.readOnlyBanner')}
          </div>
        {/if}

        {#if saveMsg}
          <div
            class="rounded-md border border-border bg-card p-3 text-sm text-foreground"
          >
            {saveMsg}
          </div>
        {/if}

        <div class="grid grid-cols-1 gap-4 lg:grid-cols-2">
          {#each [{ key: 'stage1_primary', title: t('printerHawk.providers.stage1Primary'), slot: settings.stage1_primary }, { key: 'stage1_fallback', title: t('printerHawk.providers.stage1Fallback'), slot: settings.stage1_fallback }, { key: 'stage2_primary', title: t('printerHawk.providers.stage2Primary'), slot: settings.stage2_primary }, { key: 'stage2_fallback', title: t('printerHawk.providers.stage2Fallback'), slot: settings.stage2_fallback }] as item (item.key)}
            <Card>
              <CardHeader
                class="flex flex-row items-center justify-between pb-2"
              >
                <CardTitle class="text-sm">{item.title}</CardTitle>
                <Button
                  size="compact"
                  variant="secondary"
                  disabled={testingSlot === item.key}
                  onclick={() => testSlot(item.key, item.slot)}
                >
                  {testingSlot === item.key
                    ? t('printerHawk.actions.testing')
                    : t('printerHawk.actions.testProvider')}
                </Button>
              </CardHeader>
              <CardContent class="flex flex-col gap-3 text-xs">
                {#if testResults[item.key]}
                  {@const res = testResults[item.key]}
                  {#if res}
                    <div class="flex items-center gap-2">
                      <Badge variant={res.ok ? 'success' : 'destructive'}>
                        {res.ok
                          ? t('printerHawk.labels.testOk')
                          : t('printerHawk.labels.testFail')}
                      </Badge>
                      <span class="font-mono">
                        {String(res.latency_ms)}
                        {t('printerHawk.labels.msUnit')}
                      </span>
                      <span class="text-muted-foreground">{res.detail}</span>
                    </div>
                  {/if}
                {/if}

                <div class="flex flex-col gap-1">
                  <Label variant="hint">
                    {t('printerHawk.providers.provider')}
                  </Label>
                  <Select
                    items={providerItems}
                    disabled={!settings.page_setup}
                    bind:value={item.slot.provider}
                  >
                    <SelectTrigger size="sm" class="w-full">
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      {#each providerItems as opt (opt.value)}
                        <SelectItem value={opt.value}>{opt.label}</SelectItem>
                      {/each}
                    </SelectContent>
                  </Select>
                </div>

                {#if item.slot.provider !== 'none'}
                  <div class="flex flex-col gap-1">
                    <Label variant="hint">
                      {t('printerHawk.providers.baseUrl')}
                    </Label>
                    <Input
                      type="text"
                      size="sm"
                      disabled={!settings.page_setup}
                      placeholder={t('printerHawk.providers.hintBaseUrl')}
                      value={item.slot.base_url}
                      oninput={(e: Event) => {
                        item.slot.base_url = inputValue(e);
                      }}
                    />
                  </div>

                  <div class="flex flex-col gap-1">
                    <Label variant="hint">
                      {t('printerHawk.providers.model')}
                    </Label>
                    <Input
                      type="text"
                      size="sm"
                      disabled={!settings.page_setup}
                      value={item.slot.model}
                      oninput={(e: Event) => {
                        item.slot.model = inputValue(e);
                      }}
                    />
                  </div>

                  <div class="flex flex-col gap-1">
                    <Label variant="hint">
                      {t('printerHawk.providers.apiKey')}
                    </Label>
                    <PasswordInput
                      size="sm"
                      disabled={!settings.page_setup}
                      value={item.slot.api_key}
                      oninput={(e: Event) => {
                        item.slot.api_key = inputValue(e);
                      }}
                    />
                  </div>

                  <div class="grid grid-cols-2 gap-2">
                    <div class="flex flex-col gap-1">
                      <Label variant="hint">
                        {t('printerHawk.providers.connectTimeout')}
                      </Label>
                      <Input
                        type="number"
                        step="0.5"
                        size="sm"
                        disabled={!settings.page_setup}
                        value={String(item.slot.connect_timeout_sec)}
                        oninput={(e: Event) => {
                          item.slot.connect_timeout_sec = Number(inputValue(e));
                        }}
                      />
                    </div>
                    <div class="flex flex-col gap-1">
                      <Label variant="hint">
                        {t('printerHawk.providers.readTimeout')}
                      </Label>
                      <Input
                        type="number"
                        step="1"
                        size="sm"
                        disabled={!settings.page_setup}
                        value={String(item.slot.read_timeout_sec)}
                        oninput={(e: Event) => {
                          item.slot.read_timeout_sec = Number(inputValue(e));
                        }}
                      />
                    </div>
                  </div>

                  {#if item.slot.provider === 'ollama'}
                    <div class="flex items-center gap-2 pt-1">
                      <Checkbox
                        id={`think-${item.key}`}
                        disabled={!settings.page_setup}
                        bind:checked={item.slot.think}
                      />
                      <Label for={`think-${item.key}`}>
                        {t('printerHawk.providers.think')}
                      </Label>
                    </div>
                  {/if}
                {/if}
              </CardContent>
            </Card>
          {/each}
        </div>

        {#if settings.page_setup}
          <div>
            <Button disabled={saving} onclick={saveAllSettings}>
              {saving
                ? t('printerHawk.actions.saving')
                : t('printerHawk.actions.save')}
            </Button>
          </div>
        {/if}
      {/if}
    </TabsContent>

    <TabsContent value="settings" class="mt-4 flex flex-col gap-4">
      {#if !settings}
        <Skeleton class="h-48 w-full" />
      {:else if editedPrinterConfig}
        <div
          class="rounded-md border border-border bg-card p-3 text-xs text-muted-foreground"
        >
          {t('printerHawk.labels.permHint')}
        </div>

        {#if saveMsg}
          <div
            class="rounded-md border border-border bg-card p-3 text-sm text-foreground"
          >
            {saveMsg}
          </div>
        {/if}

        <Card>
          <CardContent class="flex flex-col gap-3 pt-4 text-xs">
            <div class="flex flex-col gap-1">
              <Label variant="hint">
                {t('printerHawk.settings.printerSelect')}
              </Label>
              <Select
                items={targetPrinterItems}
                value={targetPrinterKey}
                onValueChange={(next: string) => {
                  ensurePrinterEntry(next);
                  targetPrinterKey = next;
                }}
              >
                <SelectTrigger size="sm" class="w-full">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {#each targetPrinterItems as opt (opt.value)}
                    <SelectItem value={opt.value}>{opt.label}</SelectItem>
                  {/each}
                </SelectContent>
              </Select>
            </div>

            <div class="flex items-center gap-2">
              <Checkbox
                id="printer-enable"
                disabled={!settings.page_setup}
                bind:checked={editedPrinterConfig.enabled}
              />
              <Label for="printer-enable">
                {t('printerHawk.settings.enablePrinter')}
              </Label>
            </div>

            <div class="grid grid-cols-1 gap-3 md:grid-cols-2">
              <div class="flex flex-col gap-1">
                <Label variant="hint">
                  {t('printerHawk.settings.action')}
                </Label>
                <Select
                  items={actionItems}
                  disabled={!settings.page_setup}
                  bind:value={editedPrinterConfig.action}
                >
                  <SelectTrigger size="sm" class="w-full">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    {#each actionItems as opt (opt.value)}
                      <SelectItem value={opt.value}>{opt.label}</SelectItem>
                    {/each}
                  </SelectContent>
                </Select>
              </div>

              <div class="flex flex-col gap-1">
                <Label variant="hint">
                  {t('printerHawk.settings.cameraMode')}
                </Label>
                <Select
                  items={cameraModeItems}
                  disabled={!settings.page_setup}
                  bind:value={editedPrinterConfig.camera_mode}
                >
                  <SelectTrigger size="sm" class="w-full">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    {#each cameraModeItems as opt (opt.value)}
                      <SelectItem value={opt.value}>{opt.label}</SelectItem>
                    {/each}
                  </SelectContent>
                </Select>
              </div>
            </div>

            {#if editedPrinterConfig.camera_mode === 'url'}
              <div class="flex flex-col gap-1">
                <Label variant="hint">
                  {t('printerHawk.settings.liveFrameUrl')}
                </Label>
                <Input
                  type="text"
                  size="sm"
                  disabled={!settings.page_setup}
                  placeholder={t('printerHawk.settings.liveFrameHint')}
                  value={editedPrinterConfig.live_frame_url}
                  oninput={(e: Event) => {
                    editedPrinterConfig.live_frame_url = inputValue(e);
                  }}
                />
              </div>
            {/if}

            <div class="grid grid-cols-2 gap-3 md:grid-cols-4">
              <div class="flex flex-col gap-1">
                <Label variant="hint">
                  {t('printerHawk.settings.warmupSec')}
                </Label>
                <Input
                  type="number"
                  size="sm"
                  disabled={!settings.page_setup}
                  value={String(editedPrinterConfig.warmup_seconds)}
                  oninput={(e: Event) => {
                    editedPrinterConfig.warmup_seconds = Number(inputValue(e));
                  }}
                />
              </div>
              <div class="flex flex-col gap-1">
                <Label variant="hint">
                  {t('printerHawk.settings.fallbackWait')}
                </Label>
                <Input
                  type="number"
                  size="sm"
                  disabled={!settings.page_setup}
                  value={String(editedPrinterConfig.fallback_max_wait_sec)}
                  oninput={(e: Event) => {
                    editedPrinterConfig.fallback_max_wait_sec = Number(
                      inputValue(e),
                    );
                  }}
                />
              </div>
              <div class="flex flex-col gap-1">
                <Label variant="hint">
                  {t('printerHawk.settings.zMinStep')}
                </Label>
                <Input
                  type="number"
                  step="0.01"
                  size="sm"
                  disabled={!settings.page_setup}
                  value={String(editedPrinterConfig.z_min_step_mm)}
                  oninput={(e: Event) => {
                    editedPrinterConfig.z_min_step_mm = Number(inputValue(e));
                  }}
                />
              </div>
              <div class="flex flex-col gap-1">
                <Label variant="hint">
                  {t('printerHawk.settings.zMaxStep')}
                </Label>
                <Input
                  type="number"
                  step="0.1"
                  size="sm"
                  disabled={!settings.page_setup}
                  value={String(editedPrinterConfig.z_max_step_mm)}
                  oninput={(e: Event) => {
                    editedPrinterConfig.z_max_step_mm = Number(inputValue(e));
                  }}
                />
              </div>
              <div class="flex flex-col gap-1">
                <Label variant="hint">
                  {t('printerHawk.settings.zSettle')}
                </Label>
                <Input
                  type="number"
                  step="0.5"
                  size="sm"
                  disabled={!settings.page_setup}
                  value={String(editedPrinterConfig.z_settle_seconds)}
                  oninput={(e: Event) => {
                    editedPrinterConfig.z_settle_seconds = Number(
                      inputValue(e),
                    );
                  }}
                />
              </div>
              <div class="flex flex-col gap-1">
                <Label variant="hint">
                  {t('printerHawk.settings.minInterval')}
                </Label>
                <Input
                  type="number"
                  size="sm"
                  disabled={!settings.page_setup}
                  value={String(editedPrinterConfig.min_check_interval_sec)}
                  oninput={(e: Event) => {
                    editedPrinterConfig.min_check_interval_sec = Number(
                      inputValue(e),
                    );
                  }}
                />
              </div>
              <div class="flex flex-col gap-1">
                <Label variant="hint">
                  {t('printerHawk.settings.s1Threshold')}
                </Label>
                <Input
                  type="number"
                  step="0.05"
                  size="sm"
                  disabled={!settings.page_setup}
                  value={String(editedPrinterConfig.stage1_fail_threshold)}
                  oninput={(e: Event) => {
                    editedPrinterConfig.stage1_fail_threshold = Number(
                      inputValue(e),
                    );
                  }}
                />
              </div>
              <div class="flex flex-col gap-1">
                <Label variant="hint">
                  {t('printerHawk.settings.s2Threshold')}
                </Label>
                <Input
                  type="number"
                  step="0.05"
                  size="sm"
                  disabled={!settings.page_setup}
                  value={String(editedPrinterConfig.stage2_pause_threshold)}
                  oninput={(e: Event) => {
                    editedPrinterConfig.stage2_pause_threshold = Number(
                      inputValue(e),
                    );
                  }}
                />
              </div>
            </div>

            <div class="flex flex-col gap-1">
              <Label variant="hint">
                {t('printerHawk.settings.promptNotes')}
              </Label>
              <Textarea
                rows={2}
                disabled={!settings.page_setup}
                bind:value={editedPrinterConfig.custom_prompt_notes}
              />
            </div>
          </CardContent>
        </Card>

        <Card>
          <CardHeader class="pb-2">
            <CardTitle class="text-sm">
              {t('printerHawk.settings.mqttSection')}
            </CardTitle>
          </CardHeader>
          <CardContent class="grid grid-cols-1 gap-3 text-xs md:grid-cols-3">
            <div class="flex flex-col gap-1">
              <Label variant="hint">
                {t('printerHawk.settings.mqttHost')}
              </Label>
              <Input
                type="text"
                size="sm"
                disabled={!settings.page_setup}
                value={settings.mqtt.host}
                oninput={(e: Event) => {
                  if (settings) settings.mqtt.host = inputValue(e);
                }}
              />
            </div>
            <div class="flex flex-col gap-1">
              <Label variant="hint">
                {t('printerHawk.settings.mqttPort')}
              </Label>
              <Input
                type="number"
                size="sm"
                disabled={!settings.page_setup}
                value={String(settings.mqtt.port)}
                oninput={(e: Event) => {
                  if (settings) {
                    settings.mqtt.port = Number(inputValue(e));
                  }
                }}
              />
            </div>
            <div class="flex flex-col gap-1">
              <Label variant="hint">
                {t('printerHawk.settings.mqttPrefix')}
              </Label>
              <Input
                type="text"
                size="sm"
                disabled={!settings.page_setup}
                value={settings.mqtt.prefix}
                oninput={(e: Event) => {
                  if (settings) settings.mqtt.prefix = inputValue(e);
                }}
              />
            </div>
          </CardContent>
        </Card>

        {#if settings.page_setup}
          <div>
            <Button disabled={saving} onclick={saveAllSettings}>
              {saving
                ? t('printerHawk.actions.saving')
                : t('printerHawk.actions.save')}
            </Button>
          </div>
        {/if}
      {/if}
    </TabsContent>
  </Tabs>
</div>
