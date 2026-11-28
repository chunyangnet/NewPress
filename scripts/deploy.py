#!/usr/bin/env python3
"""Linux deployment for a source checkout; Python standard library only."""
import argparse
import contextlib
import copy
import hashlib
import ipaddress
import json
import os
from pathlib import Path
import platform
import re
import secrets
import shlex
import shutil
import socket
import stat
import subprocess
import sys
import tarfile
import tempfile
import time
import unicodedata
import urllib.error
import urllib.parse
import urllib.request

import deploy_sources as sources
import deploy_prebuilt as prebuilt
from deploy_password import PasswordInputError, password_prompt
from deploy_terminal import Terminal, TerminalHelpFormatter, preview

ROOT = Path(__file__).resolve().parent.parent
NATIVE_ENV = Path("/etc/newpress/newpress.env")
NATIVE_DATA = Path("/var/lib/newpress")
NATIVE_BIN = Path("/opt/newpress/newpress")
UNIT = Path("/etc/systemd/system/newpress.service")
MANAGED = "# Managed by NewPress deploy.py"
SECRET_NAMES = ("postgres_password", "database_url", "admin_password_hash")
ui = Terminal()
DOCKER_ENDPOINT = None
IMAGE_KEYS = {name: "NEWPRESS_" + name.upper() + "_IMAGE"
              for name in ("go", "runtime", "postgres", "proxy")}


class DeployError(Exception):
    pass


def log(message, level="info"):
    ui.status(message, level)


def run(args, *, capture=False, check=True, input=None, env=None, cwd=None, timeout=None, identity=None):
    if str(args[0]) == "docker" and env is None:
        env = docker_env()
    try:
        result = subprocess.run([str(arg) for arg in args], input=input, text=True,
                                stdout=subprocess.PIPE if capture else None,
                                stderr=subprocess.PIPE if capture else None,
                                env=env, cwd=cwd, timeout=timeout, **(identity or {}))
    except subprocess.TimeoutExpired:
        result = subprocess.CompletedProcess(args, 124, "", "")
    if check and result.returncode:
        # Commands may receive secrets on stdin. Never echo stdin, environment,
        # complete command arguments, or arbitrary captured output on failure.
        raise DeployError(f"{Path(str(args[0])).name} 执行失败（退出码 {result.returncode}）。")
    return result


def read_env(path, *, compose=False):
    """Literal dotenv/systemd subset; never source or execute configuration."""
    if not path.exists():
        return {}
    values = {}
    for number, line in enumerate(path.read_text(encoding="utf-8-sig").splitlines(), 1):
        line = line.strip()
        if not line or line.startswith("#"):
            continue
        match = re.fullmatch(r"(?:export\s+)?([A-Za-z_][A-Za-z0-9_]*)\s*=\s*(.*)", line)
        if not match:
            raise DeployError(f"{path}:{number} 不是有效的 KEY=value。")
        key, raw = match.groups()
        if key in values:
            raise DeployError(f"{path}:{number} 重复设置 {key}。")
        try:
            parts = shlex.split(raw, comments=True)
        except ValueError as error:
            raise DeployError(f"{path}:{number} 引号未闭合。") from error
        value = " ".join(parts)
        if compose:
            if re.search(r"(?<!\$)\$(?!\$)", value):
                raise DeployError(f"{path}:{number} 请使用字面值，脚本不展开环境变量；字面 $ 写成 $$。")
            value = value.replace("$$", "$")
        if any(ord(char) < 32 for char in value):
            raise DeployError(f"{path}:{number} 不允许控制字符。")
        values[key] = value
    return values


def safe_path(path):
    for part in (path, *path.parents):
        if part.is_symlink():
            raise DeployError(f"拒绝通过符号链接写入：{path}")


def atomic_write(path, data, mode=0o600):
    safe_path(path)
    path.parent.mkdir(parents=True, exist_ok=True)
    descriptor, temporary = tempfile.mkstemp(prefix=".newpress-", dir=path.parent)
    try:
        with os.fdopen(descriptor, "wb") as stream:
            stream.write(data.encode("utf-8") if isinstance(data, str) else data)
            stream.flush()
            os.fsync(stream.fileno())
        os.chmod(temporary, mode)
        os.replace(temporary, path)
    finally:
        if os.path.exists(temporary):
            os.unlink(temporary)


def write_env(path, values, *, compose=False, remove=()):
    # Preserve comments and unrelated keys in an existing environment file.
    lines = path.read_text(encoding="utf-8-sig").splitlines() if path.exists() else [
        "# NewPress 部署参数；站点内容、外观、账号请在后台管理。",
        "# 首次引导项不会覆盖已保存的设置或已有管理员。",
    ]
    pending = {key: value for key, value in values.items() if key not in remove}
    lines = [line for line in lines if not (
        (match := re.match(r"\s*(?:export\s+)?([A-Za-z_][A-Za-z0-9_]*)\s*=", line))
        and match[1] in remove)]

    def encode(key, value):
        value = str(value)
        if any(ord(char) < 32 for char in value):
            raise DeployError(f"{key} 不允许控制字符。")
        if compose:
            value = value.replace("$", "$$")
        return key + "=" + json.dumps(value, ensure_ascii=False)

    for index, line in enumerate(lines):
        match = re.match(r"\s*(?:export\s+)?([A-Za-z_][A-Za-z0-9_]*)\s*=", line)
        if match and match[1] in pending:
            lines[index] = encode(match[1], pending.pop(match[1]))
    lines.extend(encode(key, value) for key, value in pending.items())
    atomic_write(path, "\n".join(lines) + "\n")


def validate_origin(value):
    try:
        parsed = urllib.parse.urlsplit(value)
        port = parsed.port
    except ValueError as error:
        raise DeployError("公开地址格式无效。") from error
    if (parsed.scheme not in ("http", "https") or not parsed.hostname or
            parsed.username is not None or parsed.password is not None or
            parsed.path not in ("", "/") or parsed.query or parsed.fragment or
            any(char.isspace() or ord(char) < 32 for char in value) or
            port == 0 or "\\" in value or "?" in value or "#" in value):
        raise DeployError("--url 必须是完整的 HTTP(S) 站点地址，例如 https://example.com；不能含路径、凭据、查询或片段。")
    if parsed.scheme == "http" and parsed.hostname.lower() != "localhost":
        try:
            local = ipaddress.ip_address(parsed.hostname).is_loopback and "%" not in parsed.hostname
        except ValueError:
            local = False
        if not local:
            raise DeployError("非回环站点地址必须使用 HTTPS；请先配置 TLS 反向代理。")
    return value.rstrip("/")


def resolve_network(values, args):
    port = args.port if args.port is not None else values.get("NEWPRESS_HTTP_PORT", "8080")
    try:
        port = int(port)
    except ValueError as error:
        raise DeployError("端口必须为整数。") from error
    if not 1 <= port <= 65535:
        raise DeployError("端口必须在 1–65535 之间。")
    bind = args.bind if args.bind is not None else values.get("NEWPRESS_HTTP_BIND", "0.0.0.0")
    try:
        # IPv4 binding also keeps Compose port syntax unambiguous.
        ipaddress.IPv4Address(bind)
    except ValueError as error:
        raise DeployError("--bind 必须为 IPv4 地址，例如 0.0.0.0 或 127.0.0.1。") from error
    origin = args.url or values.get("NEWPRESS_PUBLIC_URL")
    if not origin:
        raise DeployError("首次部署请指定 --url（实际访问地址），例如 --url https://example.com。")
    return validate_origin(origin), port, bind


def interactive(args):
    return not args.yes and (args.interactive or (sys.stdin.isatty() and sys.stdout.isatty()))


def ask(question, default="", validate=None):
    while True:
        try:
            answer = input(ui.prompt(question, default)).strip()
        except EOFError as error:
            raise DeployError("输入已结束，部署已取消；自动化请使用 --yes 并提供必需参数。") from error
        answer = answer or str(default)
        try:
            if validate:
                return validate(answer)
            if not answer:
                raise DeployError("此项不能为空。")
            return answer
        except (ValueError, DeployError, sources.SourceError) as error:
            log(str(error), "warn")


def yes_no(question, default=True):
    def validate(value):
        if value.lower() in ("y", "yes", "是"):
            return True
        if value.lower() in ("n", "no", "否"):
            return False
        raise DeployError("请输入 y 或 n。")
    return ask(question + " (y/n)", "y" if default else "n", validate)


def validate_proxy_cidrs(value):
    if not value:
        raise DeployError("请在配置文件中填写反向代理服务器的 IP/CIDR，多个用逗号分隔；参见 README.md 的部署与反向代理配置。")
    result = []
    for part in value.split(","):
        network = ipaddress.ip_network(part.strip(), strict=False)
        if network.prefixlen == 0 or network.is_multicast or network.network_address.is_unspecified:
            raise DeployError("可信代理不能使用全部地址或非单播网段。")
        result.append(str(network))
    if len(result) > 64:
        raise DeployError("最多允许 64 个可信代理网段。")
    return ",".join(dict.fromkeys(result))


def wizard_mode(args, native_exists, compose_exists, ready, systemd):
    if args.mode != "auto" or native_exists or compose_exists or not interactive(args):
        return
    default = "1" if ready or not systemd else "2"
    ui.menu("部署方式", [("1", "Docker Compose", "PostgreSQL 数据库与内置代理"),
                         ("2", "原生二进制 + systemd", "文件存储，支持使用预编译二进制")], default)
    def choose(value):
        if value not in ("1", "2"):
            raise DeployError("请选择 1 或 2。")
        if value == "2" and not systemd:
            raise DeployError("当前环境没有运行中的 systemd。")
        return "docker" if value == "1" else "native"
    args.mode = ask("使用哪种部署方式", default, choose)


def wizard_network(args, values, mode, existing):
    if not interactive(args):
        return
    if args.bind is None:
        default_bind = values.get("NEWPRESS_HTTP_BIND", "127.0.0.1" if values.get("NEWPRESS_PROXY_MODE") == "local" else "0.0.0.0")
        args.bind = ask("服务监听地址", default_bind,
                        lambda value: str(ipaddress.IPv4Address(value)))
    if args.port is None:
        def validate(value):
            port = int(value)
            minimum = 1024 if mode == "native" else 1
            if not minimum <= port <= 65535:
                raise DeployError(f"端口范围为 {minimum}–65535。")
            unchanged = existing and str(port) == values.get("NEWPRESS_HTTP_PORT", "8080") and args.bind == values.get("NEWPRESS_HTTP_BIND", "0.0.0.0")
            if not unchanged and not port_available(args.bind, port):
                raise DeployError("该端口已被占用，请换一个端口。")
            return port
        args.port = ask("应用使用哪个端口", values.get("NEWPRESS_HTTP_PORT", "8080"), validate)
    if args.url is None:
        default_url = values.get("NEWPRESS_PUBLIC_URL", "")
        if default_url and values.get("NEWPRESS_PROXY_MODE", "none") == "none":
            parsed = urllib.parse.urlsplit(validate_origin(default_url))
            previous_port = int(values.get("NEWPRESS_HTTP_PORT", "8080"))
            if parsed.scheme == "http" and (parsed.port or 80) == previous_port and args.port != previous_port:
                host = f"[{parsed.hostname}]" if ":" in parsed.hostname else parsed.hostname
                default_url = f"http://{host}:{args.port}"
        args.url = ask("站点访问地址（例如 https://example.com）", default_url, validate_origin)
    if args.source_mode is None:
        default_source = "2" if values.get("NEWPRESS_SOURCE_MODE") == "official" else "1"
        ui.menu("下载源策略", [("1", "自动选择", "检测官方与国内源，按采样耗时排序、失败切换"),
                               ("2", "仅官方源", "仅检测和使用官方提供的下载入口")], default_source)
        def choose_source(value):
            if value not in ("1", "2"):
                raise DeployError("请选择 1 或 2。")
            return "auto" if value == "1" else "official"
        args.source_mode = ask("选择下载源策略", default_source, choose_source)


def configure_proxy(values, args, mode):
    # Proxy settings come only from the operator-maintained configuration.
    proxy = values.get("NEWPRESS_PROXY_MODE", "none")
    if proxy not in ("local", "remote", "none"):
        raise DeployError("NEWPRESS_PROXY_MODE 无效。")
    if args.bind is None and "NEWPRESS_HTTP_BIND" not in values and proxy == "local":
        args.bind = "127.0.0.1"
    cidrs = ""
    if proxy == "remote":
        cidrs = validate_proxy_cidrs(values.get("NEWPRESS_EXTERNAL_PROXY_CIDRS", ""))
    values.update(NEWPRESS_PROXY_MODE=proxy, NEWPRESS_EXTERNAL_PROXY_CIDRS=cidrs)
    if mode == "native":
        values.setdefault("NEWPRESS_TRUSTED_PROXY_CIDRS", "127.0.0.1/32" if proxy == "local" else cidrs)
        if values["NEWPRESS_TRUSTED_PROXY_CIDRS"]:
            values["NEWPRESS_TRUSTED_PROXY_CIDRS"] = validate_proxy_cidrs(values["NEWPRESS_TRUSTED_PROXY_CIDRS"])


def configure_sources(values, args):
    args.source_mode = args.source_mode or values.get("NEWPRESS_SOURCE_MODE", "auto")
    if args.source_mode not in ("auto", "official"):
        raise DeployError("NEWPRESS_SOURCE_MODE 必须为 auto 或 official。")
    values["NEWPRESS_SOURCE_MODE"] = args.source_mode
    for attribute, key in (("docker_mirror", "NEWPRESS_DOCKER_MIRRORS"), ("gcr_mirror", "NEWPRESS_GCR_MIRRORS")):
        supplied = getattr(args, attribute)
        mirrors = supplied if supplied is not None else values.get(key, "").split(",")
        mirrors = list(dict.fromkeys(sources.https_base(item.strip()) for item in mirrors if item.strip()))
        if len(mirrors) > 8:
            raise DeployError("每类最多配置 8 个自定义容器镜像源。")
        setattr(args, attribute, mirrors)
        values[key] = ",".join(mirrors)


def write_proxy_config(values):
    custom = values.get("NEWPRESS_NGINX_CONFIG")
    if custom and custom not in ("./deploy/nginx.conf", "./deploy/nginx.generated.conf"):
        path = Path(custom) if Path(custom).is_absolute() else ROOT / custom
        safe_path(path)
        if not path.is_file():
            raise DeployError("NEWPRESS_NGINX_CONFIG 指定的手动配置文件不存在。")
        return
    proxy = values.get("NEWPRESS_PROXY_MODE", "none")
    if proxy == "none":
        values["NEWPRESS_NGINX_CONFIG"] = "./deploy/nginx.conf"
        return
    template = (ROOT / "deploy/nginx.conf").read_text(encoding="utf-8")
    if proxy == "local":
        network = ipaddress.ip_network(values["NEWPRESS_EDGE_SUBNET"])
        cidrs = [str(network.network_address + 1) + "/32", "127.0.0.1/32"]
    else:
        cidrs = validate_proxy_cidrs(values["NEWPRESS_EXTERNAL_PROXY_CIDRS"]).split(",")
    directives = "".join("    set_real_ip_from " + cidr + ";\n" for cidr in cidrs)
    directives += "    real_ip_header X-Forwarded-For;\n    real_ip_recursive on;\n"
    if template.count("    server_name _;") != 1:
        raise DeployError("NGINX 模板缺少唯一的 server_name，无法写入可信代理设置。")
    content = template.replace("    server_name _;", "    server_name _;\n" + directives)
    path = ROOT / "deploy/nginx.generated.conf"
    atomic_write(path, content, 0o644)
    values["NEWPRESS_NGINX_CONFIG"] = "./deploy/nginx.generated.conf"


def port_available(bind, port):
    try:
        with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as listener:
            listener.bind((bind, port))
        return True
    except OSError:
        return False


def docker_env(*, pinned=True):
    # One explicit env file is the deployment source of truth. Shell variables
    # must not silently override saved addresses, image names or secret paths.
    environment = {key: value for key, value in os.environ.items()
                   if not key.startswith(("NEWPRESS_", "COMPOSE_"))
                   and key not in ("DOCKER_DEFAULT_PLATFORM", "BUILDX_BUILDER", "BUILDKIT_HOST")}
    if pinned and DOCKER_ENDPOINT:
        for key in ("DOCKER_CONTEXT", "DOCKER_TLS", "DOCKER_TLS_VERIFY", "DOCKER_CERT_PATH"):
            environment.pop(key, None)
        environment["DOCKER_HOST"] = DOCKER_ENDPOINT
        environment["BUILDX_BUILDER"] = "default"
    return environment


def docker_ready():
    if not shutil.which("docker"):
        return False
    inspect_local_docker()
    return run(["docker", "info"], capture=True, check=False, timeout=15).returncode == 0


def docker_existing(project):
    if not docker_ready():
        return False
    for resource in ("container", "volume"):
        result = run(["docker", resource, "ls", "-aq" if resource == "container" else "-q",
                      "--filter", f"label=com.docker.compose.project={project}"], capture=True)
        if result.stdout.strip():
            return True
    # Restored/pre-created named volumes may not have Compose labels yet.
    names = set(run(["docker", "volume", "ls", "-q"], capture=True).stdout.split())
    expected = {project + "_" + name for name in (
        "postgres_data", "newpress_data", "newpress_themes", "newpress_plugins", "newpress_updates")}
    return bool(names & expected)


def select_mode(requested, native_exists, compose_exists, ready, systemd):
    if native_exists and compose_exists:
        raise DeployError("同时发现原生服务与 Compose 数据，请先明确并整理部署，脚本不会切换存储。")
    existing = "native" if native_exists else "docker" if compose_exists else None
    if existing and requested not in ("auto", existing):
        raise DeployError(f"发现已有 {existing} 部署；切换模式需要单独迁移数据。")
    if existing:
        return existing
    if requested != "auto":
        return requested
    if ready:
        return "docker"
    if systemd:
        return "native"
    raise DeployError("未检测到可用 Docker 或 systemd；请安装其一，或指定 --mode docker --install-docker。")


def compose(values, *arguments, capture=False, check=True, input=None):
    files = compose_paths(values.get("COMPOSE_FILE", "compose.yaml").split(
        values.get("COMPOSE_PATH_SEPARATOR", os.pathsep)))
    options = [item for path in files for item in ("-f", path)]
    return run(["docker", "compose", "--project-name", values.get("COMPOSE_PROJECT_NAME", "newpress"),
                "--project-directory", ROOT, "--env-file", ROOT / ".env",
                *options, *arguments],
               capture=capture, check=check, input=input, env=docker_env())


def inspect_local_docker():
    global DOCKER_ENDPOINT
    if DOCKER_ENDPOINT:
        return DOCKER_ENDPOINT
    # Docker's context override takes precedence over DOCKER_HOST. Resolve it
    # before contacting a daemon, then pin every child command to that socket.
    context = os.environ.get("DOCKER_CONTEXT")
    endpoint = os.environ.get("DOCKER_HOST")
    if context or not endpoint:
        command = ["docker", "context", "inspect"]
        if context:
            command.append(context)
        result = run([*command, "--format", "{{.Endpoints.docker.Host}}"], capture=True,
                     env=docker_env(pinned=False), timeout=15)
        endpoint = result.stdout.strip()
    parsed = urllib.parse.urlsplit(endpoint)
    if (parsed.scheme != "unix" or parsed.netloc or not parsed.path.startswith("/") or
            parsed.query or parsed.fragment or any(ord(char) < 32 for char in endpoint)):
        raise DeployError("部署脚本要求本机 Docker（unix socket），不操作远程 Docker daemon。")
    DOCKER_ENDPOINT = endpoint
    return endpoint


def compose_paths(names):
    paths = []
    for name in names:
        if not name or any(char in str(name) for char in (";", ",", "\n", "\r")):
            raise DeployError("Compose 文件列表含空路径或不支持的分隔符。")
        path = Path(name)
        if not path.is_absolute():
            path = ROOT / path
        path = Path(os.path.abspath(path))
        if not path.is_file():
            raise DeployError(f"Compose 文件不存在：{path}；请恢复文件或用 --compose-file 明确选择。")
        if path in paths:
            raise DeployError(f"Compose 文件重复：{path}")
        paths.append(path)
    base = Path(os.path.abspath(ROOT / "compose.yaml"))
    if not paths or paths[0] != base:
        raise DeployError("Compose 文件列表必须以当前项目的 compose.yaml 开头。")
    return paths


def select_compose_files(values, supplied=None, *, discover=False):
    if supplied is not None:
        # Supplying the base alone intentionally clears all overlays.
        base = Path(os.path.abspath(ROOT / "compose.yaml"))
        names = list(supplied)
        first = Path(names[0]) if names else base
        if not first.is_absolute():
            first = ROOT / first
        if Path(os.path.abspath(first)) != base:
            names.insert(0, str(base))
        selected = compose_paths(names)
    else:
        candidates = []
        if values.get("COMPOSE_FILE"):
            separator = values.get("COMPOSE_PATH_SEPARATOR", os.pathsep)
            if len(separator) != 1:
                raise DeployError("COMPOSE_PATH_SEPARATOR 必须为单个字符。")
            candidates.append(compose_paths(values["COMPOSE_FILE"].split(separator)))
        if discover:
            project = values.get("COMPOSE_PROJECT_NAME", "newpress")
            ids = run(["docker", "container", "ls", "-aq", "--filter",
                       f"label=com.docker.compose.project={project}"], capture=True).stdout.split()
            if ids:
                containers = json.loads(run(["docker", "container", "inspect", *ids], capture=True).stdout)
                for container in containers:
                    labels = container.get("Config", {}).get("Labels") or {}
                    names = labels.get("com.docker.compose.project.config_files")
                    if not names:
                        raise DeployError("已有容器缺少 Compose 文件记录；请用 --compose-file 明确指定完整配置。")
                    candidates.append(compose_paths(names.split(",")))
        if not candidates:
            candidates = [compose_paths(["compose.yaml"])]
        # A container that did not need recreating may retain a shorter list.
        # Require one ordered superset, never guess between divergent overlays.
        selected = max(candidates, key=len)
        for candidate in candidates:
            positions = [selected.index(path) for path in candidate if path in selected]
            if len(positions) != len(candidate) or positions != sorted(positions):
                raise DeployError("已有 Compose 文件记录不一致；请用 --compose-file 按顺序明确选择附加文件。")
    values["COMPOSE_PATH_SEPARATOR"] = ";"
    values["COMPOSE_FILE"] = ";".join(str(path) for path in selected)
    return selected


def install_buildx():
    if shutil.which("apt-get"):
        commands = [["apt-get", "update"], ["apt-get", "install", "-y", "docker-buildx-plugin"]]
    elif shutil.which("dnf") or shutil.which("yum"):
        commands = [[shutil.which("dnf") or shutil.which("yum"), "install", "-y", "docker-buildx-plugin"]]
    else:
        raise DeployError("无法自动安装 Buildx；请通过当前系统的软件包管理器安装 Docker Buildx 插件。")
    prefix = []
    if os.geteuid() != 0:
        if not shutil.which("sudo"):
            raise DeployError("安装 Buildx 需要 root，请使用 root 重新运行部署脚本。")
        prefix = ["sudo"]
    for command in commands:
        if run(prefix + command, check=False).returncode:
            raise DeployError("Buildx 安装失败，请查看上方软件包管理器输出；若找不到 docker-buildx-plugin，请先配置与现有 Docker 对应的软件源。")
    if run(["docker", "buildx", "version"], capture=True, check=False).returncode:
        raise DeployError("Buildx 安装后仍不可用，请检查 docker buildx version 的输出。")
    log("Docker Buildx 插件已安装并验证。", "ok")


def check_docker_plugins(args=None):
    result = run(["docker", "compose", "version", "--short"], capture=True, check=False)
    version = re.search(r"(\d+)\.(\d+)\.(\d+)", result.stdout)
    if result.returncode or not version or tuple(map(int, version.groups())) < (2, 20, 0):
        raise DeployError("需要 Docker Compose 插件 2.20+（支持 up --wait）。")
    if (ROOT / "deployment.json").is_file():
        return
    if run(["docker", "buildx", "version"], capture=True, check=False).returncode:
        if args is not None and not args.check and interactive(args):
            ui.line("Buildx 用于构建应用镜像，插件本身没有常驻进程；安装大小以软件包管理器输出为准。", "muted")
            ui.menu("缺少或无法运行 Docker Buildx，是否安装？", [
                ("0", "不安装", "结束本次部署"),
                ("1", "安装", "通过系统软件包管理器安装 docker-buildx-plugin")], "0")
            def choose(value):
                if value not in ("0", "1"):
                    raise DeployError("请输入 0 或 1。")
                return value
            if ask("是否安装 Buildx（0 不安装 / 1 安装）", "0", choose) == "1":
                install_buildx()
                return
        raise DeployError("缺少 Docker Buildx 插件，请安装 docker-buildx-plugin。")


def choose_subnet(values):
    if "NEWPRESS_EDGE_SUBNET" in values:
        subnet = ipaddress.IPv4Network(values["NEWPRESS_EDGE_SUBNET"])
        proxy = ipaddress.IPv4Address(values.get("NEWPRESS_PROXY_IPV4", str(subnet.network_address + 2)))
        if proxy not in subnet or proxy in (subnet.network_address, subnet.broadcast_address):
            raise DeployError("NEWPRESS_PROXY_IPV4 必须是 edge 子网中的有效地址。")
        values.setdefault("NEWPRESS_PROXY_IPV4", str(proxy))
        values.setdefault("NEWPRESS_TRUSTED_PROXY_CIDRS", str(proxy) + "/32")
        if subnet.prefixlen > 29:
            raise DeployError("edge 子网至少需要 /29，以分离固定代理地址和动态容器地址。")
        dynamic = next(block for block in subnet.subnets(prefixlen_diff=1) if proxy not in block)
        if "NEWPRESS_EDGE_DYNAMIC_RANGE" in values:
            dynamic = ipaddress.IPv4Network(values["NEWPRESS_EDGE_DYNAMIC_RANGE"])
            if not dynamic.subnet_of(subnet) or proxy in dynamic:
                raise DeployError("edge 动态地址范围必须位于子网内且不包含代理固定地址。")
        values["NEWPRESS_EDGE_DYNAMIC_RANGE"] = str(dynamic)
        return
    occupied = []
    networks = run(["docker", "network", "ls", "-q"], capture=True).stdout.split()
    if networks:
        for network in json.loads(run(["docker", "network", "inspect", *networks], capture=True).stdout):
            for config in network.get("IPAM", {}).get("Config") or []:
                if config.get("Subnet"):
                    subnet = ipaddress.ip_network(config["Subnet"], strict=False)
                    labels = network.get("Labels") or {}
                    if (subnet.version == 4 and
                            labels.get("com.docker.compose.project") == values.get("COMPOSE_PROJECT_NAME", "newpress") and
                            labels.get("com.docker.compose.network") == "edge"):
                        values["NEWPRESS_EDGE_SUBNET"] = str(subnet)
                        choose_subnet(values)
                        return
                    occupied.append(subnet)
    route_file = Path("/proc/net/route")
    if route_file.exists():
        for row in route_file.read_text().splitlines()[1:]:
            fields = row.split()
            if len(fields) > 7 and fields[1] != "00000000":
                address = socket.inet_ntoa(bytes.fromhex(fields[1])[::-1])
                mask = socket.inet_ntoa(bytes.fromhex(fields[7])[::-1])
                occupied.append(ipaddress.ip_network(f"{address}/{mask}", strict=False))
    for third in range(30, 250):
        candidate = ipaddress.ip_network(f"172.30.{third}.0/24")
        if not any(candidate.version == item.version and candidate.overlaps(item) for item in occupied):
            proxy = str(candidate.network_address + 2)
            values.update(NEWPRESS_EDGE_SUBNET=str(candidate), NEWPRESS_PROXY_IPV4=proxy,
                          NEWPRESS_TRUSTED_PROXY_CIDRS=proxy + "/32")
            choose_subnet(values)
            return
    raise DeployError("未找到不冲突的 edge 子网，请在 .env 配置 NEWPRESS_EDGE_SUBNET / NEWPRESS_PROXY_IPV4 / NEWPRESS_TRUSTED_PROXY_CIDRS。")


def secret_state(directory, names, existing):
    safe_path(directory)
    present = []
    for name in names:
        path = directory / name
        safe_path(path)
        if path.exists() and (not path.is_file() or not path.read_bytes().strip()):
            raise DeployError(f"密钥文件无效：{path}")
        present.append(path.exists())
    if (any(present) and not all(present)) or (existing and not all(present)):
        raise DeployError(f"已有数据或密钥不完整，请恢复 {directory} 中的原始密钥；不会生成替代数据库密码。")
    return all(present)


def docker_secret_directory(values):
    directory = Path(values.get("NEWPRESS_SECRETS_DIR", "./.secrets"))
    if not directory.is_absolute():
        directory = ROOT / directory
    safe_path(directory)
    directory = Path(os.path.abspath(directory))
    try:
        relative = directory.relative_to(ROOT)
    except ValueError:
        return directory
    if not relative.parts or relative.parts[0] != ".secrets":
        raise DeployError("源码目录内的密钥请放在 .secrets/，或使用源码目录外的绝对路径，避免进入 Docker 构建上下文。")
    return directory


def validate_admin_username(value):
    value = value.strip().lower()
    if not 3 <= len(value) <= 80 or any(not (char.isalpha() or unicodedata.category(char) == "Nd" or char in "._-") for char in value):
        raise DeployError("管理员用户名需为 3–80 个字母、数字、点、下划线或连字符。")
    return value


def validate_password(value):
    if len(value) < 12 or len(value.encode("utf-8")) > 1024 or any(unicodedata.category(char) == "Cc" for char in value):
        raise DeployError("密码至少 12 个字符，最多 1024 个 UTF-8 字节，不能包含控制字符。")
    return value


def read_password(args, *, allow_generate=False):
    if getattr(args, "_stdin_password", None) is not None:
        return args._stdin_password, False
    if args.password_stdin:
        if sys.stdin.isatty():
            raise DeployError("--password-stdin 仅接受管道或文件重定向；交互设置请去掉该参数。")
        raw = sys.stdin.buffer.read(1027)
        if len(raw) > 1026:
            raise DeployError("密码输入过长。")
        try:
            password = raw.decode("utf-8").removesuffix("\n").removesuffix("\r")
        except UnicodeError as error:
            raise DeployError("密码输入必须为 UTF-8。") from error
        return validate_password(password), False
    if not sys.stdin.isatty():
        raise DeployError("安全输入密码需要交互终端；自动化请显式使用 --password-stdin。")
    ui.line("密码内容隐藏，实时显示已输入位数；退格删除，Ctrl+U 清空。", "muted")
    while True:
        try:
            password = password_prompt("  新密码" + ("（留空自动生成）" if allow_generate else "") + "：")
            if not password and allow_generate:
                return secrets.token_urlsafe(24), True
            validate_password(password)
            confirmation = password_prompt("  再次输入密码：")
            if password != confirmation:
                raise DeployError("两次输入的密码不一致，请重试。")
            return password, False
        except PasswordInputError as error:
            raise DeployError(str(error)) from error
        except EOFError as error:
            raise DeployError("密码输入已取消。") from error
        except DeployError as error:
            log(str(error), "warn")


def initial_credentials(values, hash_password, args=None):
    username = validate_admin_username((args.admin_username if args else None) or values.get("NEWPRESS_ADMIN_USERNAME", "admin"))
    if args and args.admin_setup and not args.password_stdin:
        if not sys.stdin.isatty():
            raise DeployError("首次管理员设置需要交互终端。")
        username = ask("管理员登录名", username, validate_admin_username)
    values["NEWPRESS_ADMIN_USERNAME"] = username
    if args and (args.password_stdin or args.admin_setup):
        password, generated = read_password(args, allow_generate=True)
    else:
        password, generated = secrets.token_urlsafe(24), True
    hashed = hash_password(password).strip()
    if not hashed.startswith("$argon2id$") or "\n" in hashed:
        raise DeployError("管理员密码哈希生成失败。")
    login = ((f"地址：{values['NEWPRESS_PUBLIC_URL']}/admin/\n"
              f"账号：{username}\n密码：{password}\n"
              "仅供首次登录。请在后台修改密码，随后删除此文件。\n") if generated else "")
    log(f"管理员登录名：{username}" + ("；随机密码保存到受限的首次登录文件。" if generated else "；已采用自设密码，不保存明文副本。"), "ok")
    return hashed, login


def wait_http(url, timeout):
    deadline = time.monotonic() + timeout
    # Local probes must not go through HTTP_PROXY or HTTPS_PROXY.
    class NoRedirect(urllib.request.HTTPRedirectHandler):
        def redirect_request(self, request, response, code, message, headers, target):
            return None

    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), NoRedirect())
    while time.monotonic() < deadline:
        try:
            with opener.open(url, timeout=4) as response:
                if response.status == 200 and response.headers.get("X-NewPress-Instance"):
                    return
        except (OSError, urllib.error.URLError):
            pass
        time.sleep(2)
    raise DeployError(f"健康检查在 {timeout} 秒内未通过。数据和密钥已保留，请查看服务日志后重试。")


def matching_go():
    match = re.search(r"^toolchain (go[0-9.]+)$", (ROOT / "go.mod").read_text(), re.M)
    go = shutil.which("go")
    if match and go:
        result = run([go, "version"], capture=True, check=False, timeout=10,
                     env=dict(os.environ, GOTOOLCHAIN="local"))
        if result.returncode == 0 and match[1] in result.stdout.split():
            return go
    return None


def cached_image(reference, arch):
    result = run(["docker", "image", "inspect", reference], capture=True, check=False)
    if result.returncode:
        return False
    try:
        info = json.loads(result.stdout)[0]
        digest = sources.image_parts(reference)[2]
        return (info["Os"] == "linux" and info["Architecture"] == arch and
                any(item.endswith("@" + digest) for item in info.get("RepoDigests", [])))
    except (ValueError, KeyError, IndexError, TypeError):
        return False


def prepare_sources(args, distro, mode, ready, arch, values=None):
    selector = sources.Selector(args.source_mode, args.source_timeout, log, reporter=ui)
    plan = {}
    if mode == "docker" and not ready:
        plan["os_apt"] = selector.os_apt(distro, arch)
        plan["docker_apt"] = selector.docker_apt(distro, arch)
    try:
        bundle = prebuilt.load(ROOT, arch)
    except (OSError, ValueError, KeyError, TypeError) as error:
        raise DeployError(str(error)) from error
    if bundle:
        plan["prebuilt"] = bundle
        return plan
    if mode == "docker" or not args.binary:
        plan["goproxy"] = selector.go_modules(ROOT)
    if mode == "native" and not args.binary and not matching_go():
        plan["go_metadata"] = sources.go_metadata(ROOT, arch)
        plan["go_archive"] = selector.go_archive(plan["go_metadata"])
    if mode == "docker":
        plan["images"] = {}
        plan["image_origins"] = {}
        values = values or {}
        for name, default in sources.pinned_images(ROOT).items():
            key = IMAGE_KEYS[name]
            reference = values.get(key) or default
            if values.get(key + "_SELECTED") == reference and values.get(key + "_ORIGIN"):
                reference = values[key + "_ORIGIN"]
            sources.image_parts(reference)
            plan["image_origins"][name] = reference
            extra = args.gcr_mirror if name == "runtime" else args.docker_mirror
            cached = [item for item in sources.image_candidates(reference, args.source_mode, extra)
                      if ready and cached_image(item[1], arch)]
            if cached:
                log(f"容器镜像 {name}：使用已校验 digest 的本地缓存。")
                plan["images"][name] = [{"name": "本地缓存", "url": cached[0][1], "cached": True}]
            else:
                plan["images"][name] = selector.image("容器镜像 " + name, reference, arch, extra)
    return plan


def pull_images(plan, arch, timeout):
    selected = {}
    for name, choices in plan["images"].items():
        for choice in choices:
            log(f"准备镜像 {name}：{choice['name']}")
            if choice.get("cached") or run(["docker", "pull", "--platform", "linux/" + arch, choice["url"]],
                                            check=False, timeout=timeout, env=docker_env()).returncode == 0:
                selected[name] = choice["url"]
                break
            log(f"{choice['name']} 拉取失败或超时，切换下一个可用源。", "warn")
        else:
            raise DeployError(f"镜像 {name} 的所有源拉取失败；保留原 digest，不替换为其他版本。")
    return selected


def install_docker(distro, plan):
    if distro.get("ID") not in ("ubuntu", "debian"):
        raise DeployError("自动安装 Docker 支持 Debian/Ubuntu；其他发行版请先安装 Docker Engine 与 Compose 插件。")
    if os.geteuid() != 0:
        raise DeployError("自动安装 Docker 需要 root：请使用 sudo bash scripts/deploy.sh --mode docker --install-docker ...")
    if shutil.which("docker"):
        raise DeployError("已安装 Docker，不会覆盖现有安装；请安装对应的 Compose 插件或启动 daemon 后重试。")
    codename = distro.get("VERSION_CODENAME", "")
    if not re.fullmatch(r"[a-z]+", codename):
        raise DeployError("无法识别 Debian/Ubuntu 发行版代号。")
    key = Path("/etc/apt/keyrings/newpress-docker.asc")
    key_data = (ROOT / "deploy/docker-release-key.asc").read_bytes()
    if hashlib.sha256(key_data).hexdigest() != "1500c1f56fa9e26b9b8f42452a553675796ade0807cdce11975eb98170b3a570":
        raise DeployError("随项目保存的 Docker 官方签名公钥校验失败。")
    architecture = run(["dpkg", "--print-architecture"], capture=True).stdout.strip()
    archive_key = Path(f"/usr/share/keyrings/{distro['ID']}-archive-keyring.gpg")
    if not archive_key.is_file():
        raise DeployError("系统缺少发行版 archive-keyring，无法验证系统依赖的软件包签名。")
    atomic_write(key, key_data, 0o644)
    installed = False
    # Temporary source selection leaves the host's other repositories intact.
    # apt continues checking upstream signatures against the original keyrings.
    with tempfile.TemporaryDirectory(prefix="newpress-apt-") as temporary:
        directory = Path(temporary)
        directory.chmod(0o755)
        source_file = directory / "install.sources"
        combinations = sorted(((system, docker) for system in plan["os_apt"] for docker in plan["docker_apt"]),
                              key=lambda pair: pair[0]["seconds"] + pair[1]["seconds"])
        for system, docker in combinations:
            docker_config = (f"Types: deb\nURIs: {docker['url']}\nSuites: {codename}\nComponents: stable\n"
                             f"Architectures: {architecture}\nSigned-By: {key}\n")
            system_config = sources.apt_stanzas(distro, system['url'], architecture, str(archive_key))
            atomic_write(source_file, system_config + "\n" + docker_config, 0o644)
            options = ["-o", f"Dir::Etc::sourcelist={source_file}", "-o", "Dir::Etc::sourceparts=-",
                       "-o", "Acquire::Retries=1", "-o", "Acquire::http::Timeout=10", "-o", "Acquire::https::Timeout=10",
                       "-o", "APT::Update::Error-Mode=any"]
            log(f"安装 Docker：系统依赖使用 {system['name']}，Docker 使用 {docker['name']}。")
            if run(["apt-get", *options, "update"], check=False).returncode:
                continue
            if run(["apt-get", *options, "install", "-y", "ca-certificates", "docker-ce", "docker-ce-cli", "containerd.io",
                    "docker-buildx-plugin", "docker-compose-plugin"], check=False).returncode:
                continue
            atomic_write(Path("/etc/apt/sources.list.d/newpress-docker.sources"), docker_config, 0o644)
            installed = True
            break
    if not installed:
        raise DeployError("Docker 安装在所有已测源上失败，请查看上方 apt 错误。")
    run(["systemctl", "enable", "--now", "docker"])


def deploy_docker(values, args, existing, port, bind, arch):
    inspect_local_docker()
    check_docker_plugins()
    select_compose_files(values, args.compose_file, discover=existing)
    if not existing and not port_available(bind, port):
        raise DeployError(f"端口 {bind}:{port} 已被占用，请使用 --port 指定空闲端口。")
    choose_subnet(values)
    directory = docker_secret_directory(values)
    initialized = secret_state(directory, SECRET_NAMES, existing)
    values.setdefault("NEWPRESS_IMAGE", "newpress:local")
    image = values["NEWPRESS_IMAGE"]
    if not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9._:/@-]*", image):
        raise DeployError("NEWPRESS_IMAGE 格式无效。")
    bundle = args.source_plan.get("prebuilt")
    if bundle:
        try:
            with ui.activity("下载、校验并导入预构建容器镜像"):
                images = prebuilt.install_images(ROOT, bundle, arch, run, log)
        except (OSError, ValueError, KeyError, TypeError) as error:
            raise DeployError(str(error)) from error
        image = images["app"]
        values.update(NEWPRESS_IMAGE=image, NEWPRESS_POSTGRES_PATCHED_IMAGE=images["postgres"],
                      NEWPRESS_PROXY_PATCHED_IMAGE=images["proxy"], NEWPRESS_REDIS_PATCHED_IMAGE=images["redis"])
    else:
        images = pull_images(args.source_plan, arch, args.pull_timeout)
        values.update(NEWPRESS_GO_IMAGE=images["go"], NEWPRESS_RUNTIME_IMAGE=images["runtime"],
                      NEWPRESS_POSTGRES_IMAGE=images["postgres"], NEWPRESS_PROXY_IMAGE=images["proxy"],
                      NEWPRESS_GOPROXY="|".join(item["url"] for item in args.source_plan["goproxy"]))
    for name, reference in args.source_plan.get("image_origins", {}).items():
        values[IMAGE_KEYS[name] + "_ORIGIN"] = reference
        values[IMAGE_KEYS[name] + "_SELECTED"] = images[name]
    write_proxy_config(values)
    write_env(ROOT / ".env", values, compose=True)
    if not bundle:
        build = ["docker", "build", "--platform", "linux/" + arch, "--tag", image]
        build.extend(["--build-arg", "NEWPRESS_GO_IMAGE=" + images["go"], "--build-arg", "NEWPRESS_RUNTIME_IMAGE=" + images["runtime"],
                      "--build-arg", "GOPROXY=" + values["NEWPRESS_GOPROXY"], "--build-arg", "GOSUMDB=sum.golang.org"])
        for key, fallback in (("VERSION", "1.0.0-beta.2"), ("COMMIT", "local"), ("BUILD_DATE", "unknown")):
            build.extend(["--build-arg", f"{key}={values.get('NEWPRESS_' + key, fallback)}"])
        with ui.activity("构建应用镜像"):
            run([*build, ROOT], env=docker_env())
    if not initialized:
        hashed, login = initial_credentials(values, lambda password: run(
            ["docker", "run", "--rm", "-i", "--network", "none", "--read-only", "--cap-drop", "ALL",
             "--security-opt", "no-new-privileges:true", image, "admin", "hash-password"],
            input=password + "\n", capture=True).stdout, args)
        database_password = secrets.token_hex(32)
        directory.mkdir(parents=True, exist_ok=True)
        directory.chmod(0o700)
        for name, value in zip(SECRET_NAMES, (database_password,
                f"postgres://newpress:{database_password}@db:5432/newpress?sslmode=disable", hashed)):
            # Compose file secrets are bind mounts; UID remapping is unavailable.
            # 0700 parent protects host access; 0444 permits nonroot containers.
            atomic_write(directory / name, value + "\n", 0o444)
        if login:
            atomic_write(directory / "first-login.txt", login)
        write_env(ROOT / ".env", values, compose=True)
    else:
        directory.chmod(0o700)
        for name in SECRET_NAMES:
            (directory / name).chmod(0o444)
    compose(values, "config", "--quiet")
    if not bundle:
        with ui.activity("构建数据库与代理系统安全补丁镜像"):
            services = compose(values, "config", "--services", capture=True).stdout.split()
            patched_services = [name for name in ("db", "proxy", "redis") if name in services]
            compose(values, "build", "--pull", "--no-cache", *patched_services)
    with ui.activity("启动数据库、迁移、应用与代理"):
        compose(values, "up", "-d", "--no-build", "--wait", "--wait-timeout", str(args.timeout))
    host = "127.0.0.1" if bind == "0.0.0.0" else bind
    with ui.activity(f"本机健康检查（最多等待 {args.timeout} 秒）"):
        # Compose --wait checks deep readiness inside the app container. The
        # published proxy only needs a lightweight reachability check here.
        wait_http(f"http://{host}:{port}/health/live", args.timeout)
    return directory / "first-login.txt"


def extract_toolchain(archive_path, destination):
    # Go toolchains contain directories and regular files. Reject links and
    # traversal instead of relying on Python-version-specific tar defaults.
    with tarfile.open(archive_path, "r:gz") as archive:
        for member in archive.getmembers():
            parts = Path(member.name).parts
            if (not parts or parts[0] != "go" or ".." in parts or member.name.startswith("/") or
                    "\\" in member.name or not (member.isdir() or member.isfile())):
                raise DeployError("Go 工具链压缩包包含不安全路径或文件类型。")
        archive.extractall(destination)


def build_binary(destination, arch, plan):
    match = re.search(r"^toolchain (go[0-9.]+)$", (ROOT / "go.mod").read_text(), re.M)
    if not match:
        raise DeployError("go.mod 缺少固定工具链版本。")
    toolchain = match[1]
    go = matching_go()
    if not go:
        log(f"下载并校验 {toolchain} linux/{arch} 工具链（仅在临时目录使用）…")
        metadata = plan["go_metadata"]
        filename = metadata["filename"]
        if filename != f"{toolchain}.linux-{arch}.tar.gz":
            raise DeployError("工具链文件名不符合预期。")
        archive = destination.parent / filename
        sources.download_verified(plan["go_archive"], archive, metadata["sha256"], log)
        extract_toolchain(archive, destination.parent)
        go = destination.parent / "go/bin/go"
    environment = dict(os.environ, CGO_ENABLED="0", GOOS="linux", GOARCH=arch, GOTOOLCHAIN="local")
    environment.update(GOPROXY="|".join(item["url"] for item in plan["goproxy"]), GOSUMDB="sum.golang.org",
                       GONOPROXY="none", GONOSUMDB="none", GOPRIVATE="", GOFLAGS="")
    # An isolated cache keeps a sudo deployment from changing a user's Go cache.
    environment.update(GOCACHE=str(destination.parent / "build-cache"),
                       GOPATH=str(destination.parent / "go-cache"))
    stamp = time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())
    flags = "-s -w -X newpress.local/newpress/internal/version.Version=1.0.0-beta.2 -X newpress.local/newpress/internal/version.BuildDate=" + stamp
    run([go, "build", "-trimpath", "-buildvcs=false", "-ldflags=" + flags, "-o", destination, "./cmd/newpress"],
        cwd=ROOT, env=environment)


def validate_binary(path, arch):
    with path.open("rb") as stream:
        header = stream.read(20)
    machine = 62 if arch == "amd64" else 183
    if (len(header) < 20 or header[:6] != b"\x7fELF\x02\x01" or
            int.from_bytes(header[18:20], "little") != machine):
        raise DeployError(f"--binary 需要 linux/{arch} 的 64 位 ELF 二进制。")


def native_values(values, port, bind):
    if port < 1024:
        raise DeployError("原生服务以普通用户运行，端口需 ≥1024；80/443 请通过 NGINX/Caddy 转发。")
    result = {key: value for key, value in values.items() if key.startswith("NEWPRESS_")}
    fixed = {"NEWPRESS_DATA_DIR": str(NATIVE_DATA / "data"),
             "NEWPRESS_THEMES_DIR": str(NATIVE_DATA / "themes"),
             "NEWPRESS_PLUGINS_DIR": str(NATIVE_DATA / "plugins"),
             "NEWPRESS_UPDATE_DIR": str(NATIVE_DATA / "updates"),
             "NEWPRESS_ADMIN_PASSWORD_HASH_FILE": str(NATIVE_ENV.parent / "secrets/admin_password_hash")}
    for key, value in fixed.items():
        if key in result and result[key] != value:
            raise DeployError(f"自动部署使用固定路径 {key}={value}；自定义目录请手动管理 systemd。")
    if result.get("NEWPRESS_DATABASE_URL") or result.get("NEWPRESS_ADMIN_PASSWORD_HASH"):
        raise DeployError("原生部署请使用 *_FILE 密钥文件，不在 .env 保存数据库密码或管理员哈希。")
    # An explicit empty direct value masks the generated *_FILE secret.
    result.pop("NEWPRESS_ADMIN_PASSWORD_HASH", None)
    result.update(fixed)
    result.update(NEWPRESS_ADDR=f"{bind}:{port}", NEWPRESS_CONTAINER="false", NEWPRESS_AUTO_MIGRATE="true")
    result.setdefault("NEWPRESS_TRUSTED_PROXY_CIDRS", "")
    return result


def native_existing():
    if UNIT.exists() or NATIVE_ENV.exists():
        return True
    data = NATIVE_DATA / "data"
    safe_path(data)
    # A failed first install may have created only an empty data directory.
    return data.exists() and (not data.is_dir() or any(data.iterdir()))


def copy_extension_tree(source, destination):
    """Copy only directories and regular files; never dereference source links."""
    if os.name != "posix":
        # Deployment runs on Linux. This path allows the portable fixture tests
        # to exercise staging, with links preserved and rejected before use.
        def copy_file(src, dst):
            if not stat.S_ISREG(os.lstat(src).st_mode):
                raise DeployError(f"扩展目录含不支持的文件类型：{src}")
            return shutil.copyfile(src, dst, follow_symlinks=False)
        if source.is_symlink():
            raise DeployError(f"扩展目录不允许符号链接：{source}")
        shutil.copytree(source, destination, symlinks=True, copy_function=copy_file)
        for path in destination.rglob("*"):
            if path.is_symlink():
                raise DeployError(f"扩展目录不允许符号链接：{path}")
        return

    def copy_directory(src_fd, dst_fd):
        for name in os.listdir(src_fd):
            info = os.stat(name, dir_fd=src_fd, follow_symlinks=False)
            if stat.S_ISDIR(info.st_mode):
                child = os.open(name, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW, dir_fd=src_fd)
                try:
                    os.mkdir(name, mode=0o700, dir_fd=dst_fd)
                    target = os.open(name, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW, dir_fd=dst_fd)
                    try:
                        copy_directory(child, target)
                    finally:
                        os.close(target)
                finally:
                    os.close(child)
            elif stat.S_ISREG(info.st_mode):
                # O_NONBLOCK prevents a raced replacement with a FIFO from
                # blocking; fstat validates the object we actually opened.
                fd = os.open(name, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK, dir_fd=src_fd)
                with os.fdopen(fd, "rb") as stream:
                    if not stat.S_ISREG(os.fstat(stream.fileno()).st_mode):
                        raise DeployError(f"扩展目录含不支持的文件类型：{name}")
                    target = os.open(name, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW,
                                     0o600, dir_fd=dst_fd)
                    with os.fdopen(target, "wb") as output:
                        shutil.copyfileobj(stream, output)
            else:
                raise DeployError(f"扩展目录不允许符号链接或特殊文件：{name}")

    src_fd = os.open(source, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
    try:
        destination.mkdir(mode=0o700)
        dst_fd = os.open(destination, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
        try:
            copy_directory(src_fd, dst_fd)
        finally:
            os.close(dst_fd)
    finally:
        os.close(src_fd)


def install_extension(source, target, account, *, entries=None):
    safe_path(target)
    if target.exists():
        if not target.is_dir():
            raise DeployError(f"扩展目标不是目录：{target}")
        return
    # Same filesystem permits an atomic directory rename. Interrupted copies
    # remain hidden temporary directories and never masquerade as installed.
    with tempfile.TemporaryDirectory(prefix=".newpress-extension-", dir=target.parent) as temporary:
        staged = Path(temporary) / target.name
        if entries is None:
            copy_extension_tree(source, staged)
        else:
            staged.mkdir()
            for name in entries:
                copy_extension_tree(source / name, staged / name)
        for path in [staged, *staged.rglob("*")]:
            os.chown(path, account.pw_uid, account.pw_gid)
            path.chmod(0o750 if path.is_dir() else 0o640)
        safe_path(target)
        if target.exists():
            raise DeployError(f"扩展目标在复制期间发生变化：{target}")
        staged.rename(target)


def deploy_native(values, args, existing, port, bind, arch):
    import pwd
    if os.geteuid() != 0:
        raise DeployError("原生部署需要安装系统服务，请使用 sudo bash scripts/deploy.sh --mode native ...")
    if not Path("/run/systemd/system").is_dir() or not shutil.which("systemctl"):
        raise DeployError("原生模式需要正在运行的 systemd。")
    if UNIT.exists() and not UNIT.read_text().startswith(MANAGED):
        raise DeployError(f"{UNIT} 不是脚本管理的服务，不会覆盖。")
    values = native_values(values, port, bind)
    if not existing and not port_available(bind, port):
        raise DeployError(f"端口 {bind}:{port} 已被占用。")
    secret_dir = NATIVE_ENV.parent / "secrets"
    initialized = secret_state(secret_dir, ("admin_password_hash",), existing)
    with tempfile.TemporaryDirectory(prefix="newpress-build-") as temporary:
        binary = Path(temporary) / "newpress"
        if getattr(args, "source_plan", {}).get("prebuilt"):
            prebuilt.load(ROOT, arch)
            shutil.copyfile(ROOT / "bin/newpress", binary)
        elif args.binary:
            shutil.copyfile(args.binary, binary)
        else:
            with ui.activity("构建原生 Linux 二进制"):
                build_binary(binary, arch, args.source_plan)
        validate_binary(binary, arch)
        binary.chmod(0o755)
        run([binary, "version"], capture=True)
        if not initialized:
            hashed, login = initial_credentials(values, lambda password: run(
                [binary, "admin", "hash-password"], input=password + "\n", capture=True).stdout, args)
        try:
            account = pwd.getpwnam("newpress")
        except KeyError:
            run(["useradd", "--system", "--user-group", "--home-dir", NATIVE_DATA,
                 "--shell", "/usr/sbin/nologin", "newpress"])
            account = pwd.getpwnam("newpress")
        if account.pw_uid == 0:
            raise DeployError("newpress 服务账号不能使用 root UID。")
        for directory in (NATIVE_DATA, NATIVE_DATA / "data", NATIVE_DATA / "updates"):
            safe_path(directory)
            directory.mkdir(parents=True, exist_ok=True)
            directory.chmod(0o750)
            # The parent stays root-owned so the service cannot replace a
            # staging directory while root copies/chowns its contents.
            os.chown(directory, 0 if directory == NATIVE_DATA else account.pw_uid, account.pw_gid)
        # Do not seed sample/marketplace themes or source-site selection state.
        # Existing extension roots are left intact by install_extension.
        install_extension(ROOT / "themes", NATIVE_DATA / "themes", account, entries=("chunnel",))
        install_extension(ROOT / "plugins", NATIVE_DATA / "plugins", account)
        for directory in (NATIVE_ENV.parent, secret_dir):
            safe_path(directory)
            directory.mkdir(parents=True, exist_ok=True)
            directory.chmod(0o750)
            os.chown(directory, 0, account.pw_gid)
        if not initialized:
            atomic_write(secret_dir / "admin_password_hash", hashed + "\n", 0o640)
            if login:
                atomic_write(NATIVE_ENV.parent / "first-login.txt", login)
        os.chown(secret_dir / "admin_password_hash", 0, account.pw_gid)
        (secret_dir / "admin_password_hash").chmod(0o640)
        # Keep the previous executable for explicit recovery. Data files and
        # online-update state are never rolled back implicitly.
        safe_path(NATIVE_BIN)
        NATIVE_BIN.parent.mkdir(parents=True, exist_ok=True)
        NATIVE_BIN.parent.chmod(0o755)
        if NATIVE_BIN.exists():
            atomic_write(NATIVE_BIN.with_suffix(".previous"), NATIVE_BIN.read_bytes(), 0o755)
        if UNIT.exists():
            run(["systemctl", "stop", "newpress"], capture=True)
        if not port_available(bind, port):
            if UNIT.exists():
                run(["systemctl", "start", "newpress"], check=False, capture=True)
            raise DeployError(f"端口 {bind}:{port} 被其他进程占用，未替换运行程序。")
        atomic_write(NATIVE_BIN, binary.read_bytes(), 0o755)
        write_env(NATIVE_ENV, values, remove=("NEWPRESS_ADMIN_PASSWORD_HASH",))
        atomic_write(UNIT, (ROOT / "deploy/newpress.service").read_text(), 0o644)
        run(["systemctl", "daemon-reload"])
        run(["systemctl", "enable", "--now", "newpress"])
    host = "127.0.0.1" if bind == "0.0.0.0" else bind
    with ui.activity(f"本机健康检查（最多等待 {args.timeout} 秒）"):
        wait_http(f"http://{host}:{port}/health/ready", args.timeout)
    return NATIVE_ENV.parent / "first-login.txt"


def parser():
    cli = argparse.ArgumentParser(description="NewPress Linux 部署与管理；无参数交互运行打开命令菜单。", allow_abbrev=False,
        formatter_class=TerminalHelpFormatter,
        epilog="常用命令：\n  bash scripts/deploy.sh menu\n  bash scripts/deploy.sh deploy\n"
               "  bash scripts/deploy.sh check\n  bash scripts/deploy.sh status\n"
               "  bash scripts/deploy.sh logs --tail 100 --follow\n  bash scripts/deploy.sh restart --yes\n"
               "  bash scripts/deploy.sh stop --yes\n  bash scripts/deploy.sh admin\n"
               "  bash scripts/deploy.sh admin-list\n  bash scripts/deploy.sh admin-reset --admin-id ID\n"
               "  bash scripts/deploy.sh admin-rename --admin-id ID --admin-username NAME\n"
               "仅预览界面：bash scripts/deploy.sh --preview")
    cli._positionals.title = "命令"
    cli._optionals.title = "帮助"
    cli.add_argument("command", nargs="?", metavar="COMMAND", choices=("menu", "deploy", "check", "status", "logs", "restart", "stop",
                     "admin", "admin-list", "admin-reset", "admin-rename"),
                     help="菜单、部署、检查、服务管理与账号恢复；命令示例见下方")
    display = cli.add_argument_group("菜单与终端")
    deployment = cli.add_argument_group("部署设置")
    downloads = cli.add_argument_group("下载源与超时")
    management = cli.add_argument_group("日志管理")
    accounts = cli.add_argument_group("管理员账号")
    accounts.add_argument("--admin-id", type=int, help="账号列表中的管理员 ID；自动化修改时必须明确指定")
    accounts.add_argument("--admin-username", help="首次安装的管理员登录名，或 admin-rename 的新登录名")
    accounts.add_argument("--admin-setup", action="store_true", help="首次安装时交互设置管理员（隐藏密码输入）")
    accounts.add_argument("--password-stdin", action="store_true", help="首次安装或 admin-reset 从标准输入读取密码；不接受明文密码参数")
    display.add_argument("--menu", action="store_true", help="打开交互命令菜单")
    deployment.add_argument("--compose-file", action="append", help="按顺序指定附加 Compose 文件；可重复，保存后供后续命令复用")
    management.add_argument("--tail", type=int, default=100, help="日志末尾行数，1–10000，默认 100")
    management.add_argument("--follow", action="store_true", help="持续查看日志，Ctrl+C 返回菜单或退出命令")
    interaction = display.add_mutually_exclusive_group()
    interaction.add_argument("--yes", "--non-interactive", action="store_true", help="跳过提问和最终确认，用于显式参数或已有配置的自动化部署")
    interaction.add_argument("--interactive", action="store_true", help="强制使用提问式向导")
    appearance = display.add_mutually_exclusive_group()
    appearance.add_argument("--color", choices=("auto", "always", "never"), default="auto", help="终端配色；自动模式遵循 NO_COLOR，重定向时关闭颜色")
    appearance.add_argument("--no-color", dest="color", action="store_const", const="never", help="使用纯文本输出")
    display.add_argument("--preview", action="store_true", help="仅预览终端展示，使用示例数据，不联网或执行部署")
    deployment.add_argument("--mode", choices=("auto", "docker", "native"), default="auto",
                     help="优先沿用已有部署，否则选择可用 Docker 或 systemd")
    deployment.add_argument("--url", help="站点访问地址，例如 https://example.com（TLS 由反向代理配置）")
    deployment.add_argument("--port", type=int, help="宿主监听端口，默认 8080")
    deployment.add_argument("--bind", help="监听 IPv4，默认 0.0.0.0；反代场景可用 127.0.0.1")
    deployment.add_argument("--binary", type=Path, help="原生模式使用已有 Linux 二进制，避免现场构建")
    deployment.add_argument("--install-docker", action="store_true", help="允许自动选源安装 Docker（Debian/Ubuntu，需 root）")
    downloads.add_argument("--source-mode", choices=("auto", "official"), help="auto 测速官方和国内源；official 只使用官方源")
    downloads.add_argument("--source-timeout", type=int, default=6, help="每次源探测的超时秒数，1–30，默认 6")
    downloads.add_argument("--pull-timeout", type=int, default=600, help="每个容器镜像源拉取的超时秒数，默认 600")
    downloads.add_argument("--docker-mirror", action="append", help="额外 Docker Hub 公共镜像源 HTTPS 前缀，可多次指定")
    downloads.add_argument("--gcr-mirror", action="append", help="额外 gcr.io 公共镜像源 HTTPS 前缀，可多次指定")
    deployment.add_argument("--check", action="store_true", help="只检测环境和源（少量网络采样），不写配置、不安装或启动服务")
    downloads.add_argument("--timeout", type=int, default=180, help="健康检查等待秒数，默认 180")
    return cli


def deployment_state(project):
    state_file = ROOT / ".newpress-deploy.json"
    if not state_file.exists():
        return {}
    state = json.loads(state_file.read_text(encoding="utf-8"))
    if not isinstance(state, dict) or state.get("mode") not in ("docker", "native"):
        raise DeployError("部署状态文件无效。")
    if state["mode"] == "docker" and state.get("project") != project:
        raise DeployError("检测到 Compose 项目名变更；请恢复原 COMPOSE_PROJECT_NAME，避免切换数据卷。")
    return state


def project_name(values):
    project = values.get("COMPOSE_PROJECT_NAME", "newpress")
    if not re.fullmatch(r"[a-z0-9][a-z0-9_-]*", project):
        raise DeployError("COMPOSE_PROJECT_NAME 格式无效。")
    return project


@contextlib.contextmanager
def deployment_lock():
    import fcntl
    lock_path = ROOT / ".newpress-deploy.lock"
    safe_path(lock_path)
    with lock_path.open("a") as lock:
        try:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError as error:
            raise DeployError("已有部署或管理命令正在运行。") from error
        yield


def forwarded_options(args, command):
    # Serialize the selected action, not the original menu invocation. Paths
    # become absolute before sudo so elevated execution keeps the same inputs.
    forwarded = [command]
    for key in ("mode", "url", "port", "bind", "source_mode", "admin_id", "admin_username",
                "source_timeout", "pull_timeout", "timeout", "tail", "color", "binary"):
        value = getattr(args, key)
        if value is not None:
            if key == "binary":
                value = Path(value).absolute()
            forwarded.extend(["--" + key.replace("_", "-"), str(value)])
    for key in ("docker_mirror", "gcr_mirror", "compose_file"):
        for value in getattr(args, key) or []:
            if key == "compose_file":
                value = Path(value) if Path(value).is_absolute() else ROOT / value
            forwarded.extend(["--" + key.replace("_", "-"), str(value)])
    for key in ("yes", "interactive", "install_docker", "follow", "admin_setup", "password_stdin"):
        if getattr(args, key):
            forwarded.append("--" + key.replace("_", "-"))
    return forwarded


def sudo_command(args, command):
    if not shutil.which("sudo"):
        raise DeployError("此操作需要 root，请使用 root 重新运行脚本。")
    password = getattr(args, "_stdin_password", None)
    options = {"input": password + "\n"} if args.password_stdin and password is not None else {}
    return run(["sudo", "--", sys.executable, Path(__file__).resolve(),
                *forwarded_options(args, command)], check=False, **options).returncode


def deploy_command(args):
    arch = {"x86_64": "amd64", "aarch64": "arm64", "arm64": "arm64"}.get(platform.machine())
    if not arch:
        raise DeployError(f"不支持的 CPU 架构：{platform.machine()}；支持 amd64/arm64。")
    if args.timeout < 10:
        raise DeployError("--timeout 不能小于 10 秒。")
    if not 1 <= args.source_timeout <= 30 or not 10 <= args.pull_timeout <= 7200:
        raise DeployError("--source-timeout 需在 1–30 秒，--pull-timeout 需在 10–7200 秒。")
    if not interactive(args) and not args.yes and not args.check:
        raise DeployError("没有交互终端；自动化部署请使用 --yes，并传入 --url 等必要参数。")
    ui.banner(interactive=interactive(args), checking=args.check)
    ui.stage(1, "环境检测")
    distro = read_env(Path("/etc/os-release"))
    environment_rows = [("操作系统", distro.get("PRETTY_NAME", "Linux")),
                        ("CPU 架构", arch), ("Python", platform.python_version())]
    if Path("/proc/meminfo").exists():
        match = re.search(r"MemTotal:\s+(\d+)", Path("/proc/meminfo").read_text())
        if match:
            environment_rows.append(("物理内存", f"{int(match[1]) // 1024} MiB"))
    environment_rows.append(("可用磁盘", f"{shutil.disk_usage(ROOT).free // (1024 ** 3)} GiB"))
    values = read_env(ROOT / ".env", compose=True)
    project = project_name(values)
    native_exists = native_existing()
    ready = docker_ready()
    existing = docker_existing(project) if ready else False
    state_file = ROOT / ".newpress-deploy.json"
    state = deployment_state(project)
    native_exists = native_exists or state.get("mode") == "native"
    existing = existing or state.get("mode") == "docker"
    if args.mode == "auto" and not ready and shutil.which("docker") and not native_exists:
        raise DeployError("已安装 Docker 但 daemon 不可访问，无法确认已有部署；请检查 Docker 权限/服务，或明确指定 --mode native。")
    systemd = Path("/run/systemd/system").is_dir() and bool(shutil.which("systemctl"))
    environment_rows.extend([("Docker", "可用" if ready else "未就绪"), ("systemd", "可用" if systemd else "未就绪")])
    ui.panel("目标环境", environment_rows)
    ui.line("部署包使用预编译程序和预构建镜像，无需 Go、Node.js 或 Buildx。" if (ROOT / "deployment.json").is_file()
            else "源码构建建议至少 2 GiB 内存；可使用预编译二进制。", "muted")
    ui.stage(2, "部署设置", "沿用已有部署与已保存的默认值")
    wizard_mode(args, native_exists, existing, ready, systemd)
    mode = select_mode(args.mode, native_exists, existing, ready, systemd)
    if args.binary and mode != "native":
        raise DeployError("--binary 仅用于原生模式。")
    if args.install_docker and mode != "docker":
        raise DeployError("--install-docker 请同时指定 --mode docker。")
    if mode == "native" and not systemd:
        raise DeployError("原生模式需要运行中的 systemd。")
    if mode == "docker" and not ready:
        if shutil.which("docker"):
            raise DeployError("已安装 Docker 但 daemon 不可访问；请先检查权限和服务状态，不会覆盖现有安装。")
        if distro.get("ID") not in ("debian", "ubuntu") or not systemd:
            raise DeployError("自动安装 Docker 需要 Debian/Ubuntu 和运行中的 systemd；其他环境请先安装 Docker。")
        if not args.check and not args.install_docker and interactive(args):
            args.install_docker = yes_no("未安装 Docker，是否允许检测软件源并自动安装 Engine、Compose 和 Buildx")
        if not args.check and not args.install_docker:
            raise DeployError("Docker 未安装；请使用 --install-docker 或在向导中允许自动安装。")
    if mode == "native" and native_exists and os.geteuid() != 0:
        if not shutil.which("sudo"):
            raise DeployError("读取已有原生配置需要 root，请使用 root 重新运行脚本。")
        log("读取已有原生配置需要 root，将通过 sudo 继续向导。")
        args.mode = mode
        return sudo_command(args, "check" if args.check else "deploy")
    if mode == "native" and NATIVE_ENV.exists():
        values = read_env(NATIVE_ENV)
    previous_port = values.get("NEWPRESS_HTTP_PORT", "8080")
    previous_bind = values.get("NEWPRESS_HTTP_BIND", "0.0.0.0")
    is_existing = native_exists if mode == "native" else existing
    if mode == "native" and args.compose_file is not None:
        raise DeployError("--compose-file 仅用于 Docker 模式。")
    if args.check and not args.url and not values.get("NEWPRESS_PUBLIC_URL"):
        args.url = "http://localhost:" + str(args.port or values.get("NEWPRESS_HTTP_PORT", "8080"))
    wizard_network(args, values, mode, is_existing)
    configure_proxy(values, args, mode)
    configure_sources(values, args)
    origin, port, bind = resolve_network(values, args)
    values.update(NEWPRESS_PUBLIC_URL=origin, NEWPRESS_HTTP_PORT=str(port), NEWPRESS_HTTP_BIND=bind)
    values.setdefault("NEWPRESS_SEED_DEMO", "false")
    if mode == "native":
        native_values(values, port, bind)
        if UNIT.exists() and not UNIT.read_text().startswith(MANAGED):
            raise DeployError(f"{UNIT} 不是脚本管理的服务，不会覆盖。")
        if args.binary:
            validate_binary(args.binary, arch)
        initialized = secret_state(NATIVE_ENV.parent / "secrets", ("admin_password_hash",), is_existing)
    if (not is_existing or str(port) != previous_port or bind != previous_bind) and not port_available(bind, port):
        raise DeployError(f"端口 {bind}:{port} 已占用。")
    if mode == "docker":
        select_compose_files(values, args.compose_file, discover=ready and existing)
        initialized = secret_state(docker_secret_directory(values), SECRET_NAMES, is_existing)
        if ready:
            inspect_local_docker()
            check_docker_plugins(args)
            choose_subnet(values)
    if args.admin_username:
        args.admin_username = validate_admin_username(args.admin_username)
    if initialized and (args.admin_setup or args.admin_username or args.password_stdin):
        raise DeployError("管理员已初始化；修改登录名或密码请使用 admin 菜单，不会通过重复部署覆盖已有账号。")
    if not args.check and not initialized and interactive(args) and not args.password_stdin:
        args.admin_setup = True
    ui.line("反向代理与 TLS 请手动配置；可信代理设置从配置文件读取。参见 README.md 的「域名与 HTTPS」。", "muted")
    if values["NEWPRESS_PROXY_MODE"] != "none":
        host = "127.0.0.1" if bind == "0.0.0.0" else bind
        log(f"自行配置反向代理与 TLS：转发到 http://{host}:{port}，保留 Host，设置 X-Forwarded-For 和 X-Forwarded-Proto。")
        if values["NEWPRESS_PROXY_MODE"] == "remote":
            log("远程或容器代理请将上游地址中的 IP 换为代理可达的本服务器地址；可信来源为 " + values["NEWPRESS_EXTERNAL_PROXY_CIDRS"])
    ui.stage(3, "下载源检测", "检测实际文件与镜像层，按本次采样耗时排序")
    args.source_plan = prepare_sources(args, distro, mode, ready, arch, values)
    if not args.source_plan:
        log("使用本地预编译二进制，无需检测构建下载源。", "ok")
    ui.stage(4, "检查完成" if args.check else "确认部署")
    proxy_labels = {"local": "本机反向代理 · 自行配置 TLS", "remote": "远程 / 容器反向代理", "none": "直接 HTTP 访问"}
    plan_rows = [("部署方式", ("Docker Compose" if mode == "docker" else "原生二进制 + systemd") +
                          (" · 已有部署" if is_existing else " · 首次安装")),
                         ("监听地址", f"{bind}:{port}"), ("公开地址", origin),
                         ("访问方式", proxy_labels[values["NEWPRESS_PROXY_MODE"]]),
                         ("下载策略", "自动测速与切换" if args.source_mode == "auto" else "仅官方源"),
                         ("配置文件", str(NATIVE_ENV if mode == "native" else ROOT / ".env"))]
    if mode == "docker":
        plan_rows.append(("Compose 文件", values["COMPOSE_FILE"].replace(";", "\n")))
    ui.panel("部署计划", plan_rows)
    if args.check:
        log("环境与下载源检查通过；正式部署还会校验完整产物并等待服务就绪。", "ok")
        ui.line("本次为 --check，只读检测已结束。", "muted")
        return 0
    if interactive(args) and not yes_no("按以上设置开始部署（已有服务会重启）"):
        log("已取消，未写入配置或安装服务。", "warn")
        return 0
    if (mode == "native" or not ready) and os.geteuid() != 0:
        if not shutil.which("sudo"):
            raise DeployError("安装系统服务需要 root，请使用 root 重新运行脚本。")
        log("安装系统服务需要系统权限，将通过 sudo 继续；已选择的部署方式和参数会保留。")
        args.yes, args.interactive = True, False
        args.mode, args.url, args.port, args.bind = mode, origin, port, bind
        return sudo_command(args, "deploy")
    with deployment_lock():
        ui.stage(5, "安装与启动", "下方保留安装器、构建器与服务的实时输出")
        if mode == "native":
            credentials = deploy_native(values, args, is_existing, port, bind, arch)
        else:
            if not ready:
                install_docker(distro, args.source_plan)
            credentials = deploy_docker(values, args, is_existing, port, bind, arch)
        atomic_write(state_file, json.dumps({"mode": mode, "project": project}) + "\n")
    ui.finished(origin, credentials if credentials.exists() else None)
    return 0


def management_context(args):
    values = read_env(ROOT / ".env", compose=True)
    project = project_name(values)
    state = deployment_state(project)
    native = native_existing() or state.get("mode") == "native"
    ready = docker_ready()
    docker = (docker_existing(project) if ready else False) or state.get("mode") == "docker"
    systemd = Path("/run/systemd/system").is_dir() and bool(shutil.which("systemctl"))
    if not native and not docker:
        raise DeployError("未发现已有部署，请先选择安装/更新。")
    mode = select_mode(args.mode, native, docker, ready, systemd)
    if mode == "docker":
        if not ready:
            raise DeployError("本机 Docker daemon 不可访问，请检查权限与服务。")
        select_compose_files(values, args.compose_file, discover=True)
    else:
        if args.compose_file is not None:
            raise DeployError("--compose-file 仅用于 Docker 模式。")
        safe_path(UNIT)
        if not UNIT.is_file() or not UNIT.read_text(encoding="utf-8").startswith(MANAGED):
            raise DeployError("没有脚本管理的 newpress.service，不能执行此操作。")
        if not systemd:
            raise DeployError("原生管理命令需要正在运行的 systemd。")
    return values, mode


def manage_command(args):
    values, mode = management_context(args)
    if args.command in ("restart", "stop") and not args.yes:
        if not interactive(args):
            raise DeployError("重启/停止需要交互确认；自动化请显式指定 --yes。")
        label = "重启" if args.command == "restart" else "停止"
        if not yes_no(label + "站点服务" + ("（保留数据库运行）" if mode == "docker" else ""), default=False):
            log("操作已取消。")
            return 0
        args.yes, args.interactive = True, False
    if mode == "native" and os.geteuid() != 0:
        args.mode = mode
        log("读取服务日志或管理原生服务需要系统权限，将通过 sudo 继续所选操作。")
        return sudo_command(args, args.command)
    action_labels = {"status": "查看状态", "logs": "查看日志", "restart": "重启站点", "stop": "停止站点"}
    rows = [("部署方式", "Docker Compose" if mode == "docker" else "原生二进制 + systemd"),
            ("当前操作", action_labels[args.command])]
    if args.command == "logs":
        rows.append(("日志范围", f"最近 {args.tail} 行" + (" · 持续跟随，Ctrl+C 返回" if args.follow else "")))
    ui.panel("服务管理", rows)
    if args.command == "status":
        if mode == "docker":
            return compose(values, "ps", "--all", check=False).returncode
        return run(["systemctl", "status", "newpress.service", "--no-pager", "--full"], check=False).returncode
    if args.command == "logs":
        if mode == "docker":
            return compose(values, "logs", "--tail", str(args.tail),
                           *(["--follow"] if args.follow else []), check=False).returncode
        return run(["journalctl", "-u", "newpress.service", "-n", str(args.tail), "--no-pager",
                    *(["--follow"] if args.follow else [])], check=False).returncode
    with deployment_lock():
        if mode == "docker":
            if args.command == "stop":
                compose(values, "stop", "proxy", "app")
            else:
                # restart also starts previously stopped containers. It never
                # recreates containers, builds images or reruns migrations.
                compose(values, "restart", "--no-deps", "app", "proxy")
        else:
            run(["systemctl", args.command, "newpress.service"])
        if args.command == "restart":
            if mode == "docker":
                compose(values, "up", "-d", "--no-build", "--no-recreate", "--no-deps",
                        "--wait", "--wait-timeout", str(args.timeout), "app", "proxy")
            if mode == "native":
                values = read_env(NATIVE_ENV)
            _, port, bind = resolve_network(values, parser().parse_args([]))
            host = "127.0.0.1" if bind == "0.0.0.0" else bind
            endpoint = "live" if mode == "docker" else "ready"
            wait_http(f"http://{host}:{port}/health/{endpoint}", args.timeout)
    log("站点服务已" + ("重启并通过健康检查。" if args.command == "restart" else "停止。"), "ok")
    return 0


def native_admin_identity():
    import pwd
    account = pwd.getpwnam("newpress")
    if account.pw_uid == 0 or account.pw_gid == 0:
        raise DeployError("账号维护必须使用非 root 的 newpress 服务身份。")
    return {"user": account.pw_uid, "group": account.pw_gid, "extra_groups": []}


def discard_initial_login(values, mode):
    path = (NATIVE_ENV.parent if mode == "native" else docker_secret_directory(values)) / "first-login.txt"
    try:
        safe_path(path)
        path.unlink(missing_ok=True)
    except (OSError, DeployError):
        log("账号已修改，但旧 first-login.txt 未能删除；请手动删除该初始凭据文件。", "warn")


def run_admin_backend(values, mode, args, arguments, password=None):
    mutation = arguments[0] != "list"
    stdin = password + "\n" if password is not None else ""
    def execute():
        if mode == "docker":
            result = compose(values, "run", "--rm", "--no-deps", "-T", "app", "admin", *arguments,
                             capture=True, check=False, input=stdin)
        else:
            safe_path(NATIVE_BIN)
            environment = {key: value for key, value in values.items() if key.startswith("NEWPRESS_")}
            environment.update(PATH="/usr/local/bin:/usr/bin:/bin", LANG="C.UTF-8")
            result = run([NATIVE_BIN, "admin", *arguments], env=environment, cwd=NATIVE_DATA,
                         identity=native_admin_identity(), capture=True, check=False, input=stdin)
        if result.returncode:
            raise DeployError("管理员命令失败；请确认已更新应用、存储可用且账号版本未变化。原生文件模式还需停止其他实例。不会打印含敏感信息的原始输出。")
        if mutation:
            discard_initial_login(values, mode)
        try:
            return json.loads(result.stdout)
        except (ValueError, TypeError) as error:
            raise DeployError("管理员命令未返回有效结果；若是修改操作，请先重新查询账号再决定是否重试。") from error
    if mode == "docker" or not mutation:
        return execute()
    state = run(["systemctl", "show", "newpress.service", "--property=ActiveState", "--value"], capture=True).stdout.strip()
    if state not in ("active", "inactive", "failed"):
        raise DeployError("服务正在切换状态，请稍后重试账号操作。")
    running = state == "active"
    try:
        if running:
            log("暂时停止原生服务，以独占方式修改账号。")
            run(["systemctl", "stop", "newpress.service"], capture=True)
        return execute()
    finally:
        if running:
            restored = run(["systemctl", "start", "newpress.service"], capture=True, check=False)
            if restored.returncode:
                raise DeployError("账号维护已结束，但服务恢复失败；请查看服务日志。账号可能已修改，重试前请先查询。")
            _, port, bind = resolve_network(values, parser().parse_args([]))
            host = "127.0.0.1" if bind == "0.0.0.0" else bind
            with ui.activity("恢复原生服务并检查就绪状态"):
                wait_http(f"http://{host}:{port}/health/ready", args.timeout)


def admin_command(args):
    values, mode = management_context(args)
    if mode == "native":
        if os.geteuid() != 0:
            log("账号管理需要读取服务配置，将通过 sudo 继续；随后再安全输入密码。")
            args.mode = mode
            return sudo_command(args, args.command)
        safe_path(NATIVE_ENV)
        values = read_env(NATIVE_ENV)
    with deployment_lock():
        accounts = run_admin_backend(values, mode, args, ["list"])
        if not isinstance(accounts, list) or any(not isinstance(item, dict) or
                type(item.get("id")) is not int or item["id"] <= 0 or
                type(item.get("version")) is not int or item["version"] <= 0 or
                not isinstance(item.get("username"), str) or item.get("status") not in ("active", "suspended")
                for item in accounts):
            raise DeployError("管理员列表格式无效，请先更新应用。")
        ui.panel("管理员账号", [(f"ID {item['id']}", item["username"] +
                 (" · 启用" if item["status"] == "active" else " · 停用")) for item in accounts] or [("账号", "未发现管理员")])
        if args.command == "admin-list":
            ui.line("账号列表不包含密码或哈希。忘记密码可使用 admin-reset。", "muted")
            return 0
        if not accounts:
            raise DeployError("没有可修改的管理员；请先完成首次安装。")
        selected = {item["id"]: item for item in accounts}
        def choose(value):
            identifier = int(value)
            if identifier not in selected or selected[identifier]["status"] != "active":
                raise DeployError("请选择列表中已启用的管理员 ID；此命令不会启用或提升其他账号。")
            return identifier
        if args.admin_id is None:
            if not interactive(args):
                raise DeployError("自动化修改需要显式指定 --admin-id，避免修改错误账号。")
            args.admin_id = ask("要修改的管理员 ID", "", choose)
        identifier = choose(args.admin_id)
        account = selected[identifier]
        renamed = None
        if args.command == "admin-rename":
            renamed = validate_admin_username(args.admin_username) if args.admin_username else None
            if renamed is None:
                if not interactive(args):
                    raise DeployError("修改登录名需要 --admin-username。")
                renamed = ask("新的管理员登录名", "", validate_admin_username)
        title = "修改登录名" if renamed is not None else "重置密码"
        ui.panel("确认账号操作", [("管理员", f"ID {identifier} · {account['username']}"), ("操作", title)] +
                 ([("新登录名", renamed)] if renamed is not None else [("影响", "旧密码及旧登录会话失效")]))
        if not args.yes:
            if not interactive(args):
                raise DeployError("账号修改需要交互确认；自动化请显式指定 --yes。")
            if not yes_no("确认" + title, default=False):
                log("已取消账号修改。")
                return 0
        arguments = ["rename" if renamed is not None else "reset-password", "--id", str(identifier),
                     "--expected-version", str(account["version"])]
        password = None
        if renamed is not None:
            arguments.extend(["--username", renamed])
        else:
            password, _ = read_password(args)
            arguments.append("--password-stdin")
        result = run_admin_backend(values, mode, args, arguments, password)
        if not isinstance(result, dict) or result.get("id") != identifier or result.get("version") != account["version"] + 1:
            raise DeployError("账号操作的结果未能确认，请重新查询账号；不要盲目重试。")
        log(title + "成功；" + ("请使用新登录名登录。" if renamed is not None else "请使用新密码重新登录，旧会话已失效。"), "ok")
    return 0


ADMIN_MENU_CHOICES = [("1", "查看管理员账号", "查询登录名与状态，不显示密码或哈希"),
                      ("2", "重置管理员密码", "隐藏输入新密码，使旧登录会话失效"),
                      ("3", "修改管理员登录名", "保留账号 ID、角色、内容与其他资料"),
                      ("0", "返回", "")]


def admin_menu(args):
    if not interactive(args):
        raise DeployError("管理员菜单需要交互终端；请直接使用 admin-list/admin-reset/admin-rename。")
    while True:
        ui.menu("管理员账号", ADMIN_MENU_CHOICES, "0")
        ui.line("忘记密码请重置；已有密码只存哈希，无法还原。", "muted")
        try:
            choice = input(ui.prompt("选择操作", "0")).strip() or "0"
        except (EOFError, KeyboardInterrupt):
            return 0
        if choice == "0":
            return 0
        if choice not in ("1", "2", "3"):
            log("请选择 0–3。", "warn")
            continue
        selected = copy.deepcopy(args)
        selected.command = {"1": "admin-list", "2": "admin-reset", "3": "admin-rename"}[choice]
        try:
            code = admin_command(selected)
            if code:
                log(f"管理员命令返回退出码 {code}。", "warn")
        except KeyboardInterrupt:
            log("已中断账号操作。", "warn")
        except (DeployError, OSError, ValueError) as error:
            log(str(error), "warn")


MENU_CHOICES = [("1", "安装 / 更新", "沿用已有数据、配置与部署方式"),
                ("2", "环境检查", "检查本机环境与下载源，不安装服务"),
                ("3", "查看状态", "显示当前服务状态"),
                ("4", "查看日志", "显示最近日志；--follow 可持续查看"),
                ("5", "重启", "重启站点；Docker 数据库保持运行"),
                ("6", "停止", "停止站点；Docker 数据库保持运行"),
                ("7", "管理员账号", "查看登录名、修改登录名、重置密码"),
                ("0", "退出", "")]


def dispatch(args):
    global DOCKER_ENDPOINT
    DOCKER_ENDPOINT = None
    ui.begin(total_stages=4 if args.command == "check" else 6)
    if args.command == "admin":
        return admin_menu(args)
    if args.command.startswith("admin-"):
        return admin_command(args)
    if args.command in ("deploy", "check"):
        args.check = args.command == "check"
        if args.check:
            args.yes, args.interactive = True, False
        return deploy_command(args)
    return manage_command(args)


def command_menu(args):
    if not interactive(args):
        raise DeployError("命令菜单需要交互终端；自动化请直接指定 deploy/check/status/logs/restart/stop。")
    actions = dict(zip("1234567", ("deploy", "check", "status", "logs", "restart", "stop", "admin")))
    while True:
        ui.menu("NewPress 命令菜单", MENU_CHOICES, "0")
        try:
            choice = input(ui.prompt("选择操作", "0")).strip() or "0"
        except (EOFError, KeyboardInterrupt):
            ui.line("已退出菜单。")
            return 0
        if choice == "0":
            return 0
        if choice not in actions:
            log("请选择 0–7。", "warn")
            continue
        selected = copy.deepcopy(args)
        selected.command, selected.menu = actions[choice], False
        try:
            code = dispatch(selected)
            if code:
                log(f"命令返回退出码 {code}，可根据上方输出排查后重试。", "warn")
        except KeyboardInterrupt:
            log("已中断当前命令，返回菜单。", "warn")
        except (DeployError, sources.SourceError, OSError, ValueError, urllib.error.URLError) as error:
            log(str(error), "warn")


def main(argv=None):
    global ui
    argv = list(sys.argv[1:] if argv is None else argv)
    args = parser().parse_args(argv)
    ui = Terminal(args.color)
    if sys.version_info < (3, 9):
        raise DeployError("需要 Python 3.9 或更新版本。")
    if args.preview:
        ui.menu("NewPress 命令菜单", MENU_CHOICES, "0")
        preview(ui)
        return 0
    if platform.system() != "Linux":
        raise DeployError("部署脚本仅支持 Linux；请在目标服务器运行。")
    if args.menu and args.command not in (None, "menu"):
        raise DeployError("--menu 不能与直接命令一起使用。")
    if args.check and (args.menu or args.command not in (None, "check", "deploy")):
        raise DeployError("--check 不能与管理命令或菜单一起使用。")
    if not 1 <= args.tail <= 10000 or args.timeout < 10:
        raise DeployError("--tail 需在 1–10000 行，--timeout 不能小于 10 秒。")
    args.command = ("menu" if args.menu or (not argv and interactive(args)) else
                    "check" if args.check else args.command or "deploy")
    if args.password_stdin and (not args.yes or args.command not in ("deploy", "admin-reset")):
        raise DeployError("--password-stdin 仅用于 deploy/admin-reset，并需 --yes；不会将密码流用作菜单或确认输入。")
    if args.admin_setup and args.command != "deploy":
        raise DeployError("--admin-setup 仅用于首次安装。")
    if args.admin_username and args.command not in ("deploy", "admin-rename"):
        raise DeployError("--admin-username 仅用于首次安装或 admin-rename。")
    if args.admin_id is not None and (args.admin_id <= 0 or args.command not in ("admin-reset", "admin-rename")):
        raise DeployError("--admin-id 仅用于 admin-reset/admin-rename，且必须为正整数。")
    if args.password_stdin:
        # Consume before any probe/build subprocess can inherit the pipe.
        # When sudo is needed, forward only via its private stdin pipe.
        args._stdin_password, _ = read_password(args)
    if args.command == "menu":
        return command_menu(args)
    return dispatch(args)


if __name__ == "__main__":
    try:
        sys.exit(main())
    except KeyboardInterrupt:
        Terminal(ui.color, sys.stderr).failure("操作已中断；已有数据保留。", interrupted=True)
        sys.exit(130)
    except (DeployError, sources.SourceError, OSError, ValueError, urllib.error.URLError) as error:
        Terminal(ui.color, sys.stderr).failure(str(error))
        sys.exit(1)
