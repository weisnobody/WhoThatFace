#!/usr/bin/env python3
"""Persistent NDJSON worker boundary for InsightFace analysis.

The worker keeps model loading in one process. The optional InsightFace import
is delayed until startup so protocol and setup diagnostics remain usable when
the model environment is incomplete.
"""

import base64
import json
import os
import platform
import sys

PROTOCOL_MAJOR = 1
PROTOCOL_MINOR = 0


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
        "providers": providers(),
        "models": [os.environ.get("FACE_ME_BRO_MODEL", "buffalo_l")],
        "embedding_dimension": 512,
        "embedding_dtype": "float32",
        "embedding_metric": "cosine",
    }


def providers():
    configured = os.environ.get("FACE_ME_BRO_PROVIDERS", "")
    if configured:
        return [value for value in configured.split(",") if value]
    return ["CPUExecutionProvider"]


def error(message_id, code, message, retryable=False, detail=""):
    result = {
        "type": "error",
        "id": message_id,
        "error": {"code": code, "message": message, "retryable": retryable},
    }
    if detail:
        result["error"]["detail"] = detail
    send(result)


def handle(message):
    message_type = message.get("type")
    message_id = message.get("id", "")
    if message_type == "hello":
        requested_major = message.get("protocol_major", PROTOCOL_MAJOR)
        if requested_major != PROTOCOL_MAJOR:
            error(message_id, "protocol_mismatch", "unsupported protocol major", False)
            return False
        send({
            "type": "hello_ack",
            "id": message_id,
            "protocol_major": PROTOCOL_MAJOR,
            "protocol_minor": PROTOCOL_MINOR,
            "capabilities": capabilities(),
        })
        return True
    if message_type == "analyze":
        path = message.get("image_path", "")
        if not path or not os.path.isfile(path):
            error(message_id, "missing_image", "image path does not exist", False)
            return True
        if MODEL is None:
            error(message_id, "model_unavailable", str(MODEL_ERROR or "InsightFace is not configured"), True)
            return True
        try:
            import cv2  # type: ignore

            image = cv2.imread(path, cv2.IMREAD_COLOR)
            if image is None:
                error(message_id, "decode_failed", "OpenCV could not decode the image", False)
                return True
            source_height, source_width = image.shape[:2]
            max_pixels = int(message.get("max_pixels", 0) or 0)
            if max_pixels and source_width * source_height > max_pixels:
                error(message_id, "resource_limit", "image exceeds configured pixel limit", False, f"{source_width}x{source_height}")
                return True
            detected = MODEL.get(image)
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
                    "box": box,
                    "detection_confidence": float(face.det_score),
                    "quality": quality,
                    "embedding_base64": base64.b64encode(embedding.tobytes()).decode("ascii"),
                    "embedding_dimension": int(embedding.shape[0]),
                    "embedding_dtype": "float32",
                    "normalized": True,
                })
            send({
                "type": "analysis_result",
                "id": message_id,
                "faces": faces,
                "source_width": source_width,
                "source_height": source_height,
                "inference_width": source_width,
                "inference_height": source_height,
            })
        except Exception as exc:  # model providers expose varied exception types
            error(message_id, "analysis_failed", str(exc), True)
        return True
    error(message_id, "unknown_message", "unknown message type", False)
    return True


def quality_score(crop, cv2):
    if crop is None or crop.size == 0:
        return 0.0
    gray = cv2.cvtColor(crop, cv2.COLOR_BGR2GRAY)
    variance = float(cv2.Laplacian(gray, cv2.CV_64F).var())
    # A bounded sharpness signal; the threshold is deliberately a review
    # metric, not a recognition decision.
    return max(0.0, min(1.0, variance / 500.0))


MODEL = None
MODEL_ERROR = None


def load_model():
    global MODEL, MODEL_ERROR
    try:
        from insightface.app import FaceAnalysis  # type: ignore

        MODEL = FaceAnalysis(name=os.environ.get("FACE_ME_BRO_MODEL", "buffalo_l"), providers=providers())
        MODEL.prepare(ctx_id=0, det_size=(640, 640))
    except Exception as exc:  # startup reports structured error on first request
        MODEL_ERROR = exc


def main():
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
