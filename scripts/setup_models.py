#!/usr/bin/env python3
"""Download (if needed), verify, and offline-validate an InsightFace model."""
import argparse
import hashlib
import json
import os
from pathlib import Path


def files(root):
    return sorted(path for path in root.rglob("*") if path.is_file() and path.name != "checksums.json")


def checksums(root):
    result = {}
    for path in files(root):
        digest = hashlib.sha256()
        with path.open("rb") as stream:
            for chunk in iter(lambda: stream.read(1024 * 1024), b""):
                digest.update(chunk)
        result[str(path.relative_to(root))] = digest.hexdigest()
    return result


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--model", default="buffalo_l")
    parser.add_argument("--root", required=True)
    parser.add_argument("--offline", action="store_true")
    args = parser.parse_args()
    root = Path(args.root).expanduser().resolve()
    model_dir = root / "models" / args.model
    if not args.offline:
        from insightface.app import FaceAnalysis
        app = FaceAnalysis(name=args.model, root=str(root), providers=["CPUExecutionProvider"])
        app.prepare(ctx_id=0, det_size=(640, 640))
    if not model_dir.is_dir():
        raise SystemExit(f"model directory is missing: {model_dir}")
    actual = checksums(model_dir)
    if not actual:
        raise SystemExit(f"model directory is empty: {model_dir}")
    manifest = model_dir / "checksums.json"
    if manifest.exists():
        expected = json.loads(manifest.read_text())
        if expected != actual:
            raise SystemExit(f"model checksum mismatch: {model_dir}")
    else:
        manifest.write_text(json.dumps(actual, indent=2, sort_keys=True) + "\n")
    print(json.dumps({"model": args.model, "root": str(model_dir), "files": len(actual), "offline": args.offline}))


if __name__ == "__main__":
    main()
