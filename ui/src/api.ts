import type { ModuleHost } from '@prusa/connect-ui/slots';
import { bind, type components } from 'printer-hawk-client';

export type StatusPayload = components['schemas']['StatusPayload'];
export type PrinterSummary = components['schemas']['PrinterSummary'];
export type CheckRecord = components['schemas']['CheckRecord'];
export type RuntimeConfig = components['schemas']['RuntimeConfig'];
export type PrinterConfig = components['schemas']['PrinterConfig'];
export type ProviderSlot = components['schemas']['ProviderSlot'];
export type StageResult = components['schemas']['StageResult'];
export type ProviderTestResult = components['schemas']['ProviderTestResult'];
export type ActionResult = components['schemas']['ActionResult'];

export const hawkOn = (host: ModuleHost) => {
  const routes = bind(host.client);

  const fetchImageUrl = async (
    call: Promise<Response>,
  ): Promise<string | undefined> => {
    const res = await call;
    if (!res.ok) return undefined;
    return URL.createObjectURL(await res.blob());
  };

  return {
    status: (printer?: string, signal?: AbortSignal): Promise<StatusPayload> =>
      routes.getStatus(printer ? { query: { printer } } : {}, signal),

    getSettings: (signal?: AbortSignal): Promise<RuntimeConfig> =>
      routes.getSettings({}, signal),

    updateSettings: (
      body: RuntimeConfig,
      signal?: AbortSignal,
    ): Promise<RuntimeConfig> => routes.updateSettings({ body }, signal),

    testProvider: (
      slot: string,
      config: ProviderSlot,
      signal?: AbortSignal,
    ): Promise<ProviderTestResult> =>
      routes.testProvider({ body: { slot, config } }, signal),

    inspect: (
      printer: string,
      verify: boolean,
      signal?: AbortSignal,
    ): Promise<CheckRecord> =>
      routes.inspectPrinter({ path: { printer }, query: { verify } }, signal),

    falseAlarm: (
      printer: string,
      signal?: AbortSignal,
    ): Promise<ActionResult> =>
      routes.markFalseAlarm({ path: { printer } }, signal),

    clearAlert: (
      printer: string,
      signal?: AbortSignal,
    ): Promise<ActionResult> =>
      routes.clearAlert({ path: { printer } }, signal),

    pausePrinter: (
      printer: string,
      signal?: AbortSignal,
    ): Promise<ActionResult> =>
      routes.pausePrinter({ path: { printer } }, signal),

    stopPrinter: (
      printer: string,
      signal?: AbortSignal,
    ): Promise<ActionResult> =>
      routes.stopPrinter({ path: { printer } }, signal),

    liveFrameUrl: (printer: string, signal?: AbortSignal) =>
      fetchImageUrl(routes.getLiveFrame({ path: { printer } }, signal)),

    storedFrameUrl: (printer: string, frameId: string, signal?: AbortSignal) =>
      fetchImageUrl(
        routes.getStoredFrame({ path: { printer, frameId } }, signal),
      ),
  };
};
