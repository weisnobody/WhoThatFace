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
import struct
import sys
import uuid

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
        "providers": [],
        "models": [],
        "embedding_dimension": 512,
        "embedding_dtype": "float32",
        "embedding_metric": "cosine",
    }


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
        # Model execution is intentionally isolated behind this response
        # contract. A future implementation loads InsightFace once here and
        # emits zero or more validated face records.
        error(message_id, "model_unavailable", "InsightFace analysis is not configured", True)
        return True
    error(message_id, "unknown_message", "unknown message type", False)
    return True


def main():
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
