#!/usr/bin/env bash
# Download prebuilt bundles through measured mirrors; trust only official checksums.
set -euo pipefail
np_source=auto np_ref=v1.0.0-beta.2 np_dir=./newpress np_commit=''
np_args=()
np_usage() {
  printf '%s\n' 'NewPress 预编译部署包安装器（1.0.0-beta.2）' \
    '用法：bash install.sh [--source auto|github] [--ref 版本标签] [--dir 新目录] [--commit 完整SHA] [-- 部署参数]' \
    '自动选择 Linux amd64 / arm64，下载并校验预编译部署包，无需 Git、Go 或 Node.js。' \
    'auto 对 GitHub 和多个加速站并行测速，github 仅使用直连；校验值只从官方仓库获取。' \
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
case "$np_source" in auto|github) ;; *) printf '%s\n' '--source 只接受 auto / github；Gitee 大文件需登录，请使用 auto 自动选择 GitHub 加速站。' >&2; exit 2 ;; esac
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
np_file="newpress-${np_ref#v}-linux-$np_arch-deploy.tar.gz"
np_selected=''
np_download() {
  curl --proto '=https' --proto-redir '=https' --fail --location --silent --show-error \
    --connect-timeout 15 --max-time 600 --speed-limit 1024 --speed-time 20 \
    --max-filesize 134217728 --retry 1 --output "$2" "$1"
}
np_hash=''
# Never fetch the checksum through a third-party download proxy.
for np_checksums in \
  'https://gitee.com/chunyangnet_0/newpress/raw/releases' \
  'https://raw.githubusercontent.com/chunyangnet/NewPress/releases'; do
  if curl --proto '=https' --proto-redir '=https' --fail --location --silent --show-error \
    --connect-timeout 5 --max-time 15 --max-filesize 4096 \
    --output "$np_stage/official.sha256" "$np_checksums/distributions/${np_ref#v}/$np_file.sha256"; then
    read -r np_hash np_unused < "$np_stage/official.sha256" || true
    np_hash="${np_hash%$'\r'}"
    [[ "$np_hash" =~ ^[a-f0-9]{64}$ ]] && break
  fi
  np_hash=''
done
[[ "$np_hash" =~ ^[a-f0-9]{64}$ ]] || { printf '%s\n' '无法取得官方校验值，已停止安装。' >&2; exit 1; }
np_origin="https://github.com/chunyangnet/NewPress/releases/download/$np_ref/$np_file"
np_names=(GitHub gh-proxy ghfast ghproxy)
np_prefixes=('' 'https://gh-proxy.com/' 'https://ghfast.top/' 'https://ghproxy.net/')
[[ "$np_source" == auto ]] || { np_names=(GitHub); np_prefixes=(''); }
np_probe() {
  local np_index="$1" np_stats np_code np_speed np_size np_magic
  np_stats=$(curl --proto '=https' --proto-redir '=https' --fail --location --silent \
    --connect-timeout 4 --max-time 10 --max-filesize 1048576 --range 0-262143 \
    --output "$np_stage/sample-$np_index" --write-out '%{http_code} %{speed_download} %{size_download}' \
    "${np_prefixes[$np_index]}$np_origin") || np_stats='0 0 0'
  read -r np_code np_speed np_size <<< "$np_stats"
  np_magic=$(od -An -tx1 -N2 "$np_stage/sample-$np_index" 2>/dev/null | tr -d ' \n') || np_magic=''
  if [[ "$np_code" =~ ^20[06]$ && "$np_size" =~ ^[0-9]+$ && "$np_size" -ge 65536 && "$np_magic" == 1f8b ]]; then
    printf '%s  %s 字节/秒\n' "${np_names[$np_index]}" "$np_speed" >&2
  else
    np_speed=0
    printf '%s  采样不可用，将作为下载备用\n' "${np_names[$np_index]}" >&2
  fi
  printf '%s %s\n' "$np_speed" "$np_index" > "$np_stage/rank-$np_index"
}
printf '%s\n' '在当前服务器并行测试 GitHub 下载源（每源最多 256 KiB，10 秒超时）…'
np_jobs=()
for np_index in "${!np_names[@]}"; do np_probe "$np_index" & np_jobs+=("$!"); done
for np_job in "${np_jobs[@]}"; do wait "$np_job"; done
sort -k1,1nr -k2,2n "$np_stage"/rank-* > "$np_stage/ranked"
while read -r np_speed np_index; do
  np_candidate="${np_names[$np_index]}"
  np_checkout="$np_stage/$np_candidate"
  mkdir -- "$np_checkout"
  printf '从 %s 下载 %s（linux/%s）…\n' "$np_candidate" "$np_ref" "$np_arch"
  if ! np_download "${np_prefixes[$np_index]}$np_origin" "$np_stage/$np_candidate.tar.gz"; then
    printf '%s 下载失败，尝试下一个指定源。\n' "$np_candidate" >&2; continue
  fi
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
done < "$np_stage/ranked"
[[ -n "$np_selected" ]] || { printf '%s\n' '指定源均未提供可校验的部署包。请确认版本已发布。'; exit 1; }
[[ ! -e "$np_target" && ! -L "$np_target" ]] || { printf '%s\n' '目标目录在下载期间被创建，已停止。' >&2; exit 1; }
mv -T -- "$np_checkout" "$np_target"
printf '部署目录：%s\n来源：%s\n源码提交：%s\n' "$np_target" "$np_selected" "$np_actual"
bash "$np_target/scripts/deploy.sh" "${np_args[@]}"
