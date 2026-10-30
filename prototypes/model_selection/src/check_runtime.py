#!/usr/bin/env python3
"""Verify the prototype runtime and optionally load the baseline model."""

from __future__ import annotations

import argparse
import hashlib
import json
import platform
import sys
from pathlib import Path


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for chunk in iter(lambda: handle.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def providers() -> list[str]:
    import onnxruntime as ort

    available = ort.get_available_providers()
    selected = [
        name
        for name in ("CoreMLExecutionProvider", "CPUExecutionProvider")
        if name in available
    ]
    if not selected:
        selected = list(available)
    return selected


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument(
        "--load-model",
        action="store_true",
        help="download/load buffalo_l and report its local model files",
    )
    args = parser.parse_args()

    import insightface
    import onnxruntime as ort

    selected = providers()
    report: dict[str, object] = {
        "python": sys.version,
        "python_version": platform.python_version(),
        "machine": platform.machine(),
        "system": platform.system(),
        "insightface_version": getattr(insightface, "__version__", "unknown"),
        "onnxruntime_version": ort.__version__,
        "available_providers": ort.get_available_providers(),
        "selected_providers": selected,
    }

    if args.load_model:
        from insightface.app import FaceAnalysis

        app = FaceAnalysis(name="buffalo_l", providers=selected)
        app.prepare(ctx_id=0, det_size=(640, 640))
        model_root = Path.home() / ".insightface" / "models" / "buffalo_l"
        files = []
        if model_root.exists():
            for path in sorted(model_root.rglob("*")):
                if path.is_file():
                    files.append(
                        {
                            "path": str(path.relative_to(model_root)),
                            "bytes": path.stat().st_size,
                            "sha256": sha256_file(path),
                        }
                    )
        report["model"] = "buffalo_l"
        report["model_root"] = str(model_root)
        report["model_files"] = files

    print(json.dumps(report, indent=2, sort_keys=True))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
