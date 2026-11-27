#!/usr/bin/env python3
"""Build and verify local AI Pro Max 1.1.0 artifacts; never signs or publishes."""
from pathlib import Path
from tempfile import mkdtemp, gettempdir
from zipfile import ZipFile, ZipInfo, ZIP_DEFLATED
import hashlib, json, os, shutil, subprocess, sys
root=Path(__file__).resolve().parents[1]
repo=root.parent/'newpress_Plug'
if repo.is_dir():
 release=repo/'packages/ai-pro-max/1.1.0'
 source=repo/'sources/ai-pro-max/1.1.0'
 wp=repo/'wordpress/newpress-ai-pro-max'
else:
 # The downloadable source archive keeps extensions beside the host tree.
 release=root.parent/'packages/ai-pro-max/1.1.0'
 source=root.parent/'extensions/ai-pro-max'
 wp=root.parent/'extensions/newpress-ai-pro-max'
work=Path(mkdtemp(prefix='newpress-aipm-open-source-')).resolve(); (Path(gettempdir())/'newpress-aipm-os-stage.path').write_text(str(work),encoding='utf8')
dist=root/'output/ai-pro-max-1.1.0'; name='ai-pro-max-1.1.0'; companion='newpress-ai-pro-max-wordpress-1.1.0'; bundle='ai-pro-max-1.1.0-source'
assert not (root/'internal/httpserver/ai_pro_local_fixture_test.go').exists()
subprocess.run([sys.executable,str(root/'scripts/build_extension.py'),'--kind','plugin','--source',str(source),'--output',str(work/(name+'.zip'))],check=True)
files={}
def collect(base, paths,prefix=''):
 for p in paths:
  if p.is_symlink() or not p.is_file():continue
  rel=p.relative_to(base);parts=rel.parts
  if any(x.startswith('.') or x in ('docs','output','var','var-updates','node_modules','__pycache__') for x in parts) or p.suffix.lower() in ('.tmp','.log','.db','.exe','.key','.pem','.pyc','.zip','.bak'):continue
  dest=prefix+rel.as_posix();assert dest not in files,dest;files[dest]=p

def zipfiles(path, entries):
 with ZipFile(path,'x') as z:
  for n,p in sorted(entries.items()):
   i=ZipInfo(n,date_time=(2026,1,1,0,0,0));i.create_system=3;i.external_attr=0o100644<<16
   z.writestr(i,p.read_bytes(),compress_type=ZIP_DEFLATED,compresslevel=6)
 with ZipFile(path) as z:
  assert z.testzip() is None and set(z.namelist())==set(entries)

collect(wp,wp.rglob('*'),'newpress-ai-pro-max/');zipfiles(work/(companion+'.zip'),files);files={}
for top in ('cmd','internal','migrations','third_party','themes/chunnel','tests/frontend','scripts'):
 collect(root,(root/top).rglob('*'),'newpress/')
for top in ('go.mod','go.sum','LICENSE','THIRD_PARTY_NOTICES.md','README.md','CONTRIBUTING.md','SECURITY.md','package.json','package-lock.json'):
 p=root/top
 if p.is_file():collect(root,[p],'newpress/')
files['newpress/.env.example']=root/'.env.example'
collect(source,source.rglob('*'),'extensions/ai-pro-max/');collect(wp,wp.rglob('*'),'extensions/newpress-ai-pro-max/')
collect(wp.parent/'tests',(wp.parent/'tests').rglob('*'),'extensions/tests/')
(work/'BUILD.md').write_text('''# AI Pro Max 1.1.0 开源代码

`newpress/` 包含宿主 Go 源码、编译资源和 Chunnel 主题。`extensions/ai-pro-max/` 是 NewPress 插件的源目录，`extensions/newpress-ai-pro-max/` 是可选的 WordPress 连接器。宿主运行说明见 `newpress/README.md`，插件操作说明见 `extensions/ai-pro-max/USAGE.md`。私有文档不随源码包分发。

在 `newpress/` 目录运行 `go build -trimpath -buildvcs=false -o newpress ./cmd/newpress`。Windows 输出文件可命名为 `newpress.exe`。Go 版本和依赖由 `go.mod` 和 `go.sum` 指定。生成的前端资源已随包提供，修改界面后运行 `npm ci` 和 `npm run build:frontend`。

`python scripts/build_extension.py --kind plugin --source ../extensions/ai-pro-max --output ../ai-pro-max-1.1.0.zip` 可重新构建描述包。`python scripts/build_ai_pro_max_release.py` 会重新打包源码、两种插件 ZIP，并从解压的源码构建 Windows amd64 宿主和 SHA256 校验清单。该脚本只覆盖本地 1.1.0 候选产物，不签名、不发布。

在解压目录运行 `php extensions/tests/ai-pro-max.php` 可执行连接器回归测试。WordPress 连接器可复制到 WordPress 的 `wp-content/plugins/newpress-ai-pro-max/`。

宿主和 NewPress 插件使用 Apache-2.0 许可证，WordPress 连接器使用 GPL-2.0-or-later。第三方声明随对应目录提供。
''',encoding='utf8');files['BUILD.md']=work/'BUILD.md'
zipfiles(work/(bundle+'.zip'),files)
with ZipFile(work/(name+'.zip')) as z:
 manifest=json.loads(z.read('plugin.json'));assert (manifest['version'],manifest['slug'])==('1.1.0','ai-pro-max');assert z.testzip() is None and set(z.namelist())=={p.name for p in source.iterdir() if p.is_file()};assert all(z.read(p.name)==p.read_bytes() for p in source.iterdir() if p.is_file())
with ZipFile(work/(companion+'.zip')) as z:
 assert b'Version: 1.1.0' in z.read('newpress-ai-pro-max/newpress-ai-pro-max.php');assert b'Stable tag: 1.1.0' in z.read('newpress-ai-pro-max/readme.txt')
with ZipFile(work/(bundle+'.zip')) as z:z.extractall(work/'verify')
exe=work/'newpress.exe';subprocess.run(['go','build','-trimpath','-buildvcs=false','-o',str(exe),'./cmd/newpress'],cwd=work/'verify/newpress',check=True,env={**os.environ,'GOOS':'windows','GOARCH':'amd64','CGO_ENABLED':'0','GOWORK':'off'})
subprocess.run(['go','version','-m',str(exe)],stdout=(work/'windows-build.txt').open('w',encoding='utf8'),check=True);assert 'GOOS=windows' in (work/'windows-build.txt').read_text('utf8')
sha=lambda p:hashlib.file_digest(p.open('rb'),'sha256').hexdigest()
def deliver(a,b):
 b.parent.mkdir(parents=True,exist_ok=True);tmp=b.with_name(b.name+'.verified-'+work.name[-8:]);shutil.copyfile(a,tmp);assert sha(a)==sha(tmp);os.replace(tmp,b);assert sha(b)==sha(a)
for n in (name,companion,bundle):deliver(work/(n+'.zip'),release/(n+'.zip'))
deliver(exe,dist/'windows-amd64/newpress.exe')
(work/'SHA256SUMS.txt').write_text(''.join(sha(release/(n+'.zip'))+'  '+n+'.zip\n' for n in (name,companion,bundle)),encoding='ascii');deliver(work/'SHA256SUMS.txt',release/'SHA256SUMS.txt');(dist/'windows-amd64/SHA256SUMS.txt').write_text(sha(exe)+'  newpress.exe\n',encoding='ascii')
print(f'{len(files)} verified source files; Windows host built from extracted archive');print((work/'SHA256SUMS.txt').read_text('ascii'))
