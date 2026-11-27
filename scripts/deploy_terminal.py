"""Small, dependency-free terminal presentation for the deployment wizard."""
import argparse
from contextlib import contextmanager
import os
import re
import shutil
import sys
import time
import unicodedata


ESCAPES = re.compile(r"\x1b(?:\[[0-?]*[ -/]*[@-~]|\][^\x07\x1b]*(?:\x07|\x1b\\))")
COLORS = {"blue": "1;94", "cyan": "36", "muted": "90", "bold": "1", "warn": "33", "error": "1;31"}


def clean(value):
    text = ESCAPES.sub("", str(value)).replace("\t", "    ")
    return "".join(char for char in text if char == "\n" or unicodedata.category(char) not in ("Cc", "Cf"))


def cells(value):
    return sum(0 if unicodedata.combining(char) else 2 if unicodedata.east_asian_width(char) in "WF" else 1
               for char in clean(value))


def pad(value, width):
    text = clean(value)
    return text + " " * max(0, width - cells(text))


def wrap(value, width):
    """Wrap CJK and long paths by display cells without dropping their content."""
    lines = []
    for paragraph in clean(value).split("\n"):
        current, used = "", 0
        for char in paragraph:
            size = cells(char)
            if current and used + size > width:
                lines.append(current)
                current, used = "", 0
            current += char
            used += size
        lines.append(current)
    return lines


def duration(seconds):
    if seconds < 60:
        return f"{seconds:.1f} 秒"
    minutes, remaining = divmod(int(seconds), 60)
    return f"{minutes} 分 {remaining:02d} 秒"


class TerminalHelpFormatter(argparse.HelpFormatter):
    """argparse option text wraps by display cells, including CJK text."""
    def _split_lines(self, text, width):
        return wrap(re.sub(r"\s+", " ", text).strip(), width)

    def _fill_text(self, text, width, indent):
        return "\n".join(indent + line for line in wrap(text, max(2, width - cells(indent))))


class Terminal:
    def __init__(self, color="auto", stream=None, width=None):
        self.color = color
        self._stream = stream
        self._width = width
        self.started = time.monotonic()
        self.probes = {}
        self.total_stages = 6

    def begin(self, total_stages=6):
        """Start a new operation without counting time spent in the main menu."""
        self.started = time.monotonic()
        self.probes.clear()
        self.total_stages = total_stages

    @property
    def stream(self):
        # Resolve at call time so redirected stdout and tests stay isolated.
        return self._stream if self._stream is not None else sys.stdout

    @property
    def width(self):
        return max(16, min(self._width or shutil.get_terminal_size((80, 24)).columns, 96))

    @property
    def colored(self):
        if self.color != "auto":
            return self.color == "always"
        return (self.stream.isatty() and "NO_COLOR" not in os.environ and os.environ.get("TERM") != "dumb")

    @property
    def unicode(self):
        if os.environ.get("TERM") == "dumb":
            return False
        try:
            "─│┌┐└┘›".encode(self.stream.encoding or "utf-8")
            return True
        except (UnicodeError, LookupError):
            return False

    def paint(self, value, tone=""):
        text = clean(value)
        return f"\033[{COLORS[tone]}m{text}\033[0m" if self.colored and tone else text

    def write(self, text=""):
        encoding = self.stream.encoding or "utf-8"
        try:
            text.encode(encoding)
        except UnicodeEncodeError:
            text = text.encode(encoding, errors="replace").decode(encoding)
        print(text, file=self.stream, flush=True)

    def line(self, text="", tone="", indent=2):
        for part in wrap(text, max(2, self.width - indent - 1)):
            self.write(" " * indent + self.paint(part, tone))

    def hanging(self, prefix, text, tone="", indent=2, prefix_tone=None):
        """Keep continuation lines aligned with the text, not its marker."""
        start = " " * indent + clean(prefix)
        available = self.width - cells(start) - 2
        if available < 8:
            self.line(prefix, prefix_tone or tone, indent)
            self.line(text, tone, min(indent + 2, self.width // 3))
            return
        for index, part in enumerate(wrap(text, available)):
            leader = self.paint(start, prefix_tone or tone) if index == 0 else " " * cells(start)
            self.write(leader + self.paint(part, tone))

    def rule(self):
        self.line(("─" if self.unicode else "-") * (self.width - 4), "blue")

    def banner(self, preview=False, interactive=True, checking=False):
        with self.frame("NEWPRESS  /  " + ("部署检查" if checking else "部署向导")) as (row, divider, width):
            row("Linux · Docker Compose / systemd", "muted")
        if preview:
            self.line("展示预览 · 以下均为示例数据", "warn")
            self.line("不连接网络，不安装服务，不修改配置。", "muted")
        elif interactive:
            self.line("回车采用默认值 · Ctrl+C 退出", "muted")
        else:
            self.line("按命令行参数与已保存配置执行", "muted")

    def stage(self, number, title, detail=""):
        self.write()
        prefix = f"[{number:02d}/{self.total_stages:02d}]  "
        self.hanging(prefix, title, "blue")
        if detail:
            self.line(detail, "muted", 2 + cells(prefix) if self.width >= 48 else 4)

    def status(self, message, level="info"):
        label, tone = {"info": ("信息", "cyan"), "ok": ("完成", "blue"),
                       "warn": ("提醒", "warn"), "error": ("失败", "error"),
                       "run": ("执行", "cyan")}[level]
        self.hanging(f"[{label}]  ", message, prefix_tone=tone)

    @contextmanager
    def frame(self, title, tone="blue"):
        """Shared geometry for menus and summaries, with two-cell margins."""
        self.write()
        width = self.width - 8
        left, right, vertical, bottom_left, bottom_right, horizontal, mid_left, mid_right = (
            ("┌", "┐", "│", "└", "┘", "─", "├", "┤") if self.unicode else ("+", "+", "|", "+", "+", "-", "+", "+"))
        self.write("  " + self.paint(left + horizontal * (width + 2) + right, tone))
        def row(text, style=""):
            for part in wrap(text, width):
                self.write("  " + self.paint(vertical, tone) + " " + self.paint(part, style) +
                           " " * max(0, width - cells(part)) + " " + self.paint(vertical, tone))
        def divider():
            self.write("  " + self.paint(mid_left + horizontal * (width + 2) + mid_right, tone))
        row(title, "bold")
        divider()
        yield row, divider, width
        self.write("  " + self.paint(bottom_left + horizontal * (width + 2) + bottom_right, tone))

    def panel(self, title, rows, tone="blue"):
        rows = list(rows)
        label_width = max((cells(label) for label, _ in rows), default=0)
        with self.frame(title, tone) as (row, divider, width):
            columns = self.width >= 60 and width - label_width - 2 >= 24
            for index, (label, value) in enumerate(rows):
                if columns:
                    for offset, part in enumerate(wrap(value, width - label_width - 2)):
                        row(pad(label if offset == 0 else "", label_width) + "  " + part)
                else:
                    row(label, "muted")
                    for part in wrap(value, width - 2):
                        row("  " + part)
                    if index + 1 < len(rows):
                        row("")

    def menu(self, title, choices, default):
        choices = [(str(key), clean(label), clean(description)) for key, label, description in choices]
        key_width = max((cells(key) for key, _, _ in choices), default=1)
        labels = [f"[{pad(key, key_width)}] {label}" + (" (默认)" if key == str(default) else "")
                  for key, label, _ in choices]
        label_width = max((cells(label) for label in labels), default=0)
        with self.frame(title) as (row, divider, width):
            columns = self.width >= 72 and width - label_width - 2 >= 24
            for index, ((key, label, description), display) in enumerate(zip(choices, labels)):
                selected = key == str(default)
                if columns:
                    for offset, part in enumerate(wrap(description, width - label_width - 2)):
                        row(pad(display if offset == 0 else "", label_width) + "  " + part, "blue" if selected else "")
                else:
                    prefix = f"[{pad(key, key_width)}] "
                    for offset, part in enumerate(wrap(display[len(prefix):], width - cells(prefix))):
                        row((prefix if offset == 0 else " " * cells(prefix)) + part, "blue" if selected else "")
                    if description:
                        for part in wrap(description, width - cells(prefix)):
                            row(" " * cells(prefix) + part, "muted")
                    if index + 1 < len(choices):
                        row("")

    def prompt(self, question, default=""):
        self.write()
        self.hanging("? ", question, "bold")
        self.line("默认 " + str(default) + " · 回车采用" if str(default) else "请输入后回车确认", "muted", 4)
        return self.paint("  > ", "blue")

    def source_columns(self, total):
        progress = max(5, 2 * len(str(total)) + 3)
        available = self.width - 4 - progress - 4
        name = min(28, max(16, available // 2))
        return progress, name, available - name

    def source_start(self, label, total):
        self.probes[label] = [0, total]
        self.write()
        self.line(label, "bold")
        self.line(f"正在采样 {total} 个候选源…", "muted", 4)
        if self.width >= 64:
            progress, name, outcome = self.source_columns(total)
            self.write("  " + self.paint(pad("进度", progress) + "  " + pad("来源", name) + "  结果 / 采样", "muted"))

    def source_result(self, label, name, seconds=None, error=None):
        current = self.probes.setdefault(label, [0, 1])
        current[0] += 1
        outcome = f"可用 · {duration(seconds)}" if error is None else f"跳过 · {error}"
        tone = "cyan" if error is None else "muted"
        digits = len(str(current[1]))
        counter = f"[{current[0]:>{digits}}/{current[1]}]"
        if self.width >= 64:
            progress, name_width, outcome_width = self.source_columns(current[1])
            names, outcomes = wrap(name, name_width), wrap(outcome, outcome_width)
            for index in range(max(len(names), len(outcomes))):
                row = (pad(counter if index == 0 else "", progress) + "  " +
                       pad(names[index] if index < len(names) else "", name_width) + "  " +
                       (outcomes[index] if index < len(outcomes) else ""))
                self.write("  " + self.paint(row, tone))
        else:
            self.hanging(counter + "  ", name, tone)
            self.line(outcome, "muted", 4)

    def source_selected(self, label, choices):
        self.hanging("首选  ", choices[0]["name"] + f" · 采样 {duration(choices[0]['seconds'])}", "blue", 4)
        if len(choices) > 1:
            self.hanging("备选  ", " → ".join(item["name"] for item in choices[1:]), "muted", 4)

    @contextmanager
    def activity(self, label):
        started = time.monotonic()
        self.status(label, "run")
        yield
        self.status(label + " · 完成，用时 " + duration(time.monotonic() - started), "ok")

    def finished(self, origin, credentials=None, preview=False):
        self.stage(6, "部署完成" + ("（示例）" if preview else ""))
        rows = [("服务状态", "本机健康检查通过"), ("站点地址", origin), ("后台地址", origin + "/admin/")]
        if credentials:
            rows.append(("首次登录", str(credentials)))
        rows.append(("本次用时", "24.1 秒（示例）" if preview else duration(time.monotonic() - self.started)))
        self.panel("已就绪" + (" · 示例" if preview else ""), rows)
        if credentials:
            self.line("初始密码仅保存在权限为 0600 的文件中。", "muted")
        self.line("登录后在后台管理站点、外观与账号。", "muted")
        self.panel("常用命令", [("状态", "bash scripts/deploy.sh status"),
                                 ("日志", "bash scripts/deploy.sh logs --tail 100"),
                                 ("管理账号", "bash scripts/deploy.sh admin")])
        self.write()

    def failure(self, message, interrupted=False):
        self.panel("操作已中断" if interrupted else "操作未完成", [("说明", message)], "warn" if interrupted else "error")
        self.line("排查日志", "bold")
        self.line("Docker: docker compose logs --tail=100 app migrate proxy db", "muted")
        self.line("原生: sudo journalctl -u newpress -n 100 --no-pager", "muted")
        self.write()


def preview(terminal):
    """Render representative states only. No environment reads or deployment."""
    def answer(question, value):
        terminal.prompt(question, value)
        terminal.hanging("> ", value, "blue")

    terminal.begin()
    terminal.banner(preview=True)
    terminal.stage(1, "环境检测")
    terminal.panel("目标环境 · 示例", [("操作系统", "Debian GNU/Linux"), ("CPU 架构", "amd64"),
                                       ("Python", "3.11"), ("物理内存", "4096 MiB"), ("可用磁盘", "32 GiB"),
                                       ("Docker", "可用"), ("systemd", "可用")])
    terminal.stage(2, "部署设置")
    terminal.menu("选择部署方式", [("1", "Docker Compose", "使用 PostgreSQL 与内置代理"),
                                   ("2", "原生二进制 + systemd", "使用文件存储，按需构建二进制")], "1")
    terminal.line("反向代理与 TLS 请手动配置；可信代理设置从配置文件读取。", "muted")
    answer("监听地址", "127.0.0.1")
    answer("应用使用哪个端口", "8080")
    answer("站点访问地址（例如 https://example.com）", "https://example.com")
    terminal.menu("下载源策略", [("1", "自动选择", "检测官方与国内源，失败时切换"),
                                 ("2", "仅官方源", "仅检测和使用原始下载入口")], "1")
    terminal.stage(3, "下载源检测", "以实际文件采样耗时排序")
    terminal.source_start("Docker 安装包 · 示例", 3)
    terminal.source_result("Docker 安装包 · 示例", "国内源 A", seconds=0.72)
    terminal.source_result("Docker 安装包 · 示例", "官方源", error="连接超时")
    terminal.source_result("Docker 安装包 · 示例", "国内源 B", seconds=1.38)
    terminal.source_selected("Docker 安装包 · 示例", [{"name": "国内源 A", "seconds": 0.72}, {"name": "国内源 B", "seconds": 1.38}])
    terminal.stage(4, "确认部署")
    terminal.panel("部署计划 · 示例", [("部署方式", "Docker Compose · 首次安装"), ("监听地址", "127.0.0.1:8080"),
                                      ("公开地址", "https://example.com"), ("访问方式", "本机反向代理，由你配置 TLS"),
                                      ("下载策略", "自动测速与切换"), ("配置文件", "/srv/newpress/.env"),
                                      ("Compose 文件", "/srv/newpress/compose.yaml")])
    answer("按以上设置开始部署", "y")
    terminal.stage(5, "安装与启动")
    answer("管理员登录名（首次安装）", "admin")
    terminal.line("新密码与确认密码均隐藏输入，实时显示已输入位数；自设密码不保存明文。", "muted")
    terminal.status("构建应用镜像", "run")
    terminal.status("构建应用镜像 · 完成，用时 18.4 秒", "ok")
    terminal.status("启动数据库、迁移、应用与代理", "run")
    terminal.status("启动数据库、迁移、应用与代理 · 完成，用时 5.7 秒", "ok")
    terminal.status("本机健康检查 · 通过", "ok")
    terminal.finished("https://example.com", "/srv/newpress/.secrets/first-login.txt", preview=True)
