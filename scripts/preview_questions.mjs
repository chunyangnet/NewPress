import {spawn} from 'node:child_process';
const child=spawn('go',['test','./internal/httpserver','-run','^TestQuestionsBrowserFixture$','-v','-timeout','60m'],{stdio:'inherit',env:{...process.env,NEWPRESS_QUESTIONS_BROWSER_ADDR:'127.0.0.1:19364'}});
child.on('exit',code=>process.exit(code??1));
for(const signal of ['SIGINT','SIGTERM'])process.on(signal,()=>child.kill(signal));
