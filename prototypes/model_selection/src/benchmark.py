#!/usr/bin/env python3
"""Benchmark buffalo_l over every supported image below a directory."""

from __future__ import annotations

import argparse
import base64
import csv
import hashlib
import json
import platform
import resource
import sys
import time
from dataclasses import dataclass
from datetime import datetime, timezone
from pathlib import Path
from typing import Any, Iterable


SUPPORTED_SUFFIXES = {
    ".jpg",
    ".jpeg",
    ".png",
    ".heic",
    ".heif",
    ".tif",
    ".tiff",
    ".webp",
}


@dataclass(frozen=True)
class ImageEntry:
    path: Path
    relative_path: str
    path_id: str
    size: int
    mtime_ns: int


def now_utc() -> str:
    return datetime.now(timezone.utc).isoformat()


def sha256_bytes(value: bytes) -> str:
    return hashlib.sha256(value).hexdigest()


def discover_images(root: Path) -> list[ImageEntry]:
    entries: list[ImageEntry] = []
    for path in sorted(root.rglob("*")):
        if not path.is_file() or path.suffix.lower() not in SUPPORTED_SUFFIXES:
            continue
        relative = path.relative_to(root).as_posix()
        stat = path.stat()
        entries.append(
            ImageEntry(
                path=path,
                relative_path=relative,
                path_id=sha256_bytes(relative.encode("utf-8")),
                size=stat.st_size,
                mtime_ns=stat.st_mtime_ns,
            )
        )
    return entries


def dataset_manifest(entries: Iterable[ImageEntry]) -> str:
    value = "\n".join(
        f"{entry.relative_path}\t{entry.size}\t{entry.mtime_ns}"
        for entry in entries
    ).encode("utf-8")
    return sha256_bytes(value)


def peak_rss_bytes() -> int:
    value = resource.getrusage(resource.RUSAGE_SELF).ru_maxrss
    # macOS reports bytes; Linux reports KiB.
    if platform.system() == "Darwin":
        return int(value)
    return int(value * 1024)


def choose_providers(ort: Any, requested: str) -> list[str]:
    available = list(ort.get_available_providers())
    if requested == "cpu":
        if "CPUExecutionProvider" not in available:
            raise RuntimeError("CPUExecutionProvider is not available")
        return ["CPUExecutionProvider"]
    if requested == "coreml":
        if "CoreMLExecutionProvider" not in available:
            raise RuntimeError(
                "CoreMLExecutionProvider is not available in this environment"
            )
        return ["CoreMLExecutionProvider", "CPUExecutionProvider"]

    selected = [
        provider
        for provider in ("CoreMLExecutionProvider", "CPUExecutionProvider")
        if provider in available
    ]
    if not selected:
        raise RuntimeError(f"No supported execution provider found: {available}")
    return selected


def analyze_image(
    app: Any, entry: ImageEntry, cv2: Any, include_embeddings: bool
) -> dict[str, Any]:
    started = time.perf_counter()
    image = cv2.imread(str(entry.path), cv2.IMREAD_COLOR)
    decode_ms = (time.perf_counter() - started) * 1000
    if image is None:
        return {
            "path_id": entry.path_id,
            "relative_path": entry.relative_path,
            "status": "error",
            "error_type": "decode_error",
            "error": "OpenCV could not decode the image",
            "decode_ms": round(decode_ms, 3),
        }

    started = time.perf_counter()
    try:
        faces = app.get(image)
    except Exception as exc:  # noqa: BLE001 - benchmark must continue per file.
        return {
            "path_id": entry.path_id,
            "relative_path": entry.relative_path,
            "status": "error",
            "error_type": "inference_error",
            "error": f"{type(exc).__name__}: {exc}",
            "width": int(image.shape[1]),
            "height": int(image.shape[0]),
            "decode_ms": round(decode_ms, 3),
            "inference_ms": round((time.perf_counter() - started) * 1000, 3),
        }

    inference_ms = (time.perf_counter() - started) * 1000
    face_records = []
    for index, face in enumerate(faces):
        embedding = getattr(face, "embedding", None)
        record = {
                "index": index,
                "box": [round(float(value), 3) for value in face.bbox],
                "detection_confidence": round(float(face.det_score), 6),
                "embedding_dimension": int(len(embedding))
                if embedding is not None
                else None,
                "embedding_norm": round(float((embedding @ embedding) ** 0.5), 6)
                if embedding is not None
                else None,
            }
        if include_embeddings and embedding is not None:
            vector = embedding.astype("float32", copy=False)
            record["embedding_dtype"] = "float32"
            record["embedding_encoding"] = "base64_little_endian"
            record["embedding_base64"] = base64.b64encode(
                vector.tobytes(order="C")
            ).decode("ascii")
        face_records.append(record)

    return {
        "path_id": entry.path_id,
        "relative_path": entry.relative_path,
        "status": "ok",
        "width": int(image.shape[1]),
        "height": int(image.shape[0]),
        "face_count": len(face_records),
        "faces": face_records,
        "decode_ms": round(decode_ms, 3),
        "inference_ms": round(inference_ms, 3),
        "total_ms": round(decode_ms + inference_ms, 3),
    }


def run_pass(
    app: Any, entries: list[ImageEntry], cv2: Any, include_embeddings: bool
) -> dict[str, Any]:
    started = time.perf_counter()
    image_results = [
        analyze_image(app, entry, cv2, include_embeddings) for entry in entries
    ]
    elapsed_ms = (time.perf_counter() - started) * 1000
    successes = [item for item in image_results if item["status"] == "ok"]
    failures = [item for item in image_results if item["status"] != "ok"]
    face_count = sum(int(item.get("face_count", 0)) for item in successes)
    return {
        "image_results": image_results,
        "elapsed_ms": round(elapsed_ms, 3),
        "image_count": len(entries),
        "success_count": len(successes),
        "failure_count": len(failures),
        "face_count": face_count,
        "images_per_second": round(len(entries) / (elapsed_ms / 1000), 6)
        if elapsed_ms
        else None,
        "faces_per_second": round(face_count / (elapsed_ms / 1000), 6)
        if elapsed_ms
        else None,
    }


def write_json(path: Path, value: Any) -> None:
    path.write_text(json.dumps(value, indent=2, sort_keys=True) + "\n")


def write_image_results(path: Path, results: list[dict[str, Any]]) -> None:
    with path.open("w", encoding="utf-8") as handle:
        for result in results:
            handle.write(json.dumps(result, sort_keys=True) + "\n")


def write_summary_csv(path: Path, passes: list[dict[str, Any]]) -> None:
    fields = [
        "pass",
        "kind",
        "elapsed_ms",
        "image_count",
        "success_count",
        "failure_count",
        "face_count",
        "images_per_second",
        "faces_per_second",
    ]
    with path.open("w", newline="", encoding="utf-8") as handle:
        writer = csv.DictWriter(handle, fieldnames=fields)
        writer.writeheader()
        for item in passes:
            writer.writerow({field: item.get(field) for field in fields})


def model_files() -> list[dict[str, Any]]:
    root = Path.home() / ".insightface" / "models" / "buffalo_l"
    files = []
    if root.exists():
        for path in sorted(root.rglob("*")):
            if path.is_file():
                files.append(
                    {
                        "path": str(path.relative_to(root)),
                        "bytes": path.stat().st_size,
                        "sha256": sha256_file(path),
                    }
                )
    return files


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for chunk in iter(lambda: handle.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def main() -> int:
    parser = argparse.ArgumentParser(
        description="Run a recursive buffalo_l benchmark over a photo directory."
    )
    parser.add_argument("directory", type=Path)
    parser.add_argument(
        "--output",
        type=Path,
        help="result directory; defaults to results/<machine>-<timestamp>",
    )
    parser.add_argument("--machine-label", default=platform.machine())
    parser.add_argument(
        "--provider",
        choices=("auto", "coreml", "cpu"),
        default="auto",
        help="execution provider policy; default prefers CoreML then CPU",
    )
    parser.add_argument("--det-size", type=int, default=640)
    parser.add_argument(
        "--repetitions",
        type=int,
        default=3,
        help="number of warm full-directory passes after the cold pass",
    )
    parser.add_argument(
        "--include-paths",
        action="store_true",
        help="include relative paths in results; otherwise only path IDs are stored",
    )
    parser.add_argument(
        "--include-embeddings",
        action="store_true",
        help="store first-warm-pass float32 embeddings as base64 in images.jsonl",
    )
    args = parser.parse_args()

    if not args.directory.is_dir():
        raise SystemExit(f"Not a directory: {args.directory}")
    if args.repetitions < 1:
        raise SystemExit("--repetitions must be at least 1")

    script_started = time.perf_counter()
    started_at = now_utc()
    root = args.directory.resolve()
    entries = discover_images(root)
    if not entries:
        raise SystemExit(f"No supported images found below: {root}")

    timestamp = datetime.now().strftime("%Y%m%d-%H%M%S")
    output = args.output or Path("results") / f"{args.machine_label}-{timestamp}"
    output.mkdir(parents=True, exist_ok=False)

    import cv2
    import insightface
    import onnxruntime as ort

    selected_providers = choose_providers(ort, args.provider)
    model_started = time.perf_counter()
    from insightface.app import FaceAnalysis

    app = FaceAnalysis(name="buffalo_l", providers=selected_providers)
    app.prepare(ctx_id=0, det_size=(args.det_size, args.det_size))
    model_load_ms = (time.perf_counter() - model_started) * 1000

    passes: list[dict[str, Any]] = []
    cold_started = time.perf_counter()
    cold_pass = run_pass(app, entries, cv2, args.include_embeddings)
    cold_image_results = cold_pass.pop("image_results")
    cold_pass["pass"] = 0
    cold_pass["kind"] = "cold_full_directory"
    cold_pass["elapsed_including_pass_setup_ms"] = round(
        (time.perf_counter() - cold_started) * 1000, 3
    )
    passes.append(cold_pass)

    warm_image_results: list[dict[str, Any]] = []
    for pass_number in range(1, args.repetitions + 1):
        result = run_pass(app, entries, cv2, args.include_embeddings)
        result["pass"] = pass_number
        result["kind"] = "warm_full_directory"
        image_results = result.pop("image_results")
        passes.append(result)
        if pass_number == 1:
            warm_image_results = image_results

    if not args.include_paths:
        for result in warm_image_results:
            result.pop("relative_path", None)

    relative_results = []
    for entry in entries:
        relative_results.append(
            {
                "path_id": entry.path_id,
                "relative_path": entry.relative_path if args.include_paths else None,
                "bytes": entry.size,
                "mtime_ns": entry.mtime_ns,
            }
        )

    run_metadata = {
        "started_at": started_at,
        "machine_label": args.machine_label,
        "input_directory": str(root) if args.include_paths else None,
        "image_count": len(entries),
        "dataset_manifest_sha256": dataset_manifest(entries),
        "supported_suffixes": sorted(SUPPORTED_SUFFIXES),
        "python_version": platform.python_version(),
        "python_full_version": sys.version,
        "machine": platform.machine(),
        "system": platform.system(),
        "release": platform.release(),
        "processor": platform.processor(),
        "cpu_count": __import__("os").cpu_count(),
        "insightface_version": getattr(insightface, "__version__", "unknown"),
        "onnxruntime_version": ort.__version__,
        "available_providers": ort.get_available_providers(),
        "selected_providers": selected_providers,
        "model": "buffalo_l",
        "model_files": model_files(),
        "det_size": [args.det_size, args.det_size],
        "repetitions": args.repetitions,
        "embeddings_included": args.include_embeddings,
        "embedding_storage": "base64_little_endian_float32_in_images_jsonl"
        if args.include_embeddings
        else None,
        "model_load_ms": round(model_load_ms, 3),
        "peak_rss_bytes": peak_rss_bytes(),
        "process_elapsed_ms": round((time.perf_counter() - script_started) * 1000, 3),
    }

    write_json(output / "runtime.json", run_metadata)
    write_json(output / "passes.json", passes)
    write_json(output / "dataset.json", relative_results)
    write_image_results(output / "images.jsonl", warm_image_results)
    write_summary_csv(output / "summary.csv", passes)

    print(json.dumps({"output": str(output), **run_metadata}, indent=2, sort_keys=True))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
