#!/usr/bin/env python3
"""Persistent NDJSON worker boundary for InsightFace analysis.

The worker keeps model loading in one process. The optional InsightFace import
is delayed until startup so protocol and setup diagnostics remain usable when
the model environment is incomplete.
"""

import argparse
import base64
import hashlib
import json
import os
import platform
import sys
from pathlib import Path

PROTOCOL_MAJOR = 1
PROTOCOL_MINOR = 0
RUNTIME = {"model": "buffalo_l", "model_root": "", "providers": ["CPUExecutionProvider"], "checksums": {}, "detector_size": (640, 640), "modules": ["detection", "recognition"], "pipeline_id": "", "max_pixels": 0, "max_width": 0, "max_height": 0}


def send(message):
    sys.stdout.write(json.dumps(message, separators=(",", ":")) + "\n")
    sys.stdout.flush()


def capabilities():
    try:
        import insightface  # type: ignore

        version = getattr(insightface, "__version__", "unknown")
    except ImportError:
        version = "unavailable"
    return {
        "worker_version": "1.0.0",
        "python_version": platform.python_version(),
        "insightface_version": version,
        "providers": RUNTIME["providers"],
        "models": [RUNTIME["model"]],
        "embedding_dimension": 512,
        "embedding_dtype": "float32",
        "embedding_metric": "cosine",
        "model_checksums": RUNTIME["checksums"],
        "modules": RUNTIME["modules"],
        "preprocessing": "pillow-exif-transpose-opencv-bgr-v1",
        "detector_size": detector_size_text(),
        "pipeline_id": RUNTIME["pipeline_id"],
        "supported_commands": ["hello", "analyze"],
        "max_pixels": RUNTIME["max_pixels"],
        "max_width": RUNTIME["max_width"],
        "max_height": RUNTIME["max_height"],
    }


def providers():
    return RUNTIME["providers"]


def model_checksums():
    if RUNTIME["checksums"]:
        return RUNTIME["checksums"]
    model_root = RUNTIME["model_root"]
    model_name = RUNTIME["model"]
    if not model_root:
        return {}
    manifest = Path(model_root) / "models" / model_name / "checksums.json"
    try:
        value = json.loads(manifest.read_text())
        return value if isinstance(value, dict) else {}
    except (OSError, json.JSONDecodeError):
        return {}


def detector_size():
    return RUNTIME["detector_size"]


def detector_size_text():
    width, height = detector_size()
    return f"{width}x{height}"


def error(message_id, code, message, retryable=False, detail=""):
    result = {
        "protocol_version": PROTOCOL_MAJOR,
        "command": "error",
        "id": message_id,
        "ok": False,
        "error": {"code": code, "message": message, "retryable": retryable},
    }
    if detail:
        result["error"]["detail"] = detail
    send(result)


def handle(message):
    message_type = message.get("command")
    message_id = message.get("id", "")
    if message_type == "hello":
        requested_version = int(message.get("protocol_version", PROTOCOL_MAJOR) or 0)
        if requested_version != PROTOCOL_MAJOR:
            error(message_id, "protocol_mismatch", "unsupported protocol major", False)
            return False
        send({
            "protocol_version": PROTOCOL_MAJOR,
            "protocol_major": PROTOCOL_MAJOR,
            "protocol_minor": PROTOCOL_MINOR,
            "command": "hello_ack",
            "id": message_id,
            "ok": True,
            "capabilities": capabilities(),
        })
        return True
    if message_type == "analyze":
        path = message.get("path", "")
        if not path or not os.path.isfile(path):
            error(message_id, "missing_image", "image path does not exist", False)
            return True
        if MODEL is None:
            error(message_id, "model_unavailable", str(MODEL_ERROR or "InsightFace is not configured"), True)
            return True
        try:
            expected_size = int(message.get("expected_size", 0) or 0)
            expected_sha256 = message.get("expected_sha256", "")
            actual_size = os.path.getsize(path)
            if expected_size and actual_size != expected_size:
                error(message_id, "file_changed", "image size changed before analysis", True)
                return True
            if expected_sha256:
                digest = hashlib.sha256()
                with open(path, "rb") as source:
                    for chunk in iter(lambda: source.read(1024 * 1024), b""):
                        digest.update(chunk)
                if digest.hexdigest() != expected_sha256:
                    error(message_id, "file_changed", "image content changed before analysis", True)
                    return True
            import cv2  # type: ignore

            requested_max_pixels = int(message.get("max_pixels", 0) or 0)
            requested_max_width = int(message.get("max_width", 0) or 0)
            requested_max_height = int(message.get("max_height", 0) or 0)
            if RUNTIME["max_pixels"] and requested_max_pixels > RUNTIME["max_pixels"]:
                error(message_id, "resource_limit", "requested pixel limit exceeds configured override ceiling", False, str(RUNTIME["max_pixels"]))
                return True
            if RUNTIME["max_width"] and requested_max_width > RUNTIME["max_width"]:
                error(message_id, "resource_limit", "requested width limit exceeds configured override ceiling", False, str(RUNTIME["max_width"]))
                return True
            if RUNTIME["max_height"] and requested_max_height > RUNTIME["max_height"]:
                error(message_id, "resource_limit", "requested height limit exceeds configured override ceiling", False, str(RUNTIME["max_height"]))
                return True
            max_pixels = requested_max_pixels if "max_pixels" in message else RUNTIME["max_pixels"]
            max_width = requested_max_width if "max_width" in message else RUNTIME["max_width"]
            max_height = requested_max_height if "max_height" in message else RUNTIME["max_height"]
            image = load_oriented_image(path, cv2, max_pixels)
            if image is None:
                error(message_id, "decode_failed", "Pillow could not decode the image", False)
                return True
            source_height, source_width = image.shape[:2]
            if max_pixels and source_width * source_height > max_pixels:
                error(message_id, "resource_limit", "image exceeds configured pixel limit", False, f"{source_width}x{source_height}")
                return True
            if max_width and source_width > max_width:
                error(message_id, "resource_limit", "image exceeds configured width limit", False, str(source_width))
                return True
            if max_height and source_height > max_height:
                error(message_id, "resource_limit", "image exceeds configured height limit", False, str(source_height))
                return True
            detected = ordered_detections(MODEL.get(image))
            faces = []
            for index, face in enumerate(detected):
                embedding = face.embedding.astype("float32", copy=False)
                box = [float(value) for value in face.bbox]
                left, top, right, bottom = [int(value) for value in box]
                left, top = max(0, left), max(0, top)
                right, bottom = min(source_width, right), min(source_height, bottom)
                crop = image[top:bottom, left:right]
                quality = quality_score(crop, cv2)
                faces.append({
                    "face_index": index,
                    "box": {"left": float(left), "top": float(top), "right": float(right), "bottom": float(bottom)},
                    "detection_confidence": float(face.det_score),
                    "quality": quality,
                    "embedding": {"encoding": "base64", "dtype": "float32", "byte_order": "little",
                                  "dimension": int(embedding.shape[0]), "normalized": True,
                                  "data": base64.b64encode(embedding.tobytes()).decode("ascii")},
                })
            if expected_size and os.path.getsize(path) != expected_size:
                error(message_id, "file_changed", "image changed during analysis", True)
                return True
            if expected_sha256:
                digest = hashlib.sha256()
                with open(path, "rb") as source:
                    for chunk in iter(lambda: source.read(1024 * 1024), b""):
                        digest.update(chunk)
                if digest.hexdigest() != expected_sha256:
                    error(message_id, "file_changed", "image content changed during analysis", True)
                    return True
            send({
                "protocol_version": PROTOCOL_MAJOR,
                "protocol_major": PROTOCOL_MAJOR,
                "protocol_minor": PROTOCOL_MINOR,
                "command": "analysis_result",
                "id": message_id,
                "ok": True,
                "model": {"pipeline_id": RUNTIME["pipeline_id"]},
                "faces": faces,
                "image": {"width": source_width, "height": source_height,
                          "inference_width": source_width, "inference_height": source_height},
            })
        except Exception as exc:  # model providers expose varied exception types
            error(message_id, "analysis_failed", str(exc), True)
        return True
    error(message_id, "unknown_message", "unknown command", False)
    return True


def quality_score(crop, cv2):
    if crop is None or crop.size == 0:
        return 0.0
    gray = cv2.cvtColor(crop, cv2.COLOR_BGR2GRAY)
    variance = float(cv2.Laplacian(gray, cv2.CV_64F).var())
    # A bounded sharpness signal; the threshold is deliberately a review
    # metric, not a recognition decision.
    return max(0.0, min(1.0, variance / 500.0))


def ordered_detections(detected):
    """Return detections in stable top-to-bottom, left-to-right order."""
    return sorted(detected, key=lambda face: (float(face.bbox[1]), float(face.bbox[0])))


def load_oriented_image(path, cv2, max_pixels=0):
    """Decode and apply EXIF orientation before inference.

    InsightFace receives the oriented pixel matrix, so returned boxes are in
    the same coordinate system as the stored source dimensions.
    """
    from PIL import Image, ImageOps  # type: ignore
    if max_pixels:
        pillow_limit = max(int(max_pixels * 1.1), max_pixels + 1)
        Image.MAX_IMAGE_PIXELS = pillow_limit
    try:
        from pillow_heif import register_heif_opener  # type: ignore
    except ImportError:
        # Keep non-HEIF worker environments usable for local development and
        # optional format plugins; the default setup installs pillow-heif.
        pass
    else:
        register_heif_opener()
    import numpy as np  # type: ignore

    with Image.open(path) as source:
        oriented = ImageOps.exif_transpose(source).convert("RGB")
        pixels = np.asarray(oriented)
    return cv2.cvtColor(pixels, cv2.COLOR_RGB2BGR)


MODEL = None
MODEL_ERROR = None


def load_model():
    global MODEL, MODEL_ERROR
    try:
        from insightface.app import FaceAnalysis  # type: ignore

        model_kwargs = {"name": RUNTIME["model"], "providers": providers(), "allowed_modules": RUNTIME["modules"]}
        model_root = RUNTIME["model_root"]
        if model_root:
            model_kwargs["root"] = model_root
        MODEL = FaceAnalysis(**model_kwargs)
        MODEL.prepare(ctx_id=0, det_size=detector_size())
    except Exception as exc:  # startup reports structured error on first request
        MODEL_ERROR = exc


def main():
    parser = argparse.ArgumentParser(description="Face-Me-Bro InsightFace NDJSON worker")
    parser.add_argument("--model", required=True)
    parser.add_argument("--model-root", default="")
    parser.add_argument("--providers", required=True)
    parser.add_argument("--model-checksums-json", default="{}")
    parser.add_argument("--detector-config-json", default="{}")
    parser.add_argument("--preprocessing-config-json", default="{}")
    parser.add_argument("--modules", default="detection,recognition")
    parser.add_argument("--pipeline-id", default="")
    parser.add_argument("--max-pixels", type=int, default=0)
    parser.add_argument("--max-width", type=int, default=0)
    parser.add_argument("--max-height", type=int, default=0)
    args = parser.parse_args()
    RUNTIME["model"] = args.model
    RUNTIME["model_root"] = args.model_root
    RUNTIME["providers"] = [value for value in args.providers.split(",") if value]
    RUNTIME["modules"] = [value for value in args.modules.split(",") if value]
    RUNTIME["pipeline_id"] = args.pipeline_id
    try:
        checksums = json.loads(args.model_checksums_json)
        if isinstance(checksums, dict):
            RUNTIME["checksums"] = checksums
        detector = json.loads(args.detector_config_json)
        size = detector.get("size") if isinstance(detector, dict) else None
        if isinstance(size, (list, tuple)) and len(size) == 2:
            RUNTIME["detector_size"] = (int(size[0]), int(size[1]))
        if isinstance(detector, dict):
            for key in ("max_pixels", "max_width", "max_height"):
                if detector.get(key) is not None:
                    RUNTIME[key] = max(0, int(detector[key]))
    except (TypeError, ValueError, json.JSONDecodeError) as exc:
        parser.error(f"invalid pipeline configuration: {exc}")
    RUNTIME["max_pixels"] = max(0, args.max_pixels)
    RUNTIME["max_width"] = max(0, args.max_width)
    RUNTIME["max_height"] = max(0, args.max_height)
    load_model()
    for line in sys.stdin:
        try:
            message = json.loads(line)
            if not handle(message):
                return 2
        except (json.JSONDecodeError, TypeError) as exc:
            error("", "malformed_request", str(exc), False)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
