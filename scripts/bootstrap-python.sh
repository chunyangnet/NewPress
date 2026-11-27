#!/usr/bin/env bash
# Minimal bootstrap when Python is absent. No changes to host apt sources.
set -euo pipefail

np_ui_color=auto
np_ui_note() {
  local np_label="$1" np_text="$2" np_tone="${3:-36}" np_start='' np_end=''
  case "$np_label" in
    INFO) np_label=信息 ;;
    ' OK ') np_label=完成 ;;
    提示) np_label=提醒 ;;
  esac
  if [[ "$np_ui_color" == always ]] || { [[ "$np_ui_color" == auto && -t 1 && "${TERM:-}" != dumb && ! -v NO_COLOR ]]; }; then
    np_start=$'\033['"$np_tone"m np_end=$'\033[0m'
  fi
  printf '  %s[%s]%s  %s\n' "$np_start" "$np_label" "$np_end" "$np_text"
}

np_archive_key() { printf '/usr/share/keyrings/%s-archive-keyring.gpg' "$ID"; }
np_ca_bundle_ready() { [[ -s /etc/ssl/certs/ca-certificates.crt ]]; }

np_bootstrap_python() (
  np_policy=auto np_yes=false np_timeout=6
  while (( $# )); do
    case "$1" in
      --yes|--non-interactive) np_yes=true ;;
      --no-color) np_ui_color=never ;;
      --color) shift; np_ui_color="${1:?缺少 --color 参数}" ;;
      --color=*) np_ui_color="${1#*=}" ;;
      --check|check|status|logs|restart|stop|admin|admin-list|admin-reset|admin-rename|--help|-h) printf '%s\n' '需要 Python 3.9+；检查和管理命令不会安装依赖。' >&2; return 1 ;;
      --password-stdin) printf '%s\n' '请先安装 Python 3.9+；密码输入不会交给安装引导。' >&2; return 1 ;;
      --source-mode) shift; np_policy="${1:?缺少 --source-mode 参数}" ;;
      --source-mode=*) np_policy="${1#*=}" ;;
      --source-timeout) shift; np_timeout="${1:?缺少 --source-timeout 参数}" ;;
      --source-timeout=*) np_timeout="${1#*=}" ;;
    esac
    shift
  done
  [[ "$np_policy" == auto || "$np_policy" == official ]] || { printf '%s\n' '下载源策略必须为 auto 或 official。' >&2; return 1; }
  [[ "$np_ui_color" == auto || "$np_ui_color" == always || "$np_ui_color" == never ]] || return 1
  [[ "$np_timeout" =~ ^[0-9]{1,2}$ ]] && (( 10#$np_timeout >= 1 && 10#$np_timeout <= 30 )) || return 1
  printf '\n'
  printf '  NEWPRESS / Python 引导\n\n'
  np_ui_note 执行 '准备 Python 引导环境' '1;94'
  np_ui_note INFO '完成引导后继续所选命令或打开菜单。'
  if [[ "$np_yes" != true ]]; then
    [[ -t 0 ]] || { printf '%s\n' '缺少 Python；非交互安装需显式使用 --yes。' >&2; return 1; }
    while true; do
      printf '\n  ? 检测软件源并安装 Python 与 CA 证书？\n    y 安装 / n 取消 · 默认 y\n'
      read -r -p '  > ' np_answer || return 1
      case "${np_answer:-y}" in
        y|Y|yes|是) break ;;
        n|N|no|否) np_ui_note 提示 '已取消。' 33; return 1 ;;
        *) np_ui_note 提示 '请输入 y 或 n。' 33 ;;
      esac
    done
  fi
  if (( EUID != 0 )); then
    command -v sudo >/dev/null || { printf '%s\n' '请使用 root 安装 Python。' >&2; return 1; }
    if [[ "$np_ui_color" == auto && ( -v NO_COLOR || "${TERM:-}" == dumb ) ]]; then np_ui_color=never; fi
    sudo -- bash "${BASH_SOURCE[0]}" --yes --source-mode "$np_policy" --source-timeout "$np_timeout" --color "$np_ui_color"
    return
  fi
  # /etc/os-release is a root-managed system file, never a project dotenv file.
  . /etc/os-release
  if [[ "${ID:-}" != debian && "${ID:-}" != ubuntu ]]; then
    np_ui_note INFO '当前发行版的 Python 引导使用系统已配置的软件源。'
    if command -v dnf >/dev/null; then
      dnf install -y python3 ca-certificates
    elif command -v yum >/dev/null; then
      yum install -y python3 ca-certificates
    else
      printf '%s\n' '请先安装 Python 3.9+ 和 CA 证书。' >&2
      return 1
    fi
    return
  fi
  [[ "${VERSION_CODENAME:-}" =~ ^[a-z]+$ ]] || { printf '%s\n' '无法识别发行版代号。' >&2; return 1; }
  np_bootstrap_apt
)

np_bootstrap_apt() (
  np_arch="$(dpkg --print-architecture)"
  np_key="$(np_archive_key)"
  [[ -f "$np_key" ]] || { printf '%s\n' '缺少系统 archive-keyring，无法校验软件包签名。' >&2; return 1; }
  np_dir="$(mktemp -d -t newpress-bootstrap-XXXXXXXX)"
  np_temp_root="$(realpath -- "$(dirname -- "$np_dir")")"
  np_cleanup() {
    # Resolve and constrain the exact mktemp directory before recursive removal.
    case "$(realpath -- "$np_dir")" in
      "$np_temp_root"/newpress-bootstrap-*) [[ ! -L "$np_dir" ]] && rm -rf -- "$np_dir" ;;
    esac
  }
  trap np_cleanup EXIT
  chmod 755 "$np_dir"
  np_directory="$ID"
  np_official=https://deb.debian.org/debian
  if [[ "$ID" == ubuntu ]]; then
    np_official=https://archive.ubuntu.com/ubuntu
    if [[ "$np_arch" == arm64 ]]; then
      np_directory=ubuntu-ports
      np_official=https://ports.ubuntu.com/ubuntu-ports
    fi
  fi
  np_bases=("$np_official")
  if [[ "$np_policy" == auto ]]; then
    for np_mirror in https://mirrors.tuna.tsinghua.edu.cn https://mirrors.ustc.edu.cn https://mirrors.aliyun.com; do
      np_bases+=("$np_mirror/$np_directory")
    done
  fi
  # If curl/wget or the CA bundle is absent, signed apt itself probes candidates.
  # HTTP is allowed only in this bootstrap case; apt signature checks stay on.
  np_fetch=''
  command -v curl >/dev/null && np_fetch=curl
  [[ -n "$np_fetch" ]] || { command -v wget >/dev/null && np_fetch=wget; } || true
  np_ca_bundle_ready || np_fetch=''
  np_security_base() {
    if [[ "$ID" == debian ]]; then
      printf '%s/debian-security' "${1%/debian}"
    elif [[ "$1" == https://archive.ubuntu.com/ubuntu || "$1" == http://archive.ubuntu.com/ubuntu ]]; then
      printf '%s' "${1/archive.ubuntu.com/security.ubuntu.com}"
    else
      printf '%s' "$1"
    fi
  }
  np_write_sources() {
    np_security="$(np_security_base "$1")"
    printf 'Types: deb\nURIs: %s\nSuites: %s %s-updates\nComponents: main\nArchitectures: %s\nSigned-By: %s\n\nTypes: deb\nURIs: %s\nSuites: %s-security\nComponents: main\nArchitectures: %s\nSigned-By: %s\n' \
      "$1" "$VERSION_CODENAME" "$VERSION_CODENAME" "$np_arch" "$np_key" "$np_security" "$VERSION_CODENAME" "$np_arch" "$np_key" > "$2"
    chmod 644 "$2"
  }
  np_apt_options=(-o Dir::Etc::sourceparts=- -o Acquire::Retries=0 -o "Acquire::http::Timeout=$np_timeout" -o "Acquire::https::Timeout=$np_timeout" -o APT::Update::Error-Mode=any)
  np_probe() (
    np_index="$1" np_base="$2"
    np_start="$(date +%s%N)"
    if [[ -n "$np_fetch" ]]; then
      np_url="$np_base/dists/$VERSION_CODENAME/InRelease"
      if [[ "$np_fetch" == curl ]]; then
        curl -fsSL --proto '=https' --proto-redir '=https' --max-time "$np_timeout" --max-filesize 1048576 "$np_url" -o "$np_dir/$np_index.release" || return 1
      else
        timeout "${np_timeout}s" wget -q --https-only --timeout="$np_timeout" --tries=1 -O "$np_dir/$np_index.release" "$np_url" || return 1
      fi
      grep -q '^-----BEGIN PGP SIGNED MESSAGE-----' "$np_dir/$np_index.release" || return 1
      grep -q "^Codename: $VERSION_CODENAME$" "$np_dir/$np_index.release" || return 1
    else
      np_base="${np_base/https:/http:}"
      mkdir -p "$np_dir/$np_index.lists/partial"
      chmod 755 "$np_dir/$np_index.lists" "$np_dir/$np_index.lists/partial"
      np_write_sources "$np_base" "$np_dir/$np_index.sources"
      apt-get "${np_apt_options[@]}" -o "Dir::Etc::sourcelist=$np_dir/$np_index.sources" -o "Dir::State::lists=$np_dir/$np_index.lists" update > "$np_dir/$np_index.log" 2>&1 || return 1
    fi
    np_elapsed=$(( ($(date +%s%N) - np_start) / 1000000 ))
    printf '%s\t%s\n' "$np_elapsed" "$np_base" > "$np_dir/$np_index.rank"
    np_ui_note ' OK ' "$np_base · 采样 $np_elapsed 毫秒" '1;94'
  )
  np_jobs=()
  for np_index in "${!np_bases[@]}"; do
    np_probe "$np_index" "${np_bases[$np_index]}" &
    np_jobs+=("$!")
  done
  for np_job in "${np_jobs[@]}"; do wait "$np_job" || true; done
  shopt -s nullglob
  np_ranks=("$np_dir/"*.rank)
  (( ${#np_ranks[@]} )) || { printf '%s\n' '没有可用的 Python 引导源，请检查网络、系统时间和 CA 证书。' >&2; return 1; }
  sort -n "${np_ranks[@]}" > "$np_dir/ranked"
  while IFS=$'\t' read -r np_elapsed np_base; do
    printf '\n'
    np_ui_note 执行 "安装 Python · $np_base"
    np_write_sources "$np_base" "$np_dir/install.sources"
    if apt-get "${np_apt_options[@]}" -o "Dir::Etc::sourcelist=$np_dir/install.sources" update && \
       apt-get "${np_apt_options[@]}" -o "Dir::Etc::sourcelist=$np_dir/install.sources" install -y python3 ca-certificates; then
      np_ui_note ' OK ' 'Python 引导完成，继续部署向导。' '1;94'
      return 0
    fi
    np_ui_note 提示 '安装失败，尝试下一个可用源。' 33 >&2
  done < "$np_dir/ranked"
  np_ui_note 失败 '所有 Python 引导源均安装失败。' '1;31' >&2
  return 1
)

if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then
  np_bootstrap_python "$@"
fi
