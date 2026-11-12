import { createApp } from './app.js';
import { loadConfig } from './config.js';

const config = loadConfig();
const app = createApp(config);
const [host, port] = config.listen.split(':');
const server = app.listen(Number(port), host, () => {
  console.log(JSON.stringify({ time: new Date().toISOString(), level: 'info', msg: 'starting', service: 'frontend_provider', addr: config.listen }));
});
server.requestTimeout = 30_000;
server.headersTimeout = 10_000;
server.keepAliveTimeout = 30_000;
for (const sig of ['SIGTERM', 'SIGINT']) process.on(sig, () => server.close(() => process.exit(0)));
