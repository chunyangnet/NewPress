#!/usr/bin/env bash
# Public source installer. Repository identities are independent of extensions.
set -euo pipefail
np_source=auto np_ref=v1.0.0-beta.1 np_dir=./newpress np_commit=''
np_args=()
np_usage() {
  printf '%s\n' 'NewPress 源码安装器（1.0.0-beta.1）' \
    '用法：bash install.sh [--source auto|github|gitee] [--ref 标签或分支] [--dir 新目录] [--commit 完整SHA] [-- 部署参数]' \
    '默认下载 v1.0.0-beta.1；GitHub 不可用时尝试 Gitee。--source gitee 仅从 Gitee 获取源码。' \
    '需要 Linux、Bash、Git；下载入口需要 curl。目标目录必须不存在，避免覆盖已有站点。' \
    '已有部署请在原项目目录执行 bash scripts/deploy.sh；本安装器不覆盖或更新已有目录。'
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
case "$np_source" in auto|github|gitee) ;; *) printf '%s\n' '--source 只接受 auto / github / gitee。' >&2; exit 2 ;; esac
command -v git >/dev/null || { printf '%s\n' '缺少 Git，请先通过系统包管理器安装 git，再运行本脚本。' >&2; exit 1; }
[[ "$np_ref" != -* ]] && git check-ref-format --allow-onelevel "$np_ref" >/dev/null || { printf '%s\n' '版本标签或分支名无效。' >&2; exit 2; }
[[ -z "$np_commit" || "$np_commit" =~ ^[a-f0-9]{40}$ ]] || { printf '%s\n' '--commit 需要完整的 40 位提交 SHA。' >&2; exit 2; }
[[ -n "$np_dir" && ! -e "$np_dir" && ! -L "$np_dir" ]] || { printf '%s\n' '目标目录已存在或为空；不会覆盖。请指定新的 --dir。' >&2; exit 1; }
mkdir -p -- "$(dirname -- "$np_dir")"
np_parent="$(cd -- "$(dirname -- "$np_dir")" && pwd -P)"
np_target="$np_parent/$(basename -- "$np_dir")"
np_stage="$(mktemp -d "$np_parent/.newpress-install.XXXXXXXX")"
np_cleanup() {
  case "${np_stage:-}" in "$np_parent"/.newpress-install.*) rm -rf -- "$np_stage" ;; esac
}
trap np_cleanup EXIT
np_sources=(github gitee)
[[ "$np_source" == auto ]] || np_sources=("$np_source")
np_selected=''
for np_candidate in "${np_sources[@]}"; do
  case "$np_candidate" in
    github) np_url=https://github.com/chunyangnet/NewPress.git ;;
    gitee) np_url=https://gitee.com/chunyangnet_0/newpress.git ;;
  esac
  np_checkout="$np_stage/$np_candidate"
  printf '从 %s 下载 %s…\n' "$np_candidate" "$np_ref"
  if ! GIT_TERMINAL_PROMPT=0 git -c credential.helper= -c http.lowSpeedLimit=1024 -c http.lowSpeedTime=15 \
      clone --depth 1 --single-branch --branch "$np_ref" -- "$np_url" "$np_checkout"; then
    printf '%s 不可用或版本尚未发布。\n' "$np_candidate" >&2
    continue
  fi
  np_actual="$(git -C "$np_checkout" rev-parse HEAD)"
  if [[ ! "$np_actual" =~ ^[a-f0-9]{40}$ || -n "$np_commit" && "$np_actual" != "$np_commit" ]]; then
    printf '%s 提交不匹配，不执行该源码。\n' "$np_candidate" >&2
    continue
  fi
  [[ -f "$np_checkout/go.mod" && -f "$np_checkout/scripts/deploy.sh" && -f "$np_checkout/compose.yaml" ]] || continue
  np_selected="$np_candidate"
  break
done
[[ -n "$np_selected" ]] || { printf '%s\n' '所有指定源均不可用。请确认仓库已公开，且两个平台都存在该版本标签。' >&2; exit 1; }
[[ ! -e "$np_target" && ! -L "$np_target" ]] || { printf '%s\n' '目标目录在下载期间被创建，已停止。' >&2; exit 1; }
mv -T -- "$np_checkout" "$np_target"
printf '源码目录：%s\n来源：%s\n提交：%s\n' "$np_target" "$np_selected" "$np_actual"
bash "$np_target/scripts/deploy.sh" "${np_args[@]}"
