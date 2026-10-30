#!/usr/bin/env python3
"""Create a Markdown comparison report from two benchmark result directories."""

from __future__ import annotations

import argparse
import base64
import json
import math
import struct
from datetime import datetime
from pathlib import Path
from typing import Any


def load_json(path: Path, default: Any) -> Any:
    if not path.exists():
        return default
    return json.loads(path.read_text())


def load_images(path: Path) -> dict[str, dict[str, dict[str, Any]]]:
    result: dict[str, dict[str, dict[str, Any]]] = {}
    if not path.exists():
        return result
    for line in path.open(encoding="utf-8"):
        value = json.loads(line)
        variant = value.get("variant", "full")
        result.setdefault(variant, {})[value["path_id"]] = value
    return result


def runtime_variants(runtime: dict[str, Any]) -> dict[str, dict[str, Any]]:
    values = runtime.get("variants")
    if isinstance(values, list):
        return {value.get("variant", "unknown"): value for value in values}
    return {runtime.get("variant", "full"): runtime}


def passes_for_variant(
    passes: list[dict[str, Any]], variant: str
) -> list[dict[str, Any]]:
    return [value for value in passes if value.get("variant", "full") == variant]


def average(values: list[float]) -> float | None:
    return sum(values) / len(values) if values else None


def format_number(value: Any, digits: int = 2) -> str:
    if value is None:
        return "—"
    if isinstance(value, float):
        return f"{value:.{digits}f}"
    return str(value)


def format_bytes(value: Any) -> str:
    if value is None:
        return "—"
    value = float(value)
    for unit in ("B", "KiB", "MiB", "GiB"):
        if value < 1024 or unit == "GiB":
            return f"{value:.2f} {unit}"
        value /= 1024
    return "—"


def format_delta(left: Any, right: Any, bytes_value: bool = False) -> str:
    if not isinstance(left, (int, float)) or not isinstance(right, (int, float)):
        return "—"
    delta = right - left
    if bytes_value:
        sign = "+" if delta >= 0 else "-"
        return sign + format_bytes(abs(delta))
    return f"{delta:+.2f}"


def decode_embedding(value: dict[str, Any]) -> tuple[float, ...] | None:
    encoded = value.get("embedding_base64")
    if not encoded:
        return None
    raw = base64.b64decode(encoded)
    return struct.unpack("<" + "f" * (len(raw) // 4), raw)


def embedding_comparison(
    left: dict[str, dict[str, Any]], right: dict[str, dict[str, Any]]
) -> dict[str, Any]:
    common_images = sorted(set(left) & set(right))
    face_pairs = 0
    exact_vectors = 0
    max_abs = 0.0
    total_abs = 0.0
    component_count = 0
    cosine_values: list[float] = []
    same_face_count_images = 0

    for path_id in common_images:
        left_faces = {face["index"]: face for face in left[path_id].get("faces", [])}
        right_faces = {
            face["index"]: face for face in right[path_id].get("faces", [])
        }
        if left[path_id].get("face_count") == right[path_id].get("face_count"):
            same_face_count_images += 1
        for index in sorted(set(left_faces) & set(right_faces)):
            left_vector = decode_embedding(left_faces[index])
            right_vector = decode_embedding(right_faces[index])
            if left_vector is None or right_vector is None:
                continue
            if len(left_vector) != len(right_vector):
                continue
            left_bytes = base64.b64decode(left_faces[index]["embedding_base64"])
            right_bytes = base64.b64decode(right_faces[index]["embedding_base64"])
            if left_bytes == right_bytes:
                exact_vectors += 1
            differences = [
                abs(left_value - right_value)
                for left_value, right_value in zip(left_vector, right_vector)
            ]
            max_abs = max(max_abs, max(differences))
            total_abs += sum(differences)
            component_count += len(differences)
            left_norm = math.sqrt(sum(value * value for value in left_vector))
            right_norm = math.sqrt(sum(value * value for value in right_vector))
            if left_norm and right_norm:
                cosine_values.append(
                    sum(a * b for a, b in zip(left_vector, right_vector))
                    / (left_norm * right_norm)
                )
            face_pairs += 1

    if not face_pairs:
        return {
            "available": False,
            "common_images": len(common_images),
            "same_face_count_images": same_face_count_images,
        }
    return {
        "available": True,
        "common_images": len(common_images),
        "same_face_count_images": same_face_count_images,
        "embedding_pairs": face_pairs,
        "exact_vectors": exact_vectors,
        "max_abs_difference": max_abs,
        "mean_abs_difference": total_abs / component_count,
        "min_cosine": min(cosine_values),
        "mean_cosine": sum(cosine_values) / len(cosine_values),
    }


def dataset_comparison(
    left: list[dict[str, Any]], right: list[dict[str, Any]]
) -> dict[str, Any]:
    left_by_id = {value["path_id"]: value for value in left}
    right_by_id = {value["path_id"]: value for value in right}
    common = set(left_by_id) & set(right_by_id)
    content_available = all(
        "content_sha256" in left_by_id[path_id]
        and "content_sha256" in right_by_id[path_id]
        for path_id in common
    )
    same_content = (
        sum(
            left_by_id[path_id]["content_sha256"]
            == right_by_id[path_id]["content_sha256"]
            for path_id in common
        )
        if content_available
        else None
    )
    return {
        "left_images": len(left_by_id),
        "right_images": len(right_by_id),
        "common_images": len(common),
        "same_sizes": sum(
            left_by_id[path_id].get("bytes") == right_by_id[path_id].get("bytes")
            for path_id in common
        ),
        "content_hashes_available": content_available,
        "same_content_hashes": same_content,
    }


def benchmark_summary(
    runtime: dict[str, Any], passes: list[dict[str, Any]], variant: str
) -> dict[str, Any]:
    values = passes_for_variant(passes, variant)
    warm = [value for value in values if value.get("kind") == "warm_full_directory"]
    cold = next(
        (value for value in values if value.get("kind") == "cold_full_directory"),
        None,
    )
    return {
        "status": runtime.get("status", "ok"),
        "provider": ", ".join(runtime.get("selected_providers", [])) or "—",
        "allowed_modules": runtime.get("allowed_modules", "—"),
        "model_load_ms": runtime.get("model_load_ms"),
        "peak_rss_bytes": runtime.get("peak_rss_bytes"),
        "cold_ms": cold.get("elapsed_ms") if cold else None,
        "warm_ms": average([value["elapsed_ms"] for value in warm]),
        "warm_images_per_second": average(
            [value["images_per_second"] for value in warm]
        ),
        "warm_faces_per_second": average(
            [value["faces_per_second"] for value in warm]
        ),
        "failures": sum(value.get("failure_count", 0) for value in values),
    }


def markdown_table(headers: list[str], rows: list[list[Any]]) -> str:
    lines = [
        "| " + " | ".join(headers) + " |",
        "| " + " | ".join("---" for _ in headers) + " |",
    ]
    lines.extend("| " + " | ".join(str(value) for value in row) + " |" for row in rows)
    return "\n".join(lines)


def compare(args: argparse.Namespace) -> Path:
    left_dir = args.left.resolve()
    right_dir = args.right.resolve()
    left_runtime = load_json(left_dir / "runtime.json", {})
    right_runtime = load_json(right_dir / "runtime.json", {})
    left_passes = load_json(left_dir / "passes.json", [])
    right_passes = load_json(right_dir / "passes.json", [])
    left_images = load_images(left_dir / "images.jsonl")
    right_images = load_images(right_dir / "images.jsonl")
    left_dataset = load_json(left_dir / "dataset.json", [])
    right_dataset = load_json(right_dir / "dataset.json", [])

    left_label = left_runtime.get("machine_label") or left_dir.name
    right_label = right_runtime.get("machine_label") or right_dir.name

    left_variants = runtime_variants(left_runtime)
    right_variants = runtime_variants(right_runtime)
    variants = sorted(set(left_variants) & set(right_variants))
    dataset = dataset_comparison(left_dataset, right_dataset)
    lines = [
        "# Model Benchmark Comparison",
        "",
        f"Generated: `{datetime.now().isoformat(timespec='seconds')}`",
        "",
        f"- {left_label}: `{left_dir}`",
        f"- {right_label}: `{right_dir}`",
        "",
        "## Dataset",
        "",
        markdown_table(
            ["Metric", left_label, right_label, "Result"],
            [
                ["Images", dataset["left_images"], dataset["right_images"], ""],
                ["Common path IDs", "", "", dataset["common_images"]],
                ["Matching file sizes", "", "", dataset["same_sizes"]],
                [
                    "Content hashes available",
                    "",
                    "",
                    dataset["content_hashes_available"],
                ],
                [
                    "Matching content hashes",
                    "",
                    "",
                    dataset["same_content_hashes"],
                ],
            ],
        ),
        "",
        "## Runtime and performance",
        "",
    ]

    for variant in variants:
        left_summary = benchmark_summary(left_variants[variant], left_passes, variant)
        right_summary = benchmark_summary(right_variants[variant], right_passes, variant)
        lines.extend(
            [
                f"### `{variant}` variant",
                "",
                markdown_table(
                    ["Metric", left_label, right_label, "Difference"],
                    [
                        [
                            "Status",
                            left_summary["status"],
                            right_summary["status"],
                            "",
                        ],
                        [
                            "Provider",
                            left_summary["provider"],
                            right_summary["provider"],
                            "",
                        ],
                        [
                            "Allowed modules",
                            left_summary["allowed_modules"],
                            right_summary["allowed_modules"],
                            "",
                        ],
                        [
                            "Model load",
                            f"{format_number(left_summary['model_load_ms'])} ms",
                            f"{format_number(right_summary['model_load_ms'])} ms",
                            f"{format_delta(left_summary['model_load_ms'], right_summary['model_load_ms'])} ms",
                        ],
                        [
                            "Peak RSS",
                            format_bytes(left_summary["peak_rss_bytes"]),
                            format_bytes(right_summary["peak_rss_bytes"]),
                            format_delta(
                                left_summary["peak_rss_bytes"],
                                right_summary["peak_rss_bytes"],
                                bytes_value=True,
                            ),
                        ],
                        [
                            "Cold pass",
                            f"{format_number(left_summary['cold_ms'])} ms",
                            f"{format_number(right_summary['cold_ms'])} ms",
                            f"{format_delta(left_summary['cold_ms'], right_summary['cold_ms'])} ms",
                        ],
                        [
                            "Warm pass average",
                            f"{format_number(left_summary['warm_ms'])} ms",
                            f"{format_number(right_summary['warm_ms'])} ms",
                            f"{format_delta(left_summary['warm_ms'], right_summary['warm_ms'])} ms",
                        ],
                        [
                            "Warm images/sec",
                            format_number(left_summary["warm_images_per_second"]),
                            format_number(right_summary["warm_images_per_second"]),
                            format_delta(
                                left_summary["warm_images_per_second"],
                                right_summary["warm_images_per_second"],
                            ),
                        ],
                        [
                            "Warm faces/sec",
                            format_number(left_summary["warm_faces_per_second"]),
                            format_number(right_summary["warm_faces_per_second"]),
                            format_delta(
                                left_summary["warm_faces_per_second"],
                                right_summary["warm_faces_per_second"],
                            ),
                        ],
                        [
                            "Failures",
                            left_summary["failures"],
                            right_summary["failures"],
                            format_delta(left_summary["failures"], right_summary["failures"]),
                        ],
                    ],
                ),
                "",
            ]
        )

        left_variant_images = left_images.get(variant, {})
        right_variant_images = right_images.get(variant, {})
        embeddings = embedding_comparison(left_variant_images, right_variant_images)
        lines.extend(
            [f"#### `{variant}` embedding consistency", ""]
        )
        if embeddings["available"]:
            lines.extend(
                [
                    markdown_table(
                        ["Metric", "Value"],
                        [
                            ["Common images", embeddings["common_images"]],
                            ["Same face-count images", embeddings["same_face_count_images"]],
                            ["Embedding pairs", embeddings["embedding_pairs"]],
                            ["Exact vector matches", embeddings["exact_vectors"]],
                            ["Maximum absolute difference", f"{embeddings['max_abs_difference']:.8g}"],
                            ["Mean absolute difference", f"{embeddings['mean_abs_difference']:.8g}"],
                            ["Minimum cosine similarity", f"{embeddings['min_cosine']:.15f}"],
                            ["Mean cosine similarity", f"{embeddings['mean_cosine']:.15f}"],
                        ],
                    ),
                    "",
                ]
            )
        else:
            lines.extend(["Embedding vectors were not available in both result sets.", ""])

    lines.extend(
        [
            "## Notes",
            "",
            "- Peak RSS is reported per model variant because each variant runs in a separate process.",
            "- Embedding comparison uses the first warm pass and raw float32 vectors when available.",
            "- Content-hash comparison requires results generated by the updated benchmark script.",
            "- A matching content hash is the strongest indication that both machines processed the same bytes.",
            "",
        ]
    )

    output_dir = args.output_dir or left_dir.parent
    output_dir.mkdir(parents=True, exist_ok=True)
    output = args.output or (
        output_dir / f"comparison-{datetime.now().strftime('%Y%m%d_%H%M%S')}.md"
    )
    output.write_text("\n".join(lines) + "\n")
    return output


def main() -> int:
    parser = argparse.ArgumentParser(description="Compare two benchmark result directories.")
    parser.add_argument("left", type=Path)
    parser.add_argument("right", type=Path)
    parser.add_argument("--output-dir", type=Path)
    parser.add_argument("--output", type=Path)
    args = parser.parse_args()
    if not args.left.is_dir() or not args.right.is_dir():
        raise SystemExit("Both result arguments must be directories")
    output = compare(args)
    print(output)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
