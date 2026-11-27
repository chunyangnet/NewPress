import { spawn } from 'node:child_process';
const child = spawn('go', ['test', './internal/httpserver', '-run', '^TestWorkbenchBrowserFixture$', '-v', '-timeout', '60m'], {stdio:'inherit', env:{...process.env, NEWPRESS_WORKBENCH_BROWSER_ADDR:`127.0.0.1:${process.env.PORT || 19366}`}});
child.on('exit', code => process.exit(code ?? 1));
for (const signal of ['SIGINT','SIGTERM']) process.on(signal, () => child.kill(signal));
