#!/usr/bin/env bash
# Run from a source checkout: bash scripts/deploy.sh [command] [options]
set -euo pipefail
np_script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
for np_arg in "$@"; do
  if [[ "$np_arg" == --preview ]]; then
    # Preview never bootstraps dependencies or requires Linux/root.
    for np_python in python3 python; do
      if command -v "$np_python" >/dev/null 2>&1 && "$np_python" -c 'import sys; sys.exit(sys.version_info < (3, 9))' >/dev/null 2>&1; then
        exec "$np_python" "$np_script_dir/deploy.py" "$@"
      fi
    done
    printf '%s\n' '预览需要已有 Python 3.9+；不会安装依赖。' >&2
    exit 1
  fi
done
if [[ "$(uname -s)" != Linux ]]; then
  printf '%s\n' '部署脚本仅支持 Linux。请将项目上传到 Linux 服务器后执行。' >&2
  exit 1
fi
if ! command -v python3 >/dev/null 2>&1; then
  for np_arg in "$@"; do
    if [[ "$np_arg" == --password-stdin ]]; then
      printf '%s\n' '请先安装 Python 3.9+，再使用 --password-stdin；密码输入不会交给安装引导。' >&2
      exit 1
    fi
    if [[ "$np_arg" == --check || "$np_arg" == check || "$np_arg" == status || "$np_arg" == logs || "$np_arg" == restart || "$np_arg" == stop || "$np_arg" == admin || "$np_arg" == admin-list || "$np_arg" == admin-reset || "$np_arg" == admin-rename || "$np_arg" == --help || "$np_arg" == -h ]]; then
      printf '%s\n' '缺少 Python 3.9+。检查和管理命令不会安装依赖，请先准备 Python。' >&2
      exit 1
    fi
  done
  bash "$np_script_dir/bootstrap-python.sh" "$@"
fi
exec python3 "$np_script_dir/deploy.py" "$@"
