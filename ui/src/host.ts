import type { ModuleHost } from '@prusa/connect-ui/slots';

let held: ModuleHost | undefined;

export const holdHost = (host: ModuleHost): void => {
  if (host.signal.aborted) return;
  held = host;
  host.signal.addEventListener(
    'abort',
    () => {
      if (held === host) held = undefined;
    },
    { once: true },
  );
};

export const heldHost = (): ModuleHost => {
  if (!held) {
    throw new Error('printer-hawk drew before module setup ran');
  }
  return held;
};
