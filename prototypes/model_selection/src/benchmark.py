#!/usr/bin/env python3
"""Benchmark full and trimmed buffalo_l over a recursive photo directory."""

from __future__ import annotations

import argparse
import base64
import csv
import hashlib
import json
import os
import platform
import resource
import subprocess
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
VARIANTS = ("full", "trimmed")


@dataclass(frozen=True)
class ImageEntry:
    path: Path
    relative_path: str
    path_id: str
    size: int
    content_sha256: str


def now_utc() -> str:
    return datetime.now(timezone.utc).isoformat()


def sha256_bytes(value: bytes) -> str:
    return hashlib.sha256(value).hexdigest()


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for chunk in iter(lambda: handle.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def discover_images(root: Path) -> list[ImageEntry]:
    entries: list[ImageEntry] = []
    for path in sorted(root.rglob("*")):
        if not path.is_file() or path.suffix.lower() not in SUPPORTED_SUFFIXES:
            continue
        relative = path.relative_to(root).as_posix()
        entries.append(
            ImageEntry(
                path=path,
                relative_path=relative,
                path_id=sha256_bytes(relative.encode("utf-8")),
                size=path.stat().st_size,
                content_sha256=sha256_file(path),
            )
        )
    return entries


def dataset_manifest(entries: Iterable[ImageEntry]) -> str:
    value = "\n".join(
        f"{entry.relative_path}\t{entry.size}\t{entry.content_sha256}"
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
    except Exception as exc:  # noqa: BLE001 - benchmark continues per file.
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
        record: dict[str, Any] = {
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


def write_image_results(path: Path, results: list[dict[str, Any]], variant: str) -> None:
    with path.open("w", encoding="utf-8") as handle:
        for result in results:
            result = dict(result)
            result["variant"] = variant
            handle.write(json.dumps(result, sort_keys=True) + "\n")


def write_summary_csv(path: Path, passes: list[dict[str, Any]]) -> None:
    fields = [
        "variant",
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


def load_internal_manifest(root: Path, path: Path) -> list[ImageEntry]:
    values = json.loads(path.read_text())
    return [
        ImageEntry(
            path=root / value["relative_path"],
            relative_path=value["relative_path"],
            path_id=value["path_id"],
            size=value["bytes"],
            content_sha256=value["content_sha256"],
        )
        for value in values
    ]


def run_variant_worker(args: argparse.Namespace) -> int:
    started_at = now_utc()
    script_started = time.perf_counter()
    output = args.output
    output.mkdir(parents=True, exist_ok=True)
    prefix = output / f"variant-{args.variant}"

    try:
        import cv2
        import insightface
        import onnxruntime as ort
        from insightface.app import FaceAnalysis

        entries = load_internal_manifest(args.directory.resolve(), args.manifest)
        selected_providers = choose_providers(ort, args.provider)
        allowed_modules = (
            None if args.variant == "full" else ["detection", "recognition"]
        )
        model_started = time.perf_counter()
        app_kwargs: dict[str, Any] = {
            "name": "buffalo_l",
            "providers": selected_providers,
        }
        if allowed_modules is not None:
            app_kwargs["allowed_modules"] = allowed_modules
        app = FaceAnalysis(**app_kwargs)
        app.prepare(ctx_id=0, det_size=(args.det_size, args.det_size))
        model_load_ms = (time.perf_counter() - model_started) * 1000

        passes: list[dict[str, Any]] = []
        cold_pass = run_pass(app, entries, cv2, args.include_embeddings)
        cold_pass.pop("image_results")
        cold_pass.update(
            {"variant": args.variant, "pass": 0, "kind": "cold_full_directory"}
        )
        passes.append(cold_pass)

        warm_image_results: list[dict[str, Any]] = []
        for pass_number in range(1, args.repetitions + 1):
            result = run_pass(app, entries, cv2, args.include_embeddings)
            image_results = result.pop("image_results")
            result.update(
                {
                    "variant": args.variant,
                    "pass": pass_number,
                    "kind": "warm_full_directory",
                }
            )
            passes.append(result)
            if pass_number == 1:
                warm_image_results = image_results

        if not args.include_paths:
            for result in warm_image_results:
                result.pop("relative_path", None)

        runtime = {
            "status": "ok",
            "started_at": started_at,
            "completed_at": now_utc(),
            "variant": args.variant,
            "allowed_modules": allowed_modules or "default_all",
            "python_version": platform.python_version(),
            "python_full_version": sys.version,
            "machine": platform.machine(),
            "system": platform.system(),
            "release": platform.release(),
            "processor": platform.processor(),
            "cpu_count": os.cpu_count(),
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
            "process_elapsed_ms": round(
                (time.perf_counter() - script_started) * 1000, 3
            ),
        }
        write_json(prefix.with_name(f"{prefix.name}-runtime.json"), runtime)
        write_json(prefix.with_name(f"{prefix.name}-passes.json"), passes)
        write_image_results(
            prefix.with_name(f"{prefix.name}-images.jsonl"),
            warm_image_results,
            args.variant,
        )
        return 0
    except Exception as exc:  # noqa: BLE001 - persist worker failure for report.
        runtime = {
            "status": "error",
            "variant": args.variant,
            "error": f"{type(exc).__name__}: {exc}",
            "started_at": started_at,
            "completed_at": now_utc(),
            "process_elapsed_ms": round(
                (time.perf_counter() - script_started) * 1000, 3
            ),
        }
        write_json(prefix.with_name(f"{prefix.name}-runtime.json"), runtime)
        print(runtime["error"], file=sys.stderr)
        return 1


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        description="Run recursive full and trimmed buffalo_l benchmarks."
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
    parser.add_argument("--include-paths", action="store_true")
    parser.add_argument(
        "--include-embeddings",
        action="store_true",
        help="store first-warm-pass float32 embeddings in images.jsonl",
    )
    parser.add_argument("--_variant-worker", action="store_true", help=argparse.SUPPRESS)
    parser.add_argument("--manifest", type=Path, help=argparse.SUPPRESS)
    parser.add_argument("--variant", choices=VARIANTS, help=argparse.SUPPRESS)
    return parser


def main() -> int:
    parser = build_parser()
    args = parser.parse_args()

    if args._variant_worker:
        if not args.manifest or not args.variant or not args.output:
            raise SystemExit("Internal variant worker arguments are incomplete")
        return run_variant_worker(args)

    if not args.directory.is_dir():
        raise SystemExit(f"Not a directory: {args.directory}")
    if args.repetitions < 1:
        raise SystemExit("--repetitions must be at least 1")

    script_started = time.perf_counter()
    started_at = now_utc()
    root = args.directory.resolve()
    discovery_started = time.perf_counter()
    entries = discover_images(root)
    discovery_and_hash_ms = (time.perf_counter() - discovery_started) * 1000
    if not entries:
        raise SystemExit(f"No supported images found below: {root}")

    timestamp = datetime.now().strftime("%Y%m%d-%H%M%S")
    output = args.output or Path("results") / f"{args.machine_label}-{timestamp}"
    output.mkdir(parents=True, exist_ok=False)

    internal_manifest = output / ".benchmark-manifest.json"
    write_json(
        internal_manifest,
        [
            {
                "path_id": entry.path_id,
                "relative_path": entry.relative_path,
                "bytes": entry.size,
                "content_sha256": entry.content_sha256,
            }
            for entry in entries
        ],
    )

    worker_results: list[tuple[dict[str, Any], list[dict[str, Any]], Path]] = []
    for variant in VARIANTS:
        command = [
            sys.executable,
            str(Path(__file__).resolve()),
            str(root),
            "--_variant-worker",
            "--manifest",
            str(internal_manifest),
            "--variant",
            variant,
            "--output",
            str(output),
            "--provider",
            args.provider,
            "--det-size",
            str(args.det_size),
            "--repetitions",
            str(args.repetitions),
        ]
        if args.include_paths:
            command.append("--include-paths")
        if args.include_embeddings:
            command.append("--include-embeddings")
        completed = subprocess.run(command, check=False)
        runtime_path = output / f"variant-{variant}-runtime.json"
        passes_path = output / f"variant-{variant}-passes.json"
        images_path = output / f"variant-{variant}-images.jsonl"
        if not runtime_path.exists():
            write_json(
                runtime_path,
                {
                    "status": "error",
                    "variant": variant,
                    "error": f"variant process exited with code {completed.returncode}",
                },
            )
        runtime = json.loads(runtime_path.read_text())
        passes = json.loads(passes_path.read_text()) if passes_path.exists() else []
        worker_results.append((runtime, passes, images_path))

    public_dataset = [
        {
            "path_id": entry.path_id,
            "relative_path": entry.relative_path if args.include_paths else None,
            "bytes": entry.size,
            "content_sha256": entry.content_sha256,
        }
        for entry in entries
    ]
    all_passes = [item for _, passes, _ in worker_results for item in passes]
    runtime = {
        "status": "ok"
        if all(item[0].get("status") == "ok" for item in worker_results)
        else "partial",
        "started_at": started_at,
        "completed_at": now_utc(),
        "machine_label": args.machine_label,
        "input_directory": str(root) if args.include_paths else None,
        "image_count": len(entries),
        "dataset_manifest_sha256": dataset_manifest(entries),
        "supported_suffixes": sorted(SUPPORTED_SUFFIXES),
        "discovery_and_content_hash_ms": round(discovery_and_hash_ms, 3),
        "provider_policy": args.provider,
        "det_size": [args.det_size, args.det_size],
        "repetitions": args.repetitions,
        "embeddings_included": args.include_embeddings,
        "variants": [item[0] for item in worker_results],
        "process_elapsed_ms": round(
            (time.perf_counter() - script_started) * 1000, 3
        ),
    }

    write_json(output / "runtime.json", runtime)
    write_json(output / "passes.json", all_passes)
    write_json(output / "dataset.json", public_dataset)
    write_summary_csv(output / "summary.csv", all_passes)
    with (output / "images.jsonl").open("w", encoding="utf-8") as combined:
        for _, _, images_path in worker_results:
            if images_path.exists():
                combined.write(images_path.read_text())

    internal_manifest.unlink(missing_ok=True)
    print(json.dumps({"output": str(output), **runtime}, indent=2, sort_keys=True))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
