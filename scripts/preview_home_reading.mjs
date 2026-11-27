import fs from 'node:fs';
import path from 'node:path';
import { spawn } from 'node:child_process';
const root = process.cwd();
const sandbox = path.join(root, 'output', 'home-reading-preview');
fs.mkdirSync(sandbox, {recursive:true});
for (const name of ['themes','plugins']) {
 fs.mkdirSync(path.join(sandbox,name),{recursive:true});
 if(name==='themes') fs.cpSync(path.join(root,'themes','chunnel'),path.join(sandbox,name,'chunnel'),{recursive:true});
}
const schemaPath = path.join(sandbox,'themes','chunnel','settings.schema.json');
const schema = JSON.parse(fs.readFileSync(schemaPath,'utf8'));
schema.properties.home_reading_guide_visibility.default = 'both';
schema.properties.home_updated_visibility.default = 'both';
fs.writeFileSync(schemaPath,JSON.stringify(schema,null,2));
const child=spawn('go',['run','./cmd/newpress','serve'],{cwd:root,stdio:'inherit',env:{...process.env,NEWPRESS_ADDR:'127.0.0.1:19346',NEWPRESS_PUBLIC_URL:'http://localhost:19346',NEWPRESS_DATA_DIR:path.join(sandbox,'data'),NEWPRESS_THEMES_DIR:path.join(sandbox,'themes'),NEWPRESS_PLUGINS_DIR:path.join(sandbox,'plugins'),NEWPRESS_DATABASE_URL:'',NEWPRESS_SEED_DEMO:'true'}});
child.on('exit',code=>process.exit(code??1));
for(const signal of ['SIGINT','SIGTERM'])process.on(signal,()=>child.kill(signal));
