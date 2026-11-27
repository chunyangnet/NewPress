"""Hidden Linux terminal input with a live character count; standard library only."""
import codecs
import os
import signal
import sys
import unicodedata


class PasswordInputError(Exception):
    pass


class PasswordBuffer:
    """Consume terminal keys without ever rendering the entered characters."""
    def __init__(self):
        self.characters = []
        self.escape = ""
        self.pasting = False
        self.paste_invalid = False

    @property
    def value(self):
        return "".join(self.characters)

    def feed(self, char):
        if char == "\x03":
            raise KeyboardInterrupt
        if self.escape:
            self.escape += char
            if self.escape == "\x1b[200~":
                self.paste_invalid = self.paste_invalid or self.pasting
                self.pasting = True
                self.escape = ""
            elif self.escape == "\x1b[201~":
                self.pasting = False
                self.escape = ""
                if self.paste_invalid:
                    raise PasswordInputError("粘贴的密码含换行、控制字符或超出长度限制，请重新输入。")
            elif (len(self.escape) == 2 and char not in "[O") or (len(self.escape) >= 3 and "@" <= char <= "~") or len(self.escape) > 32:
                self.paste_invalid = self.paste_invalid or self.pasting
                self.escape = ""
            return False
        if char == "\x1b":
            self.escape = char
            return False
        if self.pasting:
            if unicodedata.category(char) in ("Cc", "Cf"):
                self.paste_invalid = True
                return False
        else:
            if char in "\r\n":
                return True
            if char == "\x04":
                raise EOFError
            if char in ("\x7f", "\b"):
                if self.characters:
                    self.characters.pop()
                return False
            if char == "\x15":  # Ctrl+U clears the input.
                self.characters.clear()
                return False
            if char == "\x17":  # Ctrl+W erases the previous word.
                while self.characters and self.characters[-1].isspace():
                    self.characters.pop()
                while self.characters and not self.characters[-1].isspace():
                    self.characters.pop()
                return False
            if char == "\x1a":
                raise PasswordInputError("密码输入已取消。")
            if unicodedata.category(char) in ("Cc", "Cf"):
                return False
        self.characters.append(char)
        if len(self.value.encode("utf-8")) > 1024:
            if self.pasting:
                self.characters.pop()
                self.paste_invalid = True
                return False
            raise PasswordInputError("密码超过 1024 个 UTF-8 字节，请重新输入。")
        return False


def password_prompt(prompt):
    """Use /dev/tty directly; failure never falls back to echoed input."""
    try:
        import termios
    except ImportError as error:
        raise PasswordInputError("此隐藏输入方式需要 Linux 终端。") from error
    if not sys.stdin.isatty():
        raise PasswordInputError("隐藏密码输入需要交互终端。")
    try:
        descriptor = os.open("/dev/tty", os.O_RDWR | os.O_NOCTTY)
    except OSError as error:
        raise PasswordInputError("无法打开安全输入终端，已取消。") from error
    original = None
    bracketed = os.environ.get("TERM") != "dumb"
    state = PasswordBuffer()
    decoder = codecs.getincrementaldecoder("utf-8")("strict")
    handlers = {}
    previous = 0
    def interrupted(number, frame):
        raise PasswordInputError("密码输入已中断。")
    def write(value):
        data = value.encode("utf-8")
        while data:
            count = os.write(descriptor, data)
            if count <= 0:
                raise OSError("terminal write failed")
            data = data[count:]
    try:
        original = termios.tcgetattr(descriptor)
        for name in ("SIGTERM", "SIGHUP"):
            number = getattr(signal, name, None)
            if number is not None:
                handlers[number] = signal.signal(number, interrupted)
        hidden = list(original)
        hidden[6] = list(original[6])
        hidden[0] &= ~(termios.IXON | termios.ICRNL | termios.INLCR | termios.IGNCR)
        # Handle interrupt/suspend explicitly so Ctrl+Z cannot leave the
        # terminal suspended with echo disabled.
        hidden[3] &= ~(termios.ECHO | termios.ECHONL | termios.ICANON | termios.ISIG | termios.IEXTEN)
        hidden[6][termios.VMIN], hidden[6][termios.VTIME] = 1, 0
        termios.tcsetattr(descriptor, termios.TCSAFLUSH, hidden)
        if bracketed:
            write("\x1b[?2004h")
        write(prompt + "\n")
        while True:
            message = f"  已输入 {len(state.characters)} 位"
            write("\r" + message + " " * max(0, previous - len(message)))
            previous = len(message)
            raw = os.read(descriptor, 1)
            if not raw:
                raise EOFError
            for char in decoder.decode(raw):
                if state.feed(char):
                    return state.value
    except (OSError, UnicodeError, termios.error) as error:
        raise PasswordInputError("终端无法安全读取密码，已取消；不会启用明文回显。") from error
    finally:
        try:
            if original is not None:
                termios.tcsetattr(descriptor, termios.TCSAFLUSH, original)
        finally:
            try:
                if bracketed:
                    write("\x1b[?2004l")
                write("\n")
            finally:
                state.characters.clear()
                os.close(descriptor)
                for number, handler in handlers.items():
                    signal.signal(number, handler)
