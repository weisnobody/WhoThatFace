#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
venv_dir="${FACE_ME_BRO_VENV:-${repo_root}/.venv}"
python_bin="${PYTHON_BIN:-python3.11}"
model_root="${FACE_ME_BRO_MODEL_ROOT:-${repo_root}/runtime/models}"
model_name="${FACE_ME_BRO_MODEL:-buffalo_l}"
clean=0
if [[ "${1:-}" == "--clean" ]]; then clean=1; fi

if ! command -v "${python_bin}" >/dev/null 2>&1; then
  echo "Python 3.11 is required: ${python_bin}" >&2; exit 1
fi
if [[ "$(${python_bin} -c 'import sys; print(f"{sys.version_info.major}.{sys.version_info.minor}")')" != "3.11" ]]; then
  echo "Python 3.11 is required." >&2; exit 1
fi
if (( clean )); then rm -rf "${venv_dir}"; fi

tmp_venv="${venv_dir}.installing.$$"
cleanup() { rm -rf "${tmp_venv}"; }
trap cleanup EXIT
if [[ ! -x "${venv_dir}/bin/python" ]]; then
  rm -rf "${tmp_venv}"
  "${python_bin}" -m venv "${tmp_venv}"
  "${tmp_venv}/bin/python" -m pip install --upgrade pip
  "${tmp_venv}/bin/python" -m pip install --requirement "${repo_root}/worker/requirements.lock"
  mv "${tmp_venv}" "${venv_dir}"
else
  "${venv_dir}/bin/python" -m pip install --requirement "${repo_root}/worker/requirements.lock"
fi

mkdir -p "${model_root}"
FACE_ME_BRO_MODEL_ROOT="${model_root}" "${venv_dir}/bin/python" "${repo_root}/scripts/setup_models.py" --model "${model_name}" --root "${model_root}"
"${venv_dir}/bin/python" "${repo_root}/scripts/setup_models.py" --model "${model_name}" --root "${model_root}" --offline
"${venv_dir}/bin/python" -m pip freeze > "${venv_dir}/requirements.lock.txt"
printf '{"python":"%s","model":"%s","model_root":"%s","completed":true}\n' "${python_bin}" "${model_name}" "${model_root}" > "${venv_dir}/setup-state.json"
echo "Worker setup complete. Activate with: source ${venv_dir}/bin/activate"
