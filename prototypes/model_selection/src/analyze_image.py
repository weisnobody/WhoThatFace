#!/usr/bin/env python3
"""Run a single-image buffalo_l smoke test and print JSON metadata."""

from __future__ import annotations

import argparse
import json
from pathlib import Path

import cv2

from check_runtime import providers


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("image", type=Path)
    args = parser.parse_args()

    image = cv2.imread(str(args.image), cv2.IMREAD_COLOR)
    if image is None:
        raise SystemExit(f"Unable to decode image: {args.image}")

    from insightface.app import FaceAnalysis

    app = FaceAnalysis(name="buffalo_l", providers=providers())
    app.prepare(ctx_id=0, det_size=(640, 640))
    faces = app.get(image)

    result = {
        "image": str(args.image),
        "width": int(image.shape[1]),
        "height": int(image.shape[0]),
        "face_count": len(faces),
        "faces": [],
    }
    for index, face in enumerate(faces):
        box = [round(float(value), 3) for value in face.bbox]
        embedding = getattr(face, "embedding", None)
        result["faces"].append(
            {
                "index": index,
                "box": box,
                "detection_confidence": float(face.det_score),
                "embedding_dimension": int(len(embedding)) if embedding is not None else None,
                "embedding_norm": float((embedding @ embedding) ** 0.5)
                if embedding is not None
                else None,
            }
        )

    print(json.dumps(result, indent=2))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
