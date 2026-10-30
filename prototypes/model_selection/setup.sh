#!/usr/bin/env bash
set -euo pipefail

prototype_root="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
venv_dir="${prototype_root}/.venv"
python_bin="${PYTHON_BIN:-python3.11}"

if ! command -v "${python_bin}" >/dev/null 2>&1; then
  echo "Required Python interpreter not found: ${python_bin}" >&2
  echo "Install Python 3.11, or set PYTHON_BIN to a Python 3.11 executable." >&2
  exit 1
fi

python_version="$(${python_bin} -c 'import sys; print(f"{sys.version_info.major}.{sys.version_info.minor}")')"
if [[ "${python_version}" != "3.11" ]]; then
  echo "Python 3.11 is required; found ${python_version}." >&2
  exit 1
fi

machine="$(${python_bin} -c 'import platform; print(platform.machine())')"
if [[ "${machine}" != "arm64" && "${machine}" != "aarch64" ]]; then
  echo "Warning: expected Apple Silicon arm64; found ${machine}." >&2
fi

if [[ ! -d "${venv_dir}" ]]; then
  "${python_bin}" -m venv "${venv_dir}"
fi

# shellcheck disable=SC1091
source "${venv_dir}/bin/activate"
python -m pip install --upgrade pip
python -m pip install --requirement "${prototype_root}/requirements.txt"

python "${prototype_root}/src/check_runtime.py" --load-model

python -m pip freeze > "${venv_dir}/requirements.lock.txt"
echo
echo "Prototype setup complete. Activate it with:"
echo "  source ${venv_dir}/bin/activate"
