"""Bounded HTTPS probes and content-verified deployment source selection."""
from concurrent.futures import ThreadPoolExecutor, as_completed
import hashlib
import json
from pathlib import Path
import re
import time
import urllib.error
import urllib.parse
import urllib.request


class SourceError(RuntimeError):
    pass


class SafeRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, request, response, code, message, headers, target):
        if urllib.parse.urlsplit(target).scheme != "https":
            raise SourceError("拒绝降级到 HTTP")
        redirected = super().redirect_request(request, response, code, message, headers, target)
        if redirected and urllib.parse.urlsplit(target).netloc != urllib.parse.urlsplit(request.full_url).netloc:
            redirected.remove_header("Authorization")
        return redirected


def https_base(value):
    parsed = urllib.parse.urlsplit(value)
    try:
        port = parsed.port
    except ValueError as error:
        raise SourceError("镜像源端口无效") from error
    if (parsed.scheme != "https" or not parsed.hostname or parsed.username is not None or
            parsed.password is not None or parsed.query or parsed.fragment or
            any(char.isspace() or ord(char) < 32 for char in value) or "\\" in value or port == 0):
        raise SourceError("镜像源必须是无凭据、无查询参数的 HTTPS 地址")
    return value.rstrip("/")


def image_parts(reference):
    match = re.fullmatch(r"([A-Za-z0-9][A-Za-z0-9._:/-]*)@sha256:([a-f0-9]{64})", reference)
    if not match:
        raise SourceError("容器镜像必须保留 SHA-256 digest")
    name, digest = match.groups()
    components = name.split("/")
    if len(components) > 1 and ("." in components[0] or ":" in components[0] or components[0] == "localhost"):
        registry, repository = components[0], "/".join(components[1:])
    else:
        registry, repository = "docker.io", name
    repository = repository.rsplit(":", 1)[0]
    component = r"[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*"
    if not re.fullmatch(component + r"(?:/" + component + r")*", repository):
        raise SourceError("容器镜像仓库路径无效")
    if not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9.-]*(?::[0-9]+)?", registry):
        raise SourceError("容器镜像仓库地址无效")
    if registry == "docker.io" and "/" not in repository:
        repository = "library/" + repository
    return registry, repository, "sha256:" + digest


def image_candidates(reference, policy="auto", extra=()):
    registry, repository, digest = image_parts(reference)
    candidates = [("官方", reference)]
    if policy == "auto":
        if registry in ("docker.io", "gcr.io"):
            candidates.append(("DaoCloud", f"m.daocloud.io/{registry}/{repository}@{digest}"))
            mirror = "docker.m.daocloud.io" if registry == "docker.io" else "gcr.m.daocloud.io"
            candidates.append(("DaoCloud 备用域名", f"{mirror}/{repository}@{digest}"))
        if registry == "docker.io":
            candidates.append(("毫秒镜像 (1ms.run)", f"docker.1ms.run/{repository}@{digest}"))
        for base in extra:
            parsed = urllib.parse.urlsplit(https_base(base))
            prefix = parsed.netloc + parsed.path
            candidates.append(("自定义 " + parsed.netloc, f"{prefix}/{repository}@{digest}"))
    return list(dict.fromkeys(candidates))


def pinned_images(root):
    dockerfile = (root / "Dockerfile").read_text(encoding="utf-8")
    compose = (root / "compose.yaml").read_text(encoding="utf-8")
    result = {}
    for key, variable in (("go", "NEWPRESS_GO_IMAGE"), ("runtime", "NEWPRESS_RUNTIME_IMAGE")):
        match = re.search(r"^ARG " + variable + r"=([^\s]+)$", dockerfile, re.M)
        if not match:
            raise SourceError(f"Dockerfile 缺少 {variable} 固定镜像")
        image_parts(match[1])
        result[key] = match[1]
    for key, variable in (("postgres", "NEWPRESS_POSTGRES_IMAGE"), ("proxy", "NEWPRESS_PROXY_IMAGE")):
        match = re.search(r"\$\{" + variable + r":-([^}]+)\}", compose)
        if not match:
            raise SourceError(f"compose.yaml 缺少 {variable} 固定镜像")
        image_parts(match[1])
        result[key] = match[1]
    return result


def security_repo(distro, base):
    if distro["ID"] == "debian":
        return base.removesuffix("/debian") + "/debian-security"
    if base == "https://archive.ubuntu.com/ubuntu":
        return "https://security.ubuntu.com/ubuntu"
    return base


def apt_stanzas(distro, base, arch, key):
    codename = distro["VERSION_CODENAME"]
    def stanza(url, suites):
        return (f"Types: deb\nURIs: {url}\nSuites: {suites}\nComponents: main\n"
                f"Architectures: {arch}\nSigned-By: {key}\n")
    return (stanza(base, f"{codename} {codename}-updates") + "\n" +
            stanza(security_repo(distro, base), f"{codename}-security"))


class Selector:
    def __init__(self, policy="auto", timeout=6, log=print, reporter=None):
        self.policy = policy
        self.timeout = timeout
        self.log = log
        self.reporter = reporter
        self.opener = urllib.request.build_opener(SafeRedirect())

    def read(self, url, *, headers=None, limit=256 * 1024, sample=False, allow_empty=False):
        https_base(url.split("?", 1)[0])
        request_headers = {"User-Agent": "NewPress-Deploy/1", "Accept-Encoding": "identity"}
        request_headers.update(headers or {})
        if sample:
            request_headers["Range"] = f"bytes=0-{limit - 1}"
        deadline = time.monotonic() + self.timeout
        request = urllib.request.Request(url, headers=request_headers)
        with self.opener.open(request, timeout=self.timeout) as response:
            if response.status not in (200, 206):
                raise SourceError(f"HTTP {response.status}")
            if "text/html" in response.headers.get("Content-Type", "").lower():
                raise SourceError("返回了 HTML 页面，文件不可用")
            chunks, size = [], 0
            maximum = limit if sample else limit + 1
            while size < maximum:
                if time.monotonic() > deadline:
                    raise SourceError("采样超时")
                chunk = response.read(min(16384, maximum - size))
                if not chunk:
                    break
                chunks.append(chunk)
                size += len(chunk)
            if not sample and size > limit:
                raise SourceError("响应超过大小限制")
            if not size and not allow_empty:
                raise SourceError("返回了空文件")
            payload = b"".join(chunks)
            if payload.lstrip().lower().startswith((b"<!doctype html", b"<html", b"<?xml")):
                raise SourceError("返回了错误页面，文件不可用")
            return payload

    def rank(self, label, candidates, probe):
        if self.reporter:
            self.reporter.source_start(label, len(candidates))
        def measure(candidate):
            name, url = candidate
            started = time.monotonic()
            probe(url)
            return {"name": name, "url": url, "seconds": time.monotonic() - started}

        available, failures = [], []
        with ThreadPoolExecutor(max_workers=min(6, len(candidates) or 1)) as pool:
            pending = {pool.submit(measure, item): item for item in candidates}
            for future in as_completed(pending):
                name, _ = pending[future]
                try:
                    entry = future.result()
                    available.append(entry)
                    if self.reporter:
                        self.reporter.source_result(label, name, seconds=entry["seconds"])
                    else:
                        self.log(f"  {label} / {name}：可用，采样 {entry['seconds']:.2f} 秒")
                except (OSError, ValueError, KeyError, TypeError, AttributeError, SourceError, urllib.error.URLError) as error:
                    # Errors may contain a token-bearing redirect URL; report only
                    # a status/type, never authentication response bodies or URLs.
                    detail = f"HTTP {error.code}" if isinstance(error, urllib.error.HTTPError) else type(error).__name__
                    if isinstance(error, urllib.error.HTTPError):
                        error.close()
                    failures.append(name)
                    if self.reporter:
                        self.reporter.source_result(label, name, error=detail)
                    else:
                        self.log(f"  {label} / {name}：不可用（{detail}）")
        available.sort(key=lambda item: item["seconds"])
        if not available:
            raise SourceError(f"{label} 的候选源均不可用：{', '.join(failures)}")
        if self.reporter:
            self.reporter.source_selected(label, available)
        else:
            self.log(f"{label}：优先使用本次采样最快的 {available[0]['name']}，失败后依次切换。")
        return available

    def docker_apt(self, distro, arch):
        family, codename = distro.get("ID"), distro.get("VERSION_CODENAME", "")
        if family not in ("debian", "ubuntu") or not re.fullmatch(r"[a-z]+", codename):
            raise SourceError("Docker 自动安装支持具有有效发行版代号的 Debian/Ubuntu")
        bases = [("Docker 官方", "https://download.docker.com")]
        if self.policy == "auto":
            bases += [("清华", "https://mirrors.tuna.tsinghua.edu.cn/docker-ce"),
                      ("中科大", "https://mirrors.ustc.edu.cn/docker-ce"),
                      ("阿里云", "https://mirrors.aliyun.com/docker-ce")]
        candidates = [(name, base + "/linux/" + family) for name, base in bases]

        def probe(base):
            release = self.read(f"{base}/dists/{codename}/Release")
            if b"Architectures:" not in release or arch.encode() not in release or b"Components:" not in release:
                raise SourceError("软件仓库元数据不匹配")
            packages = self.read(f"{base}/dists/{codename}/stable/binary-{arch}/Packages.gz", sample=True)
            if not packages.startswith(b"\x1f\x8b"):
                raise SourceError("软件包索引不是 gzip")

        return self.rank("Docker 安装包", candidates, probe)

    def os_apt(self, distro, arch):
        family, codename = distro.get("ID"), distro.get("VERSION_CODENAME", "")
        if family not in ("debian", "ubuntu") or not re.fullmatch(r"[a-z]+", codename):
            raise SourceError("系统软件源检测支持 Debian/Ubuntu")
        directory = "ubuntu-ports" if family == "ubuntu" and arch == "arm64" else family
        official = ("https://deb.debian.org/debian" if family == "debian" else
                    "https://ports.ubuntu.com/ubuntu-ports" if arch == "arm64" else "https://archive.ubuntu.com/ubuntu")
        candidates = [("系统官方", official)]
        if self.policy == "auto":
            candidates += [(name, base + "/" + directory) for name, base in (
                ("清华", "https://mirrors.tuna.tsinghua.edu.cn"),
                ("中科大", "https://mirrors.ustc.edu.cn"),
                ("阿里云", "https://mirrors.aliyun.com"))]

        def probe(base):
            for location, suite in ((base, codename), (base, codename + "-updates"),
                                    (security_repo(distro, base), codename + "-security")):
                release = self.read(f"{location}/dists/{suite}/InRelease")
                if (not release.startswith(b"-----BEGIN PGP SIGNED MESSAGE-----") or
                        not re.search(rb"^(?:Suite|Codename): " + re.escape(suite.encode()) + rb"\s*$", release, re.M)):
                    raise SourceError("系统仓库发行版不匹配")

        return self.rank("系统依赖", candidates, probe)

    def go_modules(self, root=None):
        module = "github.com/jackc/pgx/v5"
        root = root or Path(__file__).resolve().parent.parent
        match = re.search(r"^\s*" + re.escape(module) + r"\s+(v[^\s]+)", (root / "go.mod").read_text(encoding="utf-8"), re.M)
        if not match:
            raise SourceError("go.mod 缺少模块探测所需的 pgx 固定版本")
        version = match[1]
        candidates = [("Go 官方", "https://proxy.golang.org")]
        if self.policy == "auto":
            candidates += [("七牛 GoProxy", "https://goproxy.cn"), ("GoProxy.io", "https://goproxy.io")]

        def probe(base):
            # A module ZIP tests a real transfer and the proxy's checksum relay.
            checksum = base + "/sumdb/sum.golang.org"
            try:
                self.read(checksum + "/supported", allow_empty=True)
            except urllib.error.HTTPError as error:
                if base != "https://proxy.golang.org" or error.code not in (404, 410):
                    raise
                # The official module proxy may delegate directly to sum.golang.org.
                checksum = "https://sum.golang.org"
            data = self.read(f"{base}/{module}/@v/{version}.zip", sample=True)
            if not data.startswith(b"PK\x03\x04"):
                raise SourceError("模块下载不是 ZIP")
            tile = self.read(f"{checksum}/lookup/{module}@{version}")
            if f"{module} {version} h1:".encode() not in tile:
                raise SourceError("校验数据库代理不可用")

        return self.rank("Go 模块与校验数据库", candidates, probe)

    def go_archive(self, metadata):
        candidates = [("Go 官方", "https://go.dev/dl/"), ("Go 中国", "https://golang.google.cn/dl/")]
        if self.policy == "auto":
            candidates.append(("中科大", "https://mirrors.ustc.edu.cn/golang/"))
        candidates = [(name, base + metadata["filename"]) for name, base in candidates]

        def probe(url):
            if not self.read(url, sample=True).startswith(b"\x1f\x8b"):
                raise SourceError("工具链下载不是 gzip")

        return self.rank("Go 工具链", candidates, probe)

    def registry_read(self, registry, repository, suffix, auth, *, sample=False):
        host = "registry-1.docker.io" if registry == "docker.io" else registry
        url = f"https://{host}/v2/{repository}/{suffix}"
        headers = {"Accept": "application/vnd.oci.image.index.v1+json, application/vnd.docker.distribution.manifest.list.v2+json, application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.v2+json"}
        if auth.get("token"):
            headers["Authorization"] = "Bearer " + auth["token"]
        try:
            return self.read(url, headers=headers, sample=sample, limit=65536 if sample else 2 * 1024 * 1024)
        except urllib.error.HTTPError as error:
            if error.code != 401 or auth.get("token"):
                raise
            challenge = error.headers.get("WWW-Authenticate", "")
            error.close()
            if not challenge.lower().startswith("bearer "):
                raise SourceError("仓库需要登录；自动测速只支持公开拉取") from error
            fields = dict(re.findall(r'([a-z]+)="([^"\r\n]+)"', challenge))
            realm = https_base(fields.get("realm", ""))
            query = urllib.parse.urlencode({"service": fields.get("service", host), "scope": f"repository:{repository}:pull"})
            token = json.loads(self.read(realm + "?" + query, limit=65536))
            if not isinstance(token, dict):
                raise SourceError("仓库令牌响应格式无效")
            auth["token"] = token.get("token") or token.get("access_token")
            if not isinstance(auth["token"], str) or not auth["token"]:
                raise SourceError("未取得只读拉取令牌")
            return self.registry_read(registry, repository, suffix, auth, sample=sample)

    def image(self, label, reference, arch, extra=()):
        def probe(candidate):
            registry, repository, digest = image_parts(candidate)
            auth = {}

            def manifest(expected):
                payload = self.registry_read(registry, repository, "manifests/" + expected, auth)
                if "sha256:" + hashlib.sha256(payload).hexdigest() != expected:
                    raise SourceError("镜像清单 digest 不匹配")
                data = json.loads(payload)
                if not isinstance(data, dict):
                    raise SourceError("镜像清单格式无效")
                return data

            data = manifest(digest)
            if "manifests" in data:
                targets = [item for item in data["manifests"] if item.get("platform", {}).get("os") == "linux"
                           and item.get("platform", {}).get("architecture") == arch]
                if not targets:
                    raise SourceError("镜像不含当前 CPU 架构")
                data = manifest(targets[0]["digest"])
            else:
                config_digest = data["config"]["digest"]
                config_payload = self.registry_read(registry, repository, "blobs/" + config_digest, auth)
                if "sha256:" + hashlib.sha256(config_payload).hexdigest() != config_digest:
                    raise SourceError("镜像配置 digest 不匹配")
                config = json.loads(config_payload)
                if config.get("os") != "linux" or config.get("architecture") != arch:
                    raise SourceError("镜像不含当前 CPU 架构")
            layers = data.get("layers") or []
            if not layers:
                raise SourceError("镜像没有可下载的层")
            # Fetch actual blob bytes; a reachable /v2/ returning 401 is not
            # evidence that a mirror contains these particular pinned images.
            self.registry_read(registry, repository, "blobs/" + layers[-1]["digest"], auth, sample=True)

        return self.rank(label, image_candidates(reference, self.policy, extra), probe)


def go_metadata(root, arch):
    match = re.search(r"^toolchain (go[0-9.]+)$", (root / "go.mod").read_text(), re.M)
    metadata = json.loads((root / "deploy/go-toolchain.json").read_text())
    if not match or metadata.get("version") != match[1]:
        raise SourceError("工具链校验清单与 go.mod 不一致，请同步 deploy/go-toolchain.json")
    matches = [item for item in metadata["files"] if item["os"] == "linux" and item["arch"] == arch]
    if len(matches) != 1 or not re.fullmatch(r"[a-f0-9]{64}", matches[0]["sha256"]):
        raise SourceError("缺少当前架构的工具链校验值")
    if matches[0]["filename"] != f"{match[1]}.linux-{arch}.tar.gz":
        raise SourceError("工具链文件名无效")
    return matches[0]


def download_verified(choices, destination, digest, log=print):
    opener = urllib.request.build_opener(SafeRedirect())
    for choice in choices:
        log(f"下载 Go 工具链：{choice['name']}")
        hasher = hashlib.sha256()
        deadline = time.monotonic() + 600
        try:
            with opener.open(choice["url"], timeout=20) as response, destination.open("wb") as stream:
                while True:
                    if time.monotonic() > deadline:
                        raise SourceError("下载超过 10 分钟")
                    block = response.read(65536)
                    if not block:
                        break
                    hasher.update(block)
                    stream.write(block)
            if hasher.hexdigest() != digest:
                raise SourceError("SHA-256 不匹配")
            return
        except (OSError, SourceError, urllib.error.URLError):
            destination.unlink(missing_ok=True)
            log(f"{choice['name']} 下载或校验失败，尝试下一个源。")
    raise SourceError("所有 Go 工具链源下载失败；未使用未校验的产物")
