import http from 'node:http';
import { readFile } from 'node:fs/promises';
import path from 'node:path';
const root = process.cwd();
const publicCSS = ['chunnel.css','chunnel-ui.css','chunnel-inputs.css','chunnel-foundation.css','chunnel-portal.css','chunnel-account.css','chunnel-glass.css','chunnel-selects.css'];
const adminCSS = ['chunnel.css','chunnel-admin.css','chunnel-inputs.css','chunnel-foundation.css','chunnel-admin-workspace.css','chunnel-selects.css'];
http.createServer(async (req,res) => {
  const url = new URL(req.url, 'http://localhost');
  if (url.pathname.startsWith('/assets/')) {
    const file = path.resolve(root, 'internal/coreui', '.' + url.pathname);
    if (!file.startsWith(path.join(root,'internal/coreui/assets') + path.sep)) { res.writeHead(403).end(); return; }
    try { const data = await readFile(file); res.setHeader('Content-Type', file.endsWith('.css')?'text/css':file.endsWith('.js')?'text/javascript':'application/octet-stream'); res.end(data); } catch {res.writeHead(404).end();} return;
  }
  const admin = url.searchParams.has('admin');
  const css = admin ? adminCSS : publicCSS;
  res.setHeader('Content-Type','text/html; charset=utf-8');
  res.end(`<!doctype html><html lang="zh-CN" data-input-style="fluent" data-style="${url.searchParams.has('glass')?'liquid-glass':'classic'}"><head><meta name="viewport" content="width=device-width,initial-scale=1">${css.map(x=>`<link rel="stylesheet" href="/assets/css/${x}">`).join('')}<title>控件对齐验证</title><style>body{margin:0;padding:24px} main{max-width:700px;margin:auto} section{margin:20px 0} .row{display:flex;gap:8px;align-items:center;flex-wrap:wrap} select{width:180px}.admin-main{margin:0!important;padding:0!important}.fluent-input-group{display:flex;align-items:center}input{min-width:0} </style></head><body class="${admin?'admin-workbench':'portal-site'}"><main class="${admin?'admin-main':'portal-page'}"><h1>控件对齐验证</h1><section><form class="discovery-sort-form"><select id="sort"><option value="updated">最近更新</option><option value="long">这是一段很长的中文 English 排序说明</option></select><button class="ui-button ui-button-small" type="reset">重置</button></form></section><section class="row"><select disabled><option>禁用状态</option></select><select dir="rtl"><option>最近更新 RTL</option></select><select multiple size="3"><option>第一项</option><option>第二项</option></select></section><section class="row" style="--fluent-control-height:44px"><select id="tall"><option>最近更新</option></select><input placeholder="标准输入框"></section>${admin?'<section class="row"><form class="fluent-input-group" role="search"><input placeholder="搜索内容"><button type="button">搜索</button></form><select><option>全部状态</option></select></section>':''}</main>${url.searchParams.has('native')?'':'<script src="/assets/js/chunnel-selects.js"></script>'}</body></html>`);
}).listen(Number(process.env.PORT || 19362),'127.0.0.1');
