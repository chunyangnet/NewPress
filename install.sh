#!/usr/bin/env bash
# Install an immutable prebuilt deployment bundle from either official mirror.
set -euo pipefail
np_source=auto np_ref=v1.0.0-beta.2 np_dir=./newpress np_commit=''
np_args=()
np_usage() {
  printf '%s\n' 'NewPress 预编译部署包安装器（1.0.0-beta.2）' \
    '用法：bash install.sh [--source auto|github|gitee] [--ref 版本标签] [--dir 新目录] [--commit 完整SHA] [-- 部署参数]' \
    '自动选择 Linux amd64 / arm64，下载并校验预编译部署包，无需 Git、Go 或 Node.js。' \
    '需要 Bash、curl、tar 和 sha256sum。目标目录必须不存在；Docker 模式另下载预构建镜像。' \
    '已有部署请使用后台核心更新，或在原部署目录执行 bash scripts/deploy.sh。'
}
while (( $# )); do
  case "$1" in
    --source|--ref|--dir|--commit)
      (( $# >= 2 )) || { printf '缺少参数值：%s\n' "$1" >&2; exit 2; }
      case "$1" in
        --source) np_source="$2" ;; --ref) np_ref="$2" ;;
        --dir) np_dir="$2" ;; --commit) np_commit="$2" ;;
      esac
      shift 2 ;;
    --) shift; np_args=("$@"); break ;;
    --help|-h) np_usage; exit 0 ;;
    *) printf '未知安装器参数：%s；部署参数放在 -- 后。\n' "$1" >&2; exit 2 ;;
  esac
done
[[ "$(uname -s)" == Linux ]] || { printf '%s\n' '请在 Linux 服务器执行安装。' >&2; exit 1; }
case "$(uname -m)" in x86_64|amd64) np_arch=amd64 ;; aarch64|arm64) np_arch=arm64 ;; *) printf '%s\n' '仅支持 Linux amd64 / arm64。' >&2; exit 1 ;; esac
case "$np_source" in auto|github|gitee) ;; *) printf '%s\n' '--source 只接受 auto / github / gitee。' >&2; exit 2 ;; esac
for np_tool in curl tar sha256sum; do command -v "$np_tool" >/dev/null || { printf '缺少 %s，请先通过系统包管理器安装。\n' "$np_tool" >&2; exit 1; }; done
[[ "$np_ref" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z]+([.-][0-9A-Za-z]+)*)?$ ]] || { printf '%s\n' '--ref 需要已发布的版本标签，例如 v1.0.0-beta.2。' >&2; exit 2; }
[[ -z "$np_commit" || "$np_commit" =~ ^[a-f0-9]{40}$ ]] || { printf '%s\n' '--commit 需要完整的 40 位提交 SHA。' >&2; exit 2; }
[[ -n "$np_dir" && ! -e "$np_dir" && ! -L "$np_dir" ]] || { printf '%s\n' '目标目录已存在或为空，不会覆盖。' >&2; exit 1; }
mkdir -p -- "$(dirname -- "$np_dir")"
np_parent="$(cd -- "$(dirname -- "$np_dir")" && pwd -P)"
np_target="$np_parent/$(basename -- "$np_dir")"
np_stage="$(mktemp -d "$np_parent/.newpress-install.XXXXXXXX")"
np_cleanup() { case "${np_stage:-}" in "$np_parent"/.newpress-install.*) rm -rf -- "$np_stage" ;; esac; }
trap np_cleanup EXIT
np_sources=(github gitee)
[[ "$np_source" == auto ]] || np_sources=("$np_source")
np_file="newpress-${np_ref#v}-linux-$np_arch-deploy.tar.gz"
np_selected=''
np_download() {
  curl --proto '=https' --proto-redir '=https' --fail --location --silent --show-error \
    --connect-timeout 15 --max-time 600 --max-filesize 134217728 --retry 2 --output "$2" "$1"
}
for np_candidate in "${np_sources[@]}"; do
  case "$np_candidate" in
    github) np_base="https://github.com/chunyangnet/NewPress/releases/download/$np_ref" ;;
    gitee) np_base="https://gitee.com/chunyangnet_0/newpress/raw/releases/distributions/${np_ref#v}" ;;
  esac
  np_checkout="$np_stage/$np_candidate"
  mkdir -- "$np_checkout"
  printf '从 %s 下载 %s（linux/%s）…\n' "$np_candidate" "$np_ref" "$np_arch"
  if ! np_download "$np_base/$np_file.sha256" "$np_stage/$np_candidate.sha256" || \
     ! np_download "$np_base/$np_file" "$np_stage/$np_candidate.tar.gz"; then
    printf '%s 下载失败，尝试下一个指定源。\n' "$np_candidate" >&2; continue
  fi
  read -r np_hash np_unused < "$np_stage/$np_candidate.sha256" || true
  np_hash="${np_hash%$'\r'}"
  if [[ ! "$np_hash" =~ ^[a-f0-9]{64}$ ]] || ! (cd "$np_stage" && printf '%s  %s.tar.gz\n' "$np_hash" "$np_candidate" | sha256sum --check --status); then
    printf '%s 部署包校验失败，不执行。\n' "$np_candidate" >&2; continue
  fi
  if ! tar -tzf "$np_stage/$np_candidate.tar.gz" | while IFS= read -r np_entry; do
      case "$np_entry" in /*|../*|*/../*|*/..|*\\*) exit 1 ;; esac
    done || ! tar -tvzf "$np_stage/$np_candidate.tar.gz" | awk 'substr($1,1,1)!="-" && substr($1,1,1)!="d" {exit 1}'; then
    printf '%s 归档路径或文件类型无效。\n' "$np_candidate" >&2; continue
  fi
  if ! tar --extract --gzip --file "$np_stage/$np_candidate.tar.gz" --directory "$np_checkout" --no-same-owner --no-same-permissions; then continue; fi
  [[ -f "$np_checkout/bin/newpress" && -f "$np_checkout/deployment.json" && -f "$np_checkout/scripts/deploy.sh" && -f "$np_checkout/compose.yaml" && -f "$np_checkout/RELEASE_COMMIT" ]] || continue
  np_actual="$(cat "$np_checkout/RELEASE_COMMIT")"
  if [[ ! "$np_actual" =~ ^[a-f0-9]{40}$ || -n "$np_commit" && "$np_actual" != "$np_commit" ]]; then
    printf '%s 提交不匹配，不执行该部署包。\n' "$np_candidate" >&2; continue
  fi
  printf '%s\n' "$np_source" > "$np_checkout/INSTALL_SOURCE"
  chmod +x "$np_checkout/bin/newpress"
  np_selected="$np_candidate"
  break
done
[[ -n "$np_selected" ]] || { printf '%s\n' '指定源均未提供可校验的部署包。请确认版本已发布。'; exit 1; }
[[ ! -e "$np_target" && ! -L "$np_target" ]] || { printf '%s\n' '目标目录在下载期间被创建，已停止。' >&2; exit 1; }
mv -T -- "$np_checkout" "$np_target"
printf '部署目录：%s\n来源：%s\n源码提交：%s\n' "$np_target" "$np_selected" "$np_actual"
bash "$np_target/scripts/deploy.sh" "${np_args[@]}"
