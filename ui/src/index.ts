import { defineModule } from '@prusa/connect-ui/slots';

import { holdHost } from './host';
import MainPage from './main-page.svelte';
import PrinterTab from './printer-tab.svelte';
import './styles.css';

export default defineModule((host) => {
  holdHost(host);
  host.register.page('printer-hawk', MainPage);
  host.register.deviceTab('printer-hawk', PrinterTab);
});
