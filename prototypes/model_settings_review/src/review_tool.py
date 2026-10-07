#!/usr/bin/env python3
"""Local tooling for the simplified face-recognition review protocol."""

from __future__ import annotations

import argparse
import base64
import csv
import hashlib
import html
import http.server
import json
import math
import mimetypes
import os
import platform
import sqlite3
import sys
import threading
import time
import uuid
from collections import Counter
from datetime import datetime, timezone
from pathlib import Path
from typing import Any
from urllib.parse import parse_qs, urlparse

SUPPORTED = {".jpg", ".jpeg", ".png", ".heic", ".heif", ".tif", ".tiff", ".webp"}
SCHEMA_VERSION = 1


def utc_now() -> str:
    return datetime.now(timezone.utc).isoformat()


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def file_creation_date(path: Path) -> str:
    """Return the filesystem creation/birth time as an ISO-8601 value."""
    stat = path.stat()
    timestamp = getattr(stat, "st_birthtime", stat.st_ctime)
    return datetime.fromtimestamp(timestamp, timezone.utc).isoformat()


def path_id(value: str) -> str:
    return hashlib.sha256(value.encode("utf-8")).hexdigest()


def choose_providers(requested: str) -> list[str]:
    import onnxruntime as ort

    available = list(ort.get_available_providers())
    if requested == "cpu":
        return ["CPUExecutionProvider"]
    if requested == "coreml":
        if "CoreMLExecutionProvider" not in available:
            raise RuntimeError(f"CoreMLExecutionProvider unavailable: {available}")
        return ["CoreMLExecutionProvider", "CPUExecutionProvider"]
    selected = [p for p in ("CoreMLExecutionProvider", "CPUExecutionProvider") if p in available]
    if not selected:
        raise RuntimeError(f"No supported execution provider: {available}")
    return selected


def connect(experiment: Path) -> sqlite3.Connection:
    db = sqlite3.connect(experiment / "experiment.sqlite3")
    db.row_factory = sqlite3.Row
    db.execute("PRAGMA foreign_keys = ON")
    return db


def init_db(db: sqlite3.Connection) -> None:
    db.executescript(
        """
        CREATE TABLE IF NOT EXISTS metadata (key TEXT PRIMARY KEY, value TEXT NOT NULL);
        CREATE TABLE IF NOT EXISTS assets (
            id INTEGER PRIMARY KEY, content_sha256 TEXT NOT NULL UNIQUE,
            size INTEGER NOT NULL, created_at TEXT NOT NULL
        );
        CREATE TABLE IF NOT EXISTS images (
            id INTEGER PRIMARY KEY, asset_id INTEGER NOT NULL REFERENCES assets(id),
            root_path TEXT NOT NULL, relative_path TEXT NOT NULL, absolute_path TEXT NOT NULL,
            path_sha256 TEXT NOT NULL UNIQUE, width INTEGER, height INTEGER,
            capture_date TEXT, status TEXT NOT NULL DEFAULT 'pending', error TEXT
        );
        CREATE TABLE IF NOT EXISTS faces (
            id INTEGER PRIMARY KEY, image_id INTEGER NOT NULL REFERENCES images(id),
            face_index INTEGER NOT NULL, x1 REAL NOT NULL, y1 REAL NOT NULL,
            x2 REAL NOT NULL, y2 REAL NOT NULL, detection_confidence REAL,
            quality REAL, embedding BLOB, embedding_dimension INTEGER,
            UNIQUE(image_id, face_index)
        );
        CREATE TABLE IF NOT EXISTS people (
            id INTEGER PRIMARY KEY, label TEXT NOT NULL UNIQUE, created_at TEXT NOT NULL
        );
        CREATE TABLE IF NOT EXISTS labels (
            id INTEGER PRIMARY KEY, face_id INTEGER NOT NULL REFERENCES faces(id),
            role TEXT NOT NULL CHECK(role IN ('reference','evaluation')),
            kind TEXT NOT NULL CHECK(kind IN ('person','unknown','ambiguous','false_detection')),
            person_id INTEGER REFERENCES people(id), reviewer TEXT, created_at TEXT NOT NULL,
            UNIQUE(face_id)
        );
        CREATE TABLE IF NOT EXISTS predictions (
            id INTEGER PRIMARY KEY, face_id INTEGER NOT NULL REFERENCES faces(id),
            person_id INTEGER REFERENCES people(id), person_label TEXT, similarity REAL,
            margin REAL, support_count INTEGER NOT NULL, quality REAL,
            category TEXT NOT NULL, candidates_json TEXT NOT NULL, settings_json TEXT NOT NULL,
            created_at TEXT NOT NULL, UNIQUE(face_id)
        );
        CREATE TABLE IF NOT EXISTS post_reviews (
            id INTEGER PRIMARY KEY, face_id INTEGER NOT NULL REFERENCES faces(id),
            outcome TEXT NOT NULL, note TEXT, created_at TEXT NOT NULL, UNIQUE(face_id)
        );
        CREATE TABLE IF NOT EXISTS reveal_events (
            id INTEGER PRIMARY KEY, created_at TEXT NOT NULL, settings_json TEXT NOT NULL
        );
        CREATE TABLE IF NOT EXISTS missed_faces (
            id INTEGER PRIMARY KEY, image_id INTEGER NOT NULL REFERENCES images(id),
            note TEXT, reviewer TEXT, created_at TEXT NOT NULL
        );
        CREATE INDEX IF NOT EXISTS faces_image_idx ON faces(image_id);
        CREATE INDEX IF NOT EXISTS labels_role_idx ON labels(role);
        """
    )
    db.execute("INSERT OR IGNORE INTO metadata(key,value) VALUES('schema_version',?)", (str(SCHEMA_VERSION),))
    db.commit()


def set_meta(db: sqlite3.Connection, key: str, value: Any) -> None:
    db.execute("INSERT OR REPLACE INTO metadata(key,value) VALUES(?,?)", (key, json.dumps(value)))


def get_meta(db: sqlite3.Connection, key: str, default: Any = None) -> Any:
    row = db.execute("SELECT value FROM metadata WHERE key=?", (key,)).fetchone()
    if not row:
        return default
    try:
        return json.loads(row["value"])
    except json.JSONDecodeError:
        return row["value"]


def discover(root: Path) -> list[Path]:
    return sorted(p for p in root.rglob("*") if p.is_file() and p.suffix.lower() in SUPPORTED)


def image_quality(face: Any, width: int, height: int) -> float:
    box = [float(v) for v in face.bbox]
    area = max(0.0, box[2] - box[0]) * max(0.0, box[3] - box[1])
    relative = min(1.0, math.sqrt(area / max(1, width * height)) * 8.0)
    return round(max(0.0, min(1.0, float(face.det_score) * 0.65 + relative * 0.35)), 6)


def load_model(provider: str, det_size: int) -> Any:
    from insightface.app import FaceAnalysis

    app = FaceAnalysis(name="buffalo_l", providers=choose_providers(provider))
    app.prepare(ctx_id=0, det_size=(det_size, det_size))
    return app


def infer_asset(app: Any, path: Path) -> tuple[int, int, list[dict[str, Any]]]:
    import cv2
    import numpy as np

    image = cv2.imread(str(path), cv2.IMREAD_COLOR)
    if image is None:
        raise ValueError("OpenCV could not decode image")
    height, width = image.shape[:2]
    output: list[dict[str, Any]] = []
    for index, face in enumerate(app.get(image)):
        vector = getattr(face, "embedding", None)
        if vector is None:
            continue
        vector = np.asarray(vector, dtype=np.float32)
        box = [float(v) for v in face.bbox]
        output.append({
            "index": index, "box": box, "det": float(face.det_score),
            "quality": image_quality(face, width, height),
            "embedding": vector.tobytes(order="C"), "dimension": int(vector.size),
        })
    return width, height, output


def import_experiment(args: argparse.Namespace) -> int:
    root = args.directory.resolve()
    experiment = args.experiment.resolve()
    if not root.is_dir():
        raise SystemExit(f"Not a directory: {root}")
    experiment.mkdir(parents=True, exist_ok=True)
    db = connect(experiment)
    init_db(db)
    set_meta(db, "experiment_id", get_meta(db, "experiment_id", str(uuid.uuid4())))
    set_meta(db, "protocol", "identify_model_settings-simplified")
    set_meta(db, "created_at", get_meta(db, "created_at", utc_now()))
    set_meta(db, "dataset_root", path_id(str(root)) if args.omit_paths else str(root))
    set_meta(db, "model", "buffalo_l")
    set_meta(db, "provider", args.provider)
    set_meta(db, "det_size", args.det_size)
    set_meta(db, "platform", {"system": platform.system(), "machine": platform.machine(), "python": sys.version})
    db.commit()

    files = discover(root)
    if not files:
        raise SystemExit("No supported images found")
    app = load_model(args.provider, args.det_size)
    asset_cache: dict[str, tuple[int, int, list[dict[str, Any]]]] = {}
    for number, file_path in enumerate(files, 1):
        relative = file_path.relative_to(root).as_posix()
        digest = sha256_file(file_path)
        size = file_path.stat().st_size
        db.execute("INSERT OR IGNORE INTO assets(content_sha256,size,created_at) VALUES(?,?,?)", (digest, size, utc_now()))
        asset = db.execute("SELECT id FROM assets WHERE content_sha256=?", (digest,)).fetchone()
        path_hash = path_id(str(root) + "\0" + relative)
        db.execute(
            "INSERT OR IGNORE INTO images(asset_id,root_path,relative_path,absolute_path,path_sha256,capture_date,status) VALUES(?,?,?,?,?,?,?)",
            # The review server needs the source path to display the image. The
            # experiment directory is expected to be protected as it contains
            # private dataset metadata.
            (asset["id"], str(root), relative, "" if args.omit_paths else str(file_path), path_hash, file_creation_date(file_path), "pending"),
        )
        image = db.execute("SELECT * FROM images WHERE path_sha256=?", (path_hash,)).fetchone()
        if not image["capture_date"]:
            db.execute("UPDATE images SET capture_date=? WHERE id=?", (file_creation_date(file_path), image["id"]))
            db.commit()
        if image["status"] == "ok":
            continue
        try:
            if digest not in asset_cache:
                width, height, detected = infer_asset(app, file_path)
                asset_cache[digest] = (width, height, detected)
            width, height, detected = asset_cache[digest]
            # Duplicate content receives an independent face record for each path.
            db.execute("UPDATE images SET width=?,height=?,status='ok',error=NULL WHERE id=?", (width, height, image["id"]))
            for record in detected:
                db.execute(
                    "INSERT OR REPLACE INTO faces(image_id,face_index,x1,y1,x2,y2,detection_confidence,quality,embedding,embedding_dimension) VALUES(?,?,?,?,?,?,?,?,?,?)",
                    (image["id"], record["index"], *record["box"], record["det"], record["quality"], record["embedding"], record["dimension"]),
                )
        except Exception as exc:  # noqa: BLE001 - preserve per-image failures.
            db.execute("UPDATE images SET status='error',error=? WHERE id=?", (f"{type(exc).__name__}: {exc}", image["id"]))
        db.commit()
        print(f"[{number}/{len(files)}] {relative}", flush=True)
    manifest = {
        "images": len(files), "assets": db.execute("SELECT COUNT(*) FROM assets").fetchone()[0],
        "faces": db.execute("SELECT COUNT(*) FROM faces").fetchone()[0],
        "completed_at": utc_now(),
    }
    set_meta(db, "manifest", manifest)
    db.commit()
    (experiment / "experiment.json").write_text(json.dumps({"metadata": {r["key"]: get_meta(db, r["key"]) for r in db.execute("SELECT key FROM metadata")}, "manifest": manifest}, indent=2) + "\n")
    print(json.dumps(manifest, indent=2))
    return 0


def backfill_dates(args: argparse.Namespace) -> int:
    experiment = args.experiment.resolve()
    db = connect(experiment)
    init_db(db)
    updated = 0
    missing = 0
    for row in db.execute("SELECT id,absolute_path FROM images WHERE capture_date IS NULL OR capture_date='' ").fetchall():
        path = Path(row["absolute_path"])
        if not path.is_file():
            missing += 1
            continue
        db.execute("UPDATE images SET capture_date=? WHERE id=?", (file_creation_date(path), row["id"]))
        updated += 1
    db.commit()
    db.close()
    print(json.dumps({"updated": updated, "missing_paths": missing}, indent=2))
    return 0


def vector(blob: bytes) -> Any:
    import numpy as np
    return np.frombuffer(blob, dtype="<f4")


def cosine(a: Any, b: Any) -> float:
    denominator = float((a @ a) ** 0.5 * (b @ b) ** 0.5)
    return float(a @ b / denominator) if denominator else 0.0


def reviewed(db: sqlite3.Connection) -> tuple[int, int]:
    total = db.execute("SELECT COUNT(*) FROM faces").fetchone()[0]
    labeled = db.execute("SELECT COUNT(*) FROM labels").fetchone()[0]
    return total, labeled


def generate_predictions(db: sqlite3.Connection, settings: dict[str, Any]) -> dict[str, Any]:
    total, labeled = reviewed(db)
    if total and total != labeled:
        raise ValueError(f"Blind review incomplete: {labeled}/{total} faces labeled")
    refs = db.execute("SELECT f.id,f.embedding,l.person_id,p.label FROM faces f JOIN labels l ON l.face_id=f.id JOIN people p ON p.id=l.person_id WHERE l.role='reference' AND l.kind='person'").fetchall()
    if not refs:
        raise ValueError("At least one reference face is required before reveal")
    by_person: dict[int, list[tuple[Any, str]]] = {}
    for row in refs:
        by_person.setdefault(row["person_id"], []).append((vector(row["embedding"]), row["label"]))
    threshold = float(settings["similarity_threshold"])
    margin_threshold = float(settings["margin_threshold"])
    min_support = int(settings["min_support"])
    quality_threshold = float(settings["quality_threshold"])
    db.execute("DELETE FROM predictions")
    count = 0
    for row in db.execute("SELECT f.* FROM faces f JOIN labels l ON l.face_id=f.id WHERE l.role='evaluation'").fetchall():
        scores = []
        query = vector(row["embedding"])
        for person_id, examples in by_person.items():
            person_scores = sorted((cosine(query, emb) for emb, _ in examples), reverse=True)
            scores.append({"person_id": person_id, "label": examples[0][1], "similarity": person_scores[0], "support_count": len(person_scores)})
        scores.sort(key=lambda item: item["similarity"], reverse=True)
        best = scores[0]
        second = scores[1]["similarity"] if len(scores) > 1 else 0.0
        margin = best["similarity"] - second
        quality = float(row["quality"] or 0.0)
        if quality < quality_threshold or best["similarity"] < threshold or margin < margin_threshold or best["support_count"] < min_support:
            category = "unknown" if best["similarity"] < threshold else "review"
            person_id = None
        else:
            category = "high_confidence"
            person_id = best["person_id"]
        db.execute("INSERT INTO predictions(face_id,person_id,person_label,similarity,margin,support_count,quality,category,candidates_json,settings_json,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)", (row["id"], person_id, best["label"], best["similarity"], margin, best["support_count"], quality, category, json.dumps(scores[:5]), json.dumps(settings), utc_now()))
        count += 1
    db.execute("INSERT INTO reveal_events(created_at,settings_json) VALUES(?,?)", (utc_now(), json.dumps(settings)))
    db.commit()
    return {"predictions": count, "settings": settings}


def default_settings() -> dict[str, Any]:
    return {"similarity_threshold": 0.45, "margin_threshold": 0.05, "min_support": 1, "quality_threshold": 0.25}


def latest_settings(db: sqlite3.Connection) -> dict[str, Any]:
    row = db.execute("SELECT settings_json FROM reveal_events ORDER BY id DESC LIMIT 1").fetchone()
    if not row:
        return default_settings()
    try:
        settings = json.loads(row["settings_json"])
    except (TypeError, json.JSONDecodeError):
        return default_settings()
    return {**default_settings(), **settings}


def parse_range(value: str) -> list[float]:
    parts = [float(part) for part in value.split(":")]
    if len(parts) == 1:
        return parts
    if len(parts) != 3 or parts[2] <= 0:
        raise ValueError(f"Range must be VALUE or START:STOP:STEP: {value}")
    start, stop, step = parts
    values = []
    current = start
    while current <= stop + step / 1000:
        values.append(round(current, 6))
        current += step
    return values


def parse_int_values(value: str) -> list[int]:
    return [int(part) for part in value.split(",") if part.strip()]


def parse_float_values(value: str) -> list[float]:
    return [float(part) for part in value.split(",") if part.strip()]


def provisional_clusters(db: sqlite3.Connection, threshold: float = 0.55) -> list[dict[str, Any]]:
    """Return a simple review aid; this is deliberately not a final clusterer."""
    groups: list[dict[str, Any]] = []
    rows = db.execute("SELECT f.id,f.embedding,i.relative_path,f.face_index FROM faces f JOIN images i ON i.id=f.image_id ORDER BY f.id").fetchall()
    for row in rows:
        current = vector(row["embedding"])
        selected = None
        best_score = threshold
        for group in groups:
            score = cosine(current, group["centroid"])
            if score >= best_score:
                selected, best_score = group, score
        item = {"face_id": row["id"], "photo": row["relative_path"], "face_index": row["face_index"]}
        if selected is None:
            groups.append({"centroid": current.copy(), "items": [item]})
        else:
            selected["items"].append(item)
            selected["centroid"] = (selected["centroid"] * (len(selected["items"]) - 1) + current) / len(selected["items"])
    return [{"id": index + 1, "face_count": len(group["items"]), "faces": group["items"]} for index, group in enumerate(groups)]


def load_grouping_faces(db: sqlite3.Connection) -> list[dict[str, Any]]:
    rows = db.execute(
        """SELECT f.id face_id,f.embedding,f.image_id,f.face_index,f.x1,f.y1,f.x2,f.y2,
                  f.quality,f.detection_confidence,
                  l.kind,l.person_id,p.label person_label,l.role,
                  i.relative_path,i.absolute_path,i.width image_width,i.height image_height
           FROM faces f JOIN labels l ON l.face_id=f.id
           LEFT JOIN people p ON p.id=l.person_id
           JOIN images i ON i.id=f.image_id ORDER BY f.id"""
    ).fetchall()
    if not rows:
        raise ValueError("No labeled faces are available for grouping")
    return [dict(row) for row in rows]


def normalized_embeddings(rows: list[dict[str, Any]]) -> Any:
    import numpy as np

    vectors = np.vstack([np.frombuffer(row["embedding"], dtype="<f4") for row in rows]).astype("<f4", copy=False)
    norms = np.linalg.norm(vectors, axis=1, keepdims=True)
    norms[norms == 0] = 1.0
    return vectors / norms


def exact_top_neighbors(face_ids: list[int], embeddings: Any, max_k: int, batch_size: int = 512) -> tuple[list[list[tuple[int, float]]], int]:
    """Return exact top neighbors without retaining a dense all-pairs matrix."""
    import numpy as np

    count = len(face_ids)
    if count < 2:
        return [[] for _ in face_ids], 0
    max_k = min(max_k, count - 1)
    neighbors: list[list[tuple[int, float]]] = []
    comparisons = 0
    for start in range(0, count, batch_size):
        stop = min(count, start + batch_size)
        scores = embeddings[start:stop] @ embeddings.T
        scores[np.arange(stop - start), np.arange(start, stop)] = -np.inf
        candidate_count = min(max_k, count - 1)
        candidate_indices = np.argpartition(-scores, candidate_count - 1, axis=1)[:, :candidate_count]
        for row_index, candidates in enumerate(candidate_indices):
            source_index = start + row_index
            ordered = sorted(
                ((int(index), float(scores[row_index, index])) for index in candidates),
                key=lambda item: (-item[1], face_ids[item[0]]),
            )
            neighbors.append(ordered)
        comparisons += (stop - start) * (count - 1)
    return neighbors, comparisons


def mutual_knn_groups(face_ids: list[int], neighbors: list[list[tuple[int, float]]], k: int, similarity_threshold: float) -> tuple[list[dict[str, Any]], dict[int, int], int]:
    """Build connected components from mutual top-k relationships."""
    count = len(face_ids)
    k = min(k, max(0, count - 1))
    parent = list(range(count))

    def find(value: int) -> int:
        while parent[value] != value:
            parent[value] = parent[parent[value]]
            value = parent[value]
        return value

    def union(left: int, right: int) -> None:
        left_root, right_root = find(left), find(right)
        if left_root != right_root:
            parent[right_root] = left_root

    directed = [{index for index, score in row[:k] if score >= similarity_threshold} for row in neighbors]
    mutual_edges = 0
    for source, row in enumerate(neighbors):
        for target, score in row[:k]:
            if score >= similarity_threshold and source in directed[target]:
                if source < target:
                    mutual_edges += 1
                union(source, target)

    groups_by_root: dict[int, list[int]] = {}
    for index in range(count):
        groups_by_root.setdefault(find(index), []).append(index)
    ordered_groups = sorted(groups_by_root.values(), key=lambda group: min(face_ids[index] for index in group))
    clusters = [{"cluster_id": cluster_id, "indices": group} for cluster_id, group in enumerate(ordered_groups, start=1)]
    assignments = {face_ids[index]: cluster["cluster_id"] for cluster in clusters for index in cluster["indices"]}
    return clusters, assignments, mutual_edges


def grouping_population_rows(rows: list[dict[str, Any]], reference_people: set[int], population: str) -> list[dict[str, Any]]:
    if population == "all":
        return rows
    if population == "reference":
        return [row for row in rows if row["kind"] == "person" and row["person_id"] in reference_people]
    return [row for row in rows if not (row["kind"] == "person" and row["person_id"] in reference_people)]


def reference_candidates(rows: list[dict[str, Any]], embeddings: Any, maximum: int = 5) -> dict[int, list[dict[str, Any]]]:
    """Return reference-gallery candidates for report display only."""
    references: dict[int, list[tuple[str, Any]]] = {}
    for index, row in enumerate(rows):
        if row["role"] == "reference" and row["kind"] == "person" and row["person_id"] is not None:
            references.setdefault(int(row["person_id"]), []).append((row["person_label"] or str(row["person_id"]), embeddings[index]))
    candidates: dict[int, list[dict[str, Any]]] = {}
    for index, row in enumerate(rows):
        scores = []
        for person_id, examples in references.items():
            label = examples[0][0]
            similarity = max(cosine(embeddings[index], example) for _, example in examples)
            scores.append({"person_id": person_id, "person": label, "similarity": round(similarity, 4), "support": len(examples)})
        scores.sort(key=lambda item: (-item["similarity"], item["person"]))
        candidates[int(row["face_id"])] = scores[:maximum]
    return candidates


def grouping_metrics(rows: list[dict[str, Any]], assignments: dict[int, int], clusters: list[dict[str, Any]], reference_people: set[int], population: str, review_quality_threshold: float = 0.70, review_detection_threshold: float = 0.70) -> dict[str, Any]:
    selected = grouping_population_rows(rows, reference_people, population)
    selected_ids = {row["face_id"] for row in selected}
    selected_clusters = [cluster for cluster in clusters if any(rows[index]["face_id"] in selected_ids for index in cluster["indices"])]
    known = [row for row in selected if row["kind"] == "person" and row["person_id"] is not None]
    cluster_counts: dict[int, Counter[int]] = {}
    for row in known:
        cluster_counts.setdefault(assignments[row["face_id"]], Counter())[int(row["person_id"])] += 1
    purity_correct = sum(max(counts.values()) for counts in cluster_counts.values() if counts)
    true_pairs = predicted_pairs = true_positive = 0
    for index, left in enumerate(known):
        for right in known[index + 1:]:
            same_person = left["person_id"] == right["person_id"]
            same_cluster = assignments[left["face_id"]] == assignments[right["face_id"]]
            true_pairs += int(same_person)
            predicted_pairs += int(same_cluster)
            true_positive += int(same_person and same_cluster)
    people_clusters: dict[int, set[int]] = {}
    for row in known:
        people_clusters.setdefault(int(row["person_id"]), set()).add(assignments[row["face_id"]])
    mixed_clusters = sum(1 for counts in cluster_counts.values() if len(counts) > 1)
    unknown = [row for row in selected if row["kind"] != "person" or row["person_id"] is None]
    all_cluster_people: dict[int, set[int]] = {}
    for row in rows:
        if row["kind"] == "person" and row["person_id"] is not None:
            all_cluster_people.setdefault(assignments[row["face_id"]], set()).add(int(row["person_id"]))
    mixed_cluster_ids = {cluster_id for cluster_id, people in all_cluster_people.items() if len(people) > 1}
    low_quality = [row for row in selected if row.get("quality") is not None and row["quality"] < review_quality_threshold]
    low_detection = [row for row in selected if row.get("detection_confidence") is not None and row["detection_confidence"] < review_detection_threshold]
    low_quality_ids = {row["face_id"] for row in low_quality}
    low_detection_ids = {row["face_id"] for row in low_detection}
    low_signal_ids = {row["face_id"] for row in low_quality} | {row["face_id"] for row in low_detection}
    very_low_signal_ids = low_quality_ids & low_detection_ids
    cluster_sizes = {cluster["cluster_id"]: len(cluster["indices"]) for cluster in clusters}

    def cluster_count(face_ids: set[int]) -> tuple[int, int]:
        cluster_ids = {assignments[face_id] for face_id in face_ids}
        return len(cluster_ids), sum(1 for cluster_id in cluster_ids if cluster_sizes[cluster_id] > 1)

    low_quality_clusters, low_quality_multi_clusters = cluster_count(low_quality_ids)
    low_detection_clusters, low_detection_multi_clusters = cluster_count(low_detection_ids)
    low_signal_clusters, low_signal_multi_clusters = cluster_count(low_signal_ids)
    very_low_signal_clusters, very_low_signal_multi_clusters = cluster_count(very_low_signal_ids)
    low_signal_mixed_ids = {row["face_id"] for row in selected if row["face_id"] in low_signal_ids and assignments[row["face_id"]] in mixed_cluster_ids}
    low_signal_mixed_clusters, low_signal_mixed_multi_clusters = cluster_count(low_signal_mixed_ids)
    return {
        "faces": len(selected),
        "people": len(people_clusters),
        "clusters_with_population": len(selected_clusters),
        "singleton_clusters": sum(1 for cluster in selected_clusters if sum(rows[index]["face_id"] in selected_ids for index in cluster["indices"]) == 1),
        "known_faces": len(known),
        "unknown_faces": len(unknown),
        "purity": round(purity_correct / len(known), 4) if known else None,
        "pair_precision": round(true_positive / predicted_pairs, 4) if predicted_pairs else None,
        "pair_recall": round(true_positive / true_pairs, 4) if true_pairs else None,
        "pair_f1": round(2 * true_positive / (predicted_pairs + true_pairs), 4) if predicted_pairs + true_pairs else None,
        "mixed_person_clusters": mixed_clusters,
        "mixed_person_cluster_rate": round(mixed_clusters / len(cluster_counts), 4) if cluster_counts else 0.0,
        "people_split_across_clusters": sum(1 for group_ids in people_clusters.values() if len(group_ids) > 1),
        "mean_clusters_per_person": round(sum(len(group_ids) for group_ids in people_clusters.values()) / len(people_clusters), 4) if people_clusters else None,
        "estimated_cluster_assignments": len(selected_clusters),
        "low_quality_faces": len(low_quality),
        "low_detection_faces": len(low_detection),
        "low_signal_faces": len(low_signal_ids),
        "very_low_signal_faces": len(very_low_signal_ids),
        "low_signal_mixed_faces": len(low_signal_mixed_ids),
        "low_quality_clusters": low_quality_clusters,
        "low_quality_multi_clusters": low_quality_multi_clusters,
        "low_detection_clusters": low_detection_clusters,
        "low_detection_multi_clusters": low_detection_multi_clusters,
        "low_signal_clusters": low_signal_clusters,
        "low_signal_multi_clusters": low_signal_multi_clusters,
        "very_low_signal_clusters": very_low_signal_clusters,
        "very_low_signal_multi_clusters": very_low_signal_multi_clusters,
        "low_signal_mixed_clusters": low_signal_mixed_clusters,
        "low_signal_mixed_multi_clusters": low_signal_mixed_multi_clusters,
    }


def format_low_signal_metric(metrics: dict[str, Any], name: str) -> str:
    return f"{metrics[name + '_faces']} ({metrics[name + '_clusters']}/{metrics[name + '_multi_clusters']})"


def grouping_memory_mb() -> float | None:
    try:
        import resource
        value = resource.getrusage(resource.RUSAGE_SELF).ru_maxrss
        return round(value / (1024 * 1024) if sys.platform == "darwin" else value / 1024, 2)
    except (ImportError, AttributeError):
        return None


def grouping_report_markdown(summary: dict[str, Any]) -> str:
    lines = ["# Mutual-kNN Face Grouping Benchmark", "", f"Generated: `{summary['generated_at']}`", "", "## Configuration", "", f"- Input faces: {summary['input_faces']}", f"- Similarity threshold: `{summary['similarity_threshold']}`", f"- Review quality threshold: `{summary['review_quality_threshold']}`", f"- Review detection threshold: `{summary['review_detection_threshold']}`", f"- Candidate k values: `{', '.join(str(value) for value in summary['k_values'])}`", f"- Order stable: `{summary['order_stable']}`", "", "## Recommendation", "", f"- Recommended k: `{summary['recommendation']['k']}`", "- Rule: select the highest all-population pair F1, then prefer higher pair precision and fewer low-signal faces.", "", "## Results", "", "| k | Faces | Clusters | Mutual edges | All purity | All pair precision | All pair recall | All pair F1 | Low-signal faces | Low-quality faces | Low-detection faces | Very-low-signal faces | Low-signal mixed faces | Mixed clusters | Cluster > 1 | Cluster > 2 |", "|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|"]
    for result in summary["results"]:
        metrics = result["populations"]["all"]
        lines.append(f"| {result['k']} | {result['face_count']} | {result['cluster_count']} | {result['mutual_edges']} | {metrics['purity']} | {metrics['pair_precision']} | {metrics['pair_recall']} | {metrics['pair_f1']} | {format_low_signal_metric(metrics, 'low_signal')} | {format_low_signal_metric(metrics, 'low_quality')} | {format_low_signal_metric(metrics, 'low_detection')} | {format_low_signal_metric(metrics, 'very_low_signal')} | {format_low_signal_metric(metrics, 'low_signal_mixed')} | {result['mixed_clusters']} | {result['clusters_gt_1']} | {result['clusters_gt_2']} |")
    lines += ["", "## Population comparison", "", "| k | Population | Faces | Clusters | Purity | Pair precision | Pair recall | Pair F1 | Low-signal faces | Low-quality faces | Low-detection faces | Very-low-signal faces | Low-signal mixed faces | Mixed clusters | People split | Mean clusters/person |", "|---:|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|"]
    for result in summary["results"]:
        for population, metrics in result["populations"].items():
            lines.append(f"| {result['k']} | {population} | {metrics['faces']} | {metrics['clusters_with_population']} | {metrics['purity']} | {metrics['pair_precision']} | {metrics['pair_recall']} | {metrics['pair_f1']} | {format_low_signal_metric(metrics, 'low_signal')} | {format_low_signal_metric(metrics, 'low_quality')} | {format_low_signal_metric(metrics, 'low_detection')} | {format_low_signal_metric(metrics, 'very_low_signal')} | {format_low_signal_metric(metrics, 'low_signal_mixed')} | {metrics['mixed_person_clusters']} | {metrics['people_split_across_clusters']} | {metrics['mean_clusters_per_person']} |")
    lines += ["", "## Interpretation", "", "Purity measures the proportion of known-person faces assigned to the dominant person in their cluster. Pair precision measures whether faces grouped together belong to the same person; pair recall measures whether faces belonging to the same person were grouped together. High precision with lower recall indicates clean but fragmented groups.", "", "Low-signal faces have quality or detection confidence below the configured review thresholds. They are review-priority indicators, not proof that a face is incorrectly grouped. Low-signal mixed-cluster faces deserve particular attention.", "", "The recommended k should prioritize high pair precision and a low mixed-cluster rate, then use pair recall and low-signal review effort to break ties.", ""]
    return "\n".join(lines)


def write_cluster_pages(output: Path, cluster_summaries: dict[str, list[dict[str, Any]]]) -> None:
    for k, clusters in cluster_summaries.items():
        directory = output / f"clusters-k{k}"
        directory.mkdir(parents=True, exist_ok=True)
        for cluster in clusters:
            page_name = f"cluster-{cluster['cluster_id']:04d}.html"
            cluster["page"] = f"clusters-k{k}/{page_name}"
            lines = [
                "<!doctype html>",
                '<html lang="en"><head><meta charset="utf-8"><title>Face cluster</title>',
                "<style>body{font-family:system-ui,sans-serif;margin:1rem}.photo{margin:1.5rem 0;border-bottom:1px solid #ccc;padding-bottom:1rem}.frame{position:relative;display:inline-block;max-width:100%}.frame img{display:block;max-width:100%;max-height:75vh}.box{position:absolute;border:3px solid #d00;box-sizing:border-box}.candidate{margin:.25rem 0 0 1rem}</style>",
                "</head><body>",
                f"<h1>Cluster {cluster['cluster_id']} ({cluster['face_count']} faces)</h1>",
                f"<p>k={html.escape(str(k))}; mixed-person cluster: {html.escape(str(cluster['mixed']))}</p>",
            ]
            images: dict[int, list[dict[str, Any]]] = {}
            for face in cluster["faces"]:
                images.setdefault(int(face["image_id"]), []).append(face)
            for faces in images.values():
                first = faces[0]
                image_path = Path(first["absolute_path"])
                try:
                    image_uri = image_path.resolve().as_uri()
                except ValueError:
                    image_uri = image_path.as_posix()
                overlays = []
                for face in faces:
                    width = max(1, float(face["image_width"] or 1))
                    height = max(1, float(face["image_height"] or 1))
                    x1, y1, x2, y2 = (float(face[key]) for key in ("x1", "y1", "x2", "y2"))
                    overlays.append(f'<span class="box" style="left:{x1 / width * 100:.3f}%;top:{y1 / height * 100:.3f}%;width:{(x2-x1) / width * 100:.3f}%;height:{(y2-y1) / height * 100:.3f}%"></span>')
                lines.append(f'<section class="photo"><div class="frame"><img src="{html.escape(image_uri)}" alt="{html.escape(first["relative_path"])}">{"".join(overlays)}</div><p>{html.escape(first["relative_path"])}</p>')
                for face in faces:
                    label = face["person_label"] or face["kind"] or "unknown"
                    candidates = ", ".join(f"{item['person']} {item['similarity']:.4f}" for item in face["candidates"]) or "none available"
                    lines.append(f"<div class=\"candidate\"><b>Face {face['face_index'] + 1}</b>: {html.escape(str(label))}; quality {face['quality']}; detection {face['detection_confidence']}; candidates: {html.escape(candidates)}</div>")
                lines.append("</section>")
            lines += ["</body></html>", ""]
            (directory / page_name).write_text("\n".join(lines))


def grouping_report_html(summary: dict[str, Any], cluster_files: dict[int, str]) -> str:
    sortable = "<script>function sortTable(header){const table=header.closest('table');const rows=Array.from(table.querySelectorAll('tr:nth-child(n+2)'));const index=Array.from(header.parentNode.children).indexOf(header);const numeric=rows.every(row=>!Number.isNaN(Number(row.children[index].textContent.trim())));rows.sort((a,b)=>{const av=a.children[index].textContent.trim(),bv=b.children[index].textContent.trim();return numeric?Number(av)-Number(bv):av.localeCompare(bv)});rows.forEach(row=>table.appendChild(row))}</script>"
    lines = ["<!doctype html>", '<html lang="en"><head><meta charset="utf-8"><title>Mutual-kNN face grouping benchmark</title>', "<style>body{font-family:system-ui,sans-serif;margin:1rem}table{border-collapse:collapse}th,td{border:1px solid #ccc;padding:.35rem}th{background:#f4f4f4;cursor:pointer}details{margin:.5rem 0}code{font-size:.9em}.photo{margin:1rem 0}.frame{position:relative;display:inline-block;max-width:100%}.frame img{display:block;max-width:100%;max-height:70vh}.box{position:absolute;border:3px solid #d00;box-sizing:border-box}.candidate{margin:.25rem 0 0 1rem}</style>", "</head><body>", "<h1>Mutual-kNN face grouping benchmark</h1>", f"<p>{summary['input_faces']} faces; similarity threshold {summary['similarity_threshold']}; review thresholds: quality &lt; {summary['review_quality_threshold']}, detection &lt; {summary['review_detection_threshold']}; order stable: {html.escape(str(summary['order_stable']))}</p>", f"<p><b>Recommended k:</b> {summary['recommendation']['k']} (highest all-population pair F1, with precision and low-signal review effort as tie-breakers)</p>", "<h2>Results</h2>", '<table><tr><th onclick="sortTable(this)">k</th><th onclick="sortTable(this)">Faces</th><th onclick="sortTable(this)">Clusters</th><th onclick="sortTable(this)">Mutual edges</th><th onclick="sortTable(this)">All purity</th><th onclick="sortTable(this)">Pair precision</th><th onclick="sortTable(this)">Pair recall</th><th onclick="sortTable(this)">Pair F1</th><th onclick="sortTable(this)">Low-signal faces</th><th onclick="sortTable(this)">Low-quality faces</th><th onclick="sortTable(this)">Low-detection faces</th><th onclick="sortTable(this)">Very-low-signal faces</th><th onclick="sortTable(this)">Low-signal mixed faces</th><th onclick="sortTable(this)">Mixed clusters</th><th onclick="sortTable(this)">Cluster &gt; 1</th><th onclick="sortTable(this)">Cluster &gt; 2</th></tr>']
    for result in summary["results"]:
        metrics = result["populations"]["all"]
        lines.append(f"<tr><td>{result['k']}</td><td>{result['face_count']}</td><td>{result['cluster_count']}</td><td>{result['mutual_edges']}</td><td>{metrics['purity']}</td><td>{metrics['pair_precision']}</td><td>{metrics['pair_recall']}</td><td>{metrics['pair_f1']}</td><td>{format_low_signal_metric(metrics, 'low_signal')}</td><td>{format_low_signal_metric(metrics, 'low_quality')}</td><td>{format_low_signal_metric(metrics, 'low_detection')}</td><td>{format_low_signal_metric(metrics, 'very_low_signal')}</td><td>{format_low_signal_metric(metrics, 'low_signal_mixed')}</td><td>{result['mixed_clusters']}</td><td>{result['clusters_gt_1']}</td><td>{result['clusters_gt_2']}</td></tr>")
    lines.append('</table><h2>Population comparison</h2><table><tr><th onclick="sortTable(this)">k</th><th onclick="sortTable(this)">Population</th><th onclick="sortTable(this)">Faces</th><th onclick="sortTable(this)">Clusters</th><th onclick="sortTable(this)">Purity</th><th onclick="sortTable(this)">Pair precision</th><th onclick="sortTable(this)">Pair recall</th><th onclick="sortTable(this)">Low-signal faces</th><th onclick="sortTable(this)">Low-quality faces</th><th onclick="sortTable(this)">Low-detection faces</th><th onclick="sortTable(this)">Very-low-signal faces</th><th onclick="sortTable(this)">Low-signal mixed faces</th><th onclick="sortTable(this)">Mixed clusters</th></tr>')
    for result in summary["results"]:
        for population, metrics in result["populations"].items():
            lines.append(f"<tr><td>{result['k']}</td><td>{population}</td><td>{metrics['faces']}</td><td>{metrics['clusters_with_population']}</td><td>{metrics['purity']}</td><td>{metrics['pair_precision']}</td><td>{metrics['pair_recall']}</td><td>{format_low_signal_metric(metrics, 'low_signal')}</td><td>{format_low_signal_metric(metrics, 'low_quality')}</td><td>{format_low_signal_metric(metrics, 'low_detection')}</td><td>{format_low_signal_metric(metrics, 'very_low_signal')}</td><td>{format_low_signal_metric(metrics, 'low_signal_mixed')}</td><td>{metrics['mixed_person_clusters']}</td></tr>")
    lines.append("</table><h2>Cluster summaries</h2>")
    for k, filename in cluster_files.items():
        lines.append(f'<details><summary>k={k} clusters</summary><p><a href="{html.escape(filename)}">Download JSONL</a></p>')
        for cluster in summary["cluster_summaries"][str(k)]:
            people = ", ".join(f"{label} ({count})" for label, count in cluster["people"].items()) or "unknown faces"
            link = cluster.get("page", "")
            lines.append(f"<details><summary>Cluster {cluster['cluster_id']} — {cluster['face_count']} faces — {html.escape(people)}</summary><p><a href=\"{html.escape(link)}\">Open cluster photos</a></p><p>{html.escape(', '.join(cluster['paths']))}</p></details>")
        lines.append("</details>")
    lines += [sortable, "</body></html>", ""]
    return "\n".join(lines)


def group_benchmark_experiment(args: argparse.Namespace) -> int:
    import numpy as np

    experiment = args.experiment.resolve()
    db = connect(experiment)
    init_db(db)
    rows = load_grouping_faces(db)
    rows.sort(key=lambda row: int(row["face_id"]))
    face_ids = [int(row["face_id"]) for row in rows]
    embeddings = normalized_embeddings(rows)
    candidates_by_face = reference_candidates(rows, embeddings)
    k_values = sorted(set(args.k_values))
    if not k_values:
        raise ValueError("At least one k value is required")
    if args.batch_size <= 0:
        raise ValueError("batch size must be positive")
    if any(k <= 0 for k in k_values):
        raise ValueError("k values must be positive")
    max_k = min(max(k_values), len(rows) - 1)
    if max_k < max(k_values):
        raise ValueError(f"The dataset has only {len(rows)} faces; largest k must be less than that")
    reference_people = {int(row["person_id"]) for row in rows if row["role"] == "reference" and row["kind"] == "person" and row["person_id"] is not None}
    search_start = time.perf_counter()
    neighbors, comparisons = exact_top_neighbors(face_ids, embeddings, max_k, args.batch_size)
    search_elapsed = time.perf_counter() - search_start
    results = []
    cluster_summaries: dict[str, list[dict[str, Any]]] = {}
    cluster_files: dict[int, str] = {}
    for k in k_values:
        graph_start = time.perf_counter()
        clusters, assignments, mutual_edges = mutual_knn_groups(face_ids, neighbors, k, args.similarity_threshold)
        graph_elapsed = time.perf_counter() - graph_start
        population_metrics = {population: grouping_metrics(rows, assignments, clusters, reference_people, population, args.review_quality_threshold, args.review_detection_threshold) for population in ("all", "reference", "non_reference")}
        results.append({
            "k": k,
            "face_count": len(rows),
            "cluster_count": len(clusters),
            "mutual_edges": mutual_edges,
            "candidate_neighbor_relationships": len(rows) * k,
            "clusters_gt_1": sum(1 for cluster in clusters if len(cluster["indices"]) > 1),
            "clusters_gt_2": sum(1 for cluster in clusters if len(cluster["indices"]) > 2),
            "mixed_clusters": 0,
            "populations": population_metrics,
            "timings": {"graph_seconds": round(graph_elapsed, 6)},
        })
        summaries = []
        for cluster in clusters:
            members = [rows[index] for index in cluster["indices"]]
            people = Counter(str(row["person_label"] or row["person_id"]) for row in members if row["kind"] == "person" and row["person_id"] is not None)
            face_details = []
            for row in members:
                face_details.append({
                    "face_id": row["face_id"], "image_id": row["image_id"], "face_index": row["face_index"],
                    "relative_path": row["relative_path"], "absolute_path": row["absolute_path"],
                    "image_width": row["image_width"], "image_height": row["image_height"],
                    "x1": row["x1"], "y1": row["y1"], "x2": row["x2"], "y2": row["y2"],
                    "kind": row["kind"], "person_label": row["person_label"], "quality": row["quality"],
                    "detection_confidence": row["detection_confidence"], "candidates": candidates_by_face[row["face_id"]],
                })
            summaries.append({"cluster_id": cluster["cluster_id"], "face_count": len(members), "mixed": len(people) > 1, "people": dict(people), "paths": [row["relative_path"] for row in members], "faces": face_details})
        summaries.sort(key=lambda cluster: (not cluster["mixed"], -cluster["face_count"], cluster["cluster_id"]))
        cluster_summaries[str(k)] = summaries
        results[-1]["mixed_clusters"] = sum(1 for cluster in summaries if cluster["mixed"])
    order_stable = True
    reversed_rows = list(reversed(rows))
    reversed_embeddings = normalized_embeddings(reversed_rows)
    reversed_ids = [int(row["face_id"]) for row in reversed_rows]
    reversed_neighbors, _ = exact_top_neighbors(reversed_ids, reversed_embeddings, max_k, args.batch_size)
    for k in k_values:
        forward_clusters, _, _ = mutual_knn_groups(face_ids, neighbors, k, args.similarity_threshold)
        backward_clusters, _, _ = mutual_knn_groups(reversed_ids, reversed_neighbors, k, args.similarity_threshold)
        forward_sets = {frozenset(face_ids[index] for index in cluster["indices"]) for cluster in forward_clusters}
        backward_sets = {frozenset(reversed_ids[index] for index in cluster["indices"]) for cluster in backward_clusters}
        order_stable = order_stable and forward_sets == backward_sets
    recommended = max(
        results,
        key=lambda result: (
            result["populations"]["all"]["pair_f1"] or 0.0,
            result["populations"]["all"]["pair_precision"] or 0.0,
            -result["populations"]["all"]["estimated_cluster_assignments"],
        ),
    )
    stamp = datetime.now().strftime("%Y%m%d_%H%M%S")
    output = experiment / f"grouping-small-{stamp}"
    output.mkdir(parents=True, exist_ok=True)
    write_cluster_pages(output, cluster_summaries)
    for result in results:
        k = result["k"]
        filename = f"clusters-k{k}.jsonl"
        cluster_files[k] = filename
        with (output / filename).open("w") as stream:
            for cluster in cluster_summaries[str(k)]:
                stream.write(json.dumps(cluster) + "\n")
    summary = {"generated_at": utc_now(), "experiment": get_meta(db, "experiment_id"), "input_faces": len(rows), "reference_people": len(reference_people), "k_values": k_values, "similarity_threshold": args.similarity_threshold, "review_quality_threshold": args.review_quality_threshold, "review_detection_threshold": args.review_detection_threshold, "batch_size": args.batch_size, "comparisons": comparisons, "timings": {"neighbor_search_seconds": round(search_elapsed, 6), "peak_memory_mb": grouping_memory_mb()}, "order_stable": order_stable, "recommendation": {"k": recommended["k"], "pair_f1": recommended["populations"]["all"]["pair_f1"], "pair_precision": recommended["populations"]["all"]["pair_precision"], "low_signal_faces": recommended["populations"]["all"]["low_signal_faces"]}, "results": results, "cluster_summaries": cluster_summaries}
    (output / "grouping_results.json").write_text(json.dumps(summary, indent=2) + "\n")
    with (output / "grouping_results.csv").open("w", newline="") as stream:
        fieldnames = ["k", "population", "faces", "clusters", "purity", "pair_precision", "pair_recall", "pair_f1", "low_signal_faces", "low_quality_faces", "low_detection_faces", "very_low_signal_faces", "low_signal_mixed_faces", "mixed_person_clusters", "people_split_across_clusters", "mean_clusters_per_person"]
        writer = csv.DictWriter(stream, fieldnames=fieldnames)
        writer.writeheader()
        for result in results:
            for population, metrics in result["populations"].items():
                writer.writerow({"k": result["k"], "population": population, "faces": metrics["faces"], "clusters": metrics["clusters_with_population"], **{key: metrics[key] for key in fieldnames[4:]}})
    (output / "report.md").write_text(grouping_report_markdown(summary) + "\n")
    (output / "report.html").write_text(grouping_report_html(summary, cluster_files))
    print(json.dumps({"output": str(output), "input_faces": len(rows), "k_values": k_values, "order_stable": order_stable}, indent=2))
    return 0


def build_score_matrix(db: sqlite3.Connection) -> tuple[list[dict[str, Any]], set[int]]:
    total, labeled = reviewed(db)
    if total != labeled:
        raise ValueError(f"Blind review incomplete: {labeled}/{total} faces labeled")
    refs = db.execute("SELECT f.embedding,l.person_id,p.label FROM faces f JOIN labels l ON l.face_id=f.id JOIN people p ON p.id=l.person_id WHERE l.role='reference' AND l.kind='person'").fetchall()
    if not refs:
        raise ValueError("At least one reference face is required before calibration")
    reference_people = {row["person_id"] for row in refs}
    gallery: dict[int, dict[str, Any]] = {}
    for row in refs:
        gallery.setdefault(row["person_id"], {"label": row["label"], "examples": []})["examples"].append(vector(row["embedding"]))
    evaluations = db.execute("SELECT f.id face_id,f.embedding,f.quality,f.detection_confidence,f.image_id,i.relative_path,l.kind truth_kind,l.person_id truth_person_id FROM faces f JOIN images i ON i.id=f.image_id JOIN labels l ON l.face_id=f.id WHERE l.role='evaluation' ORDER BY f.id").fetchall()
    matrix: list[dict[str, Any]] = []
    for row in evaluations:
        query = vector(row["embedding"])
        scores = []
        for person_id, person in gallery.items():
            values = sorted((cosine(query, example) for example in person["examples"]), reverse=True)
            scores.append({"person_id": person_id, "label": person["label"], "similarity": values[0], "support_count": len(values)})
        scores.sort(key=lambda item: item["similarity"], reverse=True)
        true_person = row["truth_person_id"] if row["truth_kind"] == "person" else None
        true_score = next((item["similarity"] for item in scores if item["person_id"] == true_person), None)
        true_rank = next((index + 1 for index, item in enumerate(scores) if item["person_id"] == true_person), None)
        top = scores[0]
        second = scores[1]["similarity"] if len(scores) > 1 else 0.0
        matrix.append({
            "face_id": row["face_id"], "image_id": row["image_id"], "relative_path": row["relative_path"],
            "quality": row["quality"], "detection_confidence": row["detection_confidence"],
            "truth_kind": row["truth_kind"], "truth_person_id": true_person,
            "represented": true_person in reference_people if true_person is not None else False,
            "top_person_id": top["person_id"], "top_person_label": top["label"],
            "top_similarity": top["similarity"], "second_similarity": second,
            "margin": top["similarity"] - second, "true_similarity": true_score,
            "true_rank": true_rank, "person_scores": scores,
        })
    return matrix, reference_people


def classify_calibration_row(row: dict[str, Any], settings: dict[str, Any], reference_people: set[int], near_miss_gap: float) -> dict[str, Any]:
    true_person = row["truth_person_id"]
    if row["quality"] < settings["quality_threshold"] or row["top_similarity"] < settings["similarity_threshold"]:
        decision = "unknown"
    elif row["margin"] < settings["margin_threshold"] or row["person_scores"][0]["support_count"] < settings["min_support"]:
        decision = "review"
    else:
        decision = "high_confidence"
    if true_person is not None and true_person not in reference_people:
        comparison = "known_person_without_reference"
    elif row["top_person_id"] == true_person and true_person is not None:
        comparison = "correct"
    elif true_person is None and decision == "unknown":
        comparison = "unknown_correct"
    else:
        comparison = "incorrect_prediction"
    near_miss = bool(true_person is not None and row["represented"] and comparison != "correct" and row["true_similarity"] is not None and row["top_similarity"] - row["true_similarity"] <= near_miss_gap)
    return {"comparison": comparison, "decision": decision, "near_miss": near_miss}


def evaluate_calibration(matrix: list[dict[str, Any]], reference_people: set[int], settings: dict[str, Any], near_miss_gap: float) -> dict[str, Any]:
    counts = Counter({"correct": 0, "unknown_correct": 0, "incorrect_prediction": 0, "known_person_without_reference": 0, "review": 0, "unknown": 0, "high_confidence": 0, "near_miss": 0})
    represented = 0
    for row in matrix:
        result = classify_calibration_row(row, settings, reference_people, near_miss_gap)
        counts[result["comparison"]] += 1
        counts[result["decision"]] += 1
        counts["near_miss"] += int(result["near_miss"])
        represented += int(row["represented"])
    total = len(matrix)
    suggested = counts["high_confidence"]
    counts["evaluation_faces"] = total
    counts["represented_faces"] = represented
    counts["unrepresented_faces"] = total - represented
    counts["coverage"] = round(suggested / total, 4) if total else 0.0
    counts["accuracy_on_all"] = round((counts["correct"] + counts["unknown_correct"]) / total, 4) if total else None
    high_confidence_correct = 0
    for row in matrix:
        result = classify_calibration_row(row, settings, reference_people, near_miss_gap)
        if result["decision"] == "high_confidence" and result["comparison"] in ("correct", "unknown_correct"):
            high_confidence_correct += 1
    counts["high_confidence_correct"] = high_confidence_correct
    counts["high_confidence_precision"] = round(high_confidence_correct / suggested, 4) if suggested else None
    counts["high_confidence_false_positives"] = sum(1 for row in matrix if classify_calibration_row(row, settings, reference_people, near_miss_gap)["decision"] == "high_confidence" and classify_calibration_row(row, settings, reference_people, near_miss_gap)["comparison"] not in ("correct", "unknown_correct"))
    return {**dict(counts), **settings, "settings": settings, "near_miss_gap": near_miss_gap}


def svg_histogram(path: Path, series: dict[str, list[float]], title: str, minimum: float, maximum: float, x_tick_interval: float = 0.2, note: str | None = None) -> None:
    width, height = 820, 480
    left, top, right, bottom = 70, 75, 25, 65
    plot_width, plot_height = width - left - right, height - top - bottom
    bins = 20
    colors = ["#2673c9", "#d64b4b", "#43a047", "#8e5bb7"]
    counts_by_series = []
    for values in series.values():
        counts = [0] * bins
        for value in values:
            if value < minimum or value > maximum:
                continue
            index = min(bins - 1, max(0, int((value - minimum) / max(1e-9, maximum - minimum) * bins)))
            counts[index] += 1
        counts_by_series.append(counts)
    max_count = max([1] + [count for counts in counts_by_series for count in counts])
    elements = [f'<text x="{left}" y="28" font-size="18">{html.escape(title)}</text>']
    for index, (label, _) in enumerate(series.items()):
        x = left + index * 175
        elements.append(f'<rect x="{x}" y="43" width="12" height="12" fill="{colors[index % len(colors)]}"/><text x="{x+18}" y="54">{html.escape(label)}</text>')
    elements.append(f'<rect x="{left}" y="{top}" width="{plot_width}" height="{plot_height}" fill="#fafafa" stroke="#ddd"/>')
    for tick in range(6):
        value = max_count * tick / 5
        y = top + plot_height - plot_height * tick / 5
        elements.append(f'<line x1="{left}" y1="{y:.1f}" x2="{left+plot_width}" y2="{y:.1f}" stroke="#ddd"/>')
        elements.append(f'<text x="{left-8}" y="{y+4:.1f}" text-anchor="end">{value:.0f}</text>')
    group_width = plot_width / bins
    bar_width = group_width / max(1, len(series)) * .86
    for series_index, counts in enumerate(counts_by_series):
        for index, count in enumerate(counts):
            bar_height = plot_height * count / max_count
            x = left + index * group_width + series_index * bar_width
            y = top + plot_height - bar_height
            elements.append(f'<rect x="{x:.1f}" y="{y:.1f}" width="{bar_width-1:.1f}" height="{bar_height:.1f}" fill="{colors[series_index % len(colors)]}" opacity=".78"/>')
    for value in _regular_tick_values([minimum, maximum], x_tick_interval):
        x = left + plot_width * (value - minimum) / max(1e-9, maximum - minimum)
        elements.append(f'<line x1="{x:.1f}" y1="{top+plot_height}" x2="{x:.1f}" y2="{top+plot_height+5}" stroke="black"/><text x="{x:.1f}" y="{top+plot_height+22}" text-anchor="middle">{value:.2f}</text>')
    elements.append(f'<text x="{left+plot_width/2}" y="{height-12}" text-anchor="middle">score</text><text x="18" y="{top+plot_height/2}" transform="rotate(-90 18 {top+plot_height/2})" text-anchor="middle">number of faces</text>')
    if note:
        elements.append(f'<text x="{left}" y="{height-30}" font-size="11">{html.escape(note)}</text>')
    path.write_text(f'<svg xmlns="http://www.w3.org/2000/svg" width="{width}" height="{height}">{"".join(elements)}</svg>\n')


def svg_line_chart(path: Path, rows: list[dict[str, Any]], title: str, x_key: str) -> None:
    width, height = 820, 480
    left, top, right, bottom = 70, 75, 25, 65
    plot_width, plot_height = width - left - right, height - top - bottom
    if not rows:
        path.write_text(f'<svg xmlns="http://www.w3.org/2000/svg" width="{width}" height="{height}"><text x="20" y="30">{html.escape(title)}: no data</text></svg>\n'); return
    x_values = [float(row[x_key]) for row in rows]
    series = [("high-confidence precision", "high_confidence_precision", "#2673c9"), ("coverage", "coverage", "#43a047"), ("high-confidence false-positive rate", "high_confidence_false_positives", "#d64b4b"), ("top-1 accuracy", "accuracy_on_all", "#8e5bb7")]
    elements = [f'<text x="{left}" y="28" font-size="18">{html.escape(title)}</text>', f'<rect x="{left}" y="{top}" width="{plot_width}" height="{plot_height}" fill="#fafafa" stroke="#ddd"/>']
    for index, (label, _, color) in enumerate(series):
        x = left + index * 210
        elements.append(f'<line x1="{x}" y1="48" x2="{x+14}" y2="48" stroke="{color}" stroke-width="3"/><text x="{x+20}" y="53">{html.escape(label)}</text>')
    for tick in range(6):
        value = tick / 5
        y = top + plot_height - plot_height * value
        elements.append(f'<line x1="{left}" y1="{y:.1f}" x2="{left+plot_width}" y2="{y:.1f}" stroke="#ddd"/><text x="{left-8}" y="{y+4:.1f}" text-anchor="end">{value:.1f}</text>')
    for label, key, color in series:
        points = []
        for row in rows:
            value = float(row.get(key, 0))
            if key == "high_confidence_false_positives": value = value / max(1, row.get("evaluation_faces", 1))
            x = left + (float(row[x_key]) - min(x_values)) / max(1e-9, max(x_values) - min(x_values)) * plot_width
            y = top + plot_height - value * plot_height
            points.append(f"{x:.1f},{y:.1f}")
        elements.append(f'<polyline points="{" ".join(points)}" fill="none" stroke="{color}" stroke-width="2"/>')
    tick_values = [round(value, 2) for value in _regular_tick_values(x_values, 0.05)]
    for value in tick_values:
        position = (value - min(x_values)) / max(1e-9, max(x_values) - min(x_values))
        x = left + position * plot_width
        elements.append(f'<line x1="{x:.1f}" y1="{top+plot_height}" x2="{x:.1f}" y2="{top+plot_height+5}" stroke="black"/><text x="{x:.1f}" y="{top+plot_height+22}" text-anchor="middle">{value:.2f}</text>')
    elements.append(f'<text x="{left+plot_width/2}" y="{height-12}" text-anchor="middle">{html.escape(x_key.replace("_", " "))}</text><text x="18" y="{top+plot_height/2}" transform="rotate(-90 18 {top+plot_height/2})" text-anchor="middle">rate</text>')
    path.write_text(f'<svg xmlns="http://www.w3.org/2000/svg" width="{width}" height="{height}">{"".join(elements)}</svg>\n')


def _regular_tick_values(values: list[float], interval: float) -> list[float]:
    """Return values in the input range at a readable, fixed interval."""
    if not values:
        return []
    minimum = min(values)
    maximum = max(values)
    first = math.ceil((minimum - 1e-9) / interval) * interval
    ticks = []
    value = first
    while value <= maximum + 1e-9:
        ticks.append(round(value, 10))
        value += interval
    return ticks or [minimum]


def _heatmap_color(score: float) -> str:
    """Use a fixed 0.90-1.00 scale with distinct color bands."""
    stops = [
        (0.90, (139, 58, 58)),   # dark red
        (0.92, (199, 119, 43)),  # orange
        (0.94, (217, 182, 74)),  # gold
        (0.96, (139, 195, 74)),  # yellow-green
        (0.98, (67, 160, 71)),   # green
        (1.00, (0, 200, 83)),     # bright green
    ]
    if score <= stops[0][0]:
        return f"rgb({','.join(str(value) for value in stops[0][1])})"
    if score >= stops[-1][0]:
        return f"rgb({','.join(str(value) for value in stops[-1][1])})"
    for (low_score, low_color), (high_score, high_color) in zip(stops, stops[1:]):
        if score <= high_score:
            normalized = (score - low_score) / (high_score - low_score)
            color = [round(low_color[index] + (high_color[index] - low_color[index]) * normalized) for index in range(3)]
            return f"rgb({','.join(str(value) for value in color)})"
    # Defensive fallback; all finite scores are handled by the branches above.
    color = stops[-1][1]
    return f"rgb({','.join(str(value) for value in color)})"


def svg_heatmap(path: Path, rows: list[dict[str, Any]], title: str, score_precision: int = 2) -> None:
    width, height = 920, 500
    left, top, right, bottom = 85, 75, 110, 80
    plot_width, plot_height = width - left - right, height - top - bottom
    if not rows:
        path.write_text(f'<svg xmlns="http://www.w3.org/2000/svg" width="{width}" height="{height}"><text x="20" y="30">{html.escape(title)}: no data</text></svg>\n'); return
    x_values = sorted({row["similarity_threshold"] for row in rows})
    y_values = sorted({row["margin_threshold"] for row in rows})
    lookup = {(row["similarity_threshold"], row["margin_threshold"]): row for row in rows}
    cell_width = plot_width / max(1, len(x_values))
    cell_height = plot_height / max(1, len(y_values))
    elements = [f'<text x="{left}" y="28" font-size="18">{html.escape(title)}</text>']
    for yi, y_value in enumerate(y_values):
        for xi, x_value in enumerate(x_values):
            row = lookup.get((x_value, y_value))
            score = float(row.get("high_confidence_precision") or 0) if row else 0.0
            elements.append(f'<rect x="{left + xi*cell_width:.1f}" y="{top + plot_height-(yi+1)*cell_height:.1f}" width="{cell_width+0.5:.1f}" height="{cell_height+0.5:.1f}" fill="{_heatmap_color(score)}"/>')
            if cell_width > 18 and cell_height > 14:
                elements.append(f'<text x="{left + (xi+0.5)*cell_width:.1f}" y="{top + plot_height-(yi+0.5)*cell_height+4:.1f}" text-anchor="middle" font-size="10">{score:.{score_precision}f}</text>')
    for value in _regular_tick_values(x_values, 0.05):
        index = min(range(len(x_values)), key=lambda candidate: abs(x_values[candidate] - value))
        x = left + (index + .5) * cell_width
        elements.append(f'<text x="{x:.1f}" y="{top+plot_height+20}" text-anchor="middle">{value:.2f}</text>')
    for index, value in enumerate(y_values):
        y = top + plot_height - (index + .5) * cell_height
        elements.append(f'<text x="{left-8}" y="{y+4:.1f}" text-anchor="end">{value:.2f}</text>')
    for index in range(6):
        score = 0.90 + index * 0.02
        x = left + plot_width + 12
        y = top + plot_height - score * plot_height
        # The legend is positioned independently of the chart's 0-1 y scale.
        legend_y = top + plot_height - index * plot_height / 5
        elements.append(f'<rect x="{x}" y="{legend_y-6:.1f}" width="12" height="12" fill="{_heatmap_color(score)}"/><text x="{x+16}" y="{legend_y+4:.1f}">{score:.2f}</text>')
    elements.append(f'<text x="{left+plot_width/2}" y="{height-25}" text-anchor="middle">similarity threshold</text><text x="18" y="{top+plot_height/2}" transform="rotate(-90 18 {top+plot_height/2})" text-anchor="middle">margin threshold</text><text x="{left+plot_width+5}" y="{top-10}">precision</text>')
    path.write_text(f'<svg xmlns="http://www.w3.org/2000/svg" width="{width}" height="{height}">{"".join(elements)}</svg>\n')


def svg_outcome_bars(path: Path, rows: list[dict[str, Any]], title: str, x_key: str) -> None:
    width, height = 820, 500
    left, top, right, bottom = 70, 75, 25, 70
    plot_width, plot_height = width - left - right, height - top - bottom
    colors = [("high_confidence", "#2673c9"), ("review", "#e0a21a"), ("unknown", "#999999")]
    bar_width = plot_width / max(1, len(rows)) * 0.72
    elements = [f'<text x="{left}" y="28" font-size="18">{html.escape(title)}</text>']
    for index, (key, color) in enumerate(colors):
        x = left + index * 150
        elements.append(f'<rect x="{x}" y="43" width="12" height="12" fill="{color}"/><text x="{x+18}" y="54">{key.replace("_", " ")}</text>')
    for index, row in enumerate(rows):
        total = max(1, row.get("evaluation_faces", 1))
        x = left + index * plot_width / max(1, len(rows)) + (plot_width / max(1, len(rows)) - bar_width) / 2
        bottom = top + plot_height
        for key, color in colors:
            value = row.get(key, 0) / total
            bar_height = value * plot_height
            bottom -= bar_height
            elements.append(f'<rect x="{x:.1f}" y="{bottom:.1f}" width="{bar_width:.1f}" height="{bar_height:.1f}" fill="{color}"/>')
    for value in _regular_tick_values([float(row[x_key]) for row in rows], 0.05):
        index = min(range(len(rows)), key=lambda candidate: abs(float(rows[candidate][x_key]) - value))
        x = left + index * plot_width / max(1, len(rows)) + plot_width / max(1, len(rows)) / 2
        elements.append(f'<line x1="{x:.1f}" y1="{top+plot_height}" x2="{x:.1f}" y2="{top+plot_height+5}" stroke="black"/><text x="{x:.1f}" y="{top+plot_height+22}" text-anchor="middle">{value:.2f}</text>')
    for tick in range(6):
        value = tick / 5
        y = top + plot_height - value * plot_height
        elements.append(f'<line x1="{left}" y1="{y:.1f}" x2="{left+plot_width}" y2="{y:.1f}" stroke="#ddd"/><text x="{left-8}" y="{y+4:.1f}" text-anchor="end">{value:.1f}</text>')
    elements.append(f'<text x="{left+plot_width/2}" y="{height-15}" text-anchor="middle">{html.escape(x_key.replace("_", " "))}</text><text x="18" y="{top+plot_height/2}" transform="rotate(-90 18 {top+plot_height/2})" text-anchor="middle">proportion of evaluation faces</text>')
    path.write_text(f'<svg xmlns="http://www.w3.org/2000/svg" width="{width}" height="{height}">{"".join(elements)}</svg>\n')


def render_chart_set(charts: Path, matrix: list[dict[str, Any]], sweep: list[dict[str, Any]]) -> None:
    """Render one chart set for a selected population of evaluation faces."""
    charts.mkdir(parents=True, exist_ok=True)
    correct_scores = [row["top_similarity"] for row in matrix if row["top_person_id"] == row["truth_person_id"] and row["truth_person_id"] is not None]
    other_scores = [row["top_similarity"] for row in matrix if row["top_person_id"] != row["truth_person_id"] or row["truth_person_id"] is None]
    correct_margins = [row["margin"] for row in matrix if row["top_person_id"] == row["truth_person_id"] and row["truth_person_id"] is not None]
    other_margins = [row["margin"] for row in matrix if row["top_person_id"] != row["truth_person_id"] or row["truth_person_id"] is None]
    svg_histogram(charts / "similarity.svg", {"top correct": correct_scores, "other": other_scores}, "Top similarity scores", 0.0, 1.0, x_tick_interval=0.1)
    svg_histogram(charts / "similarity_020_100.svg", {"top correct": correct_scores, "other": other_scores}, "Top similarity scores (0.20–1.00)", 0.2, 1.0, x_tick_interval=0.1, note="Scores below 0.20 are omitted from this chart.")
    svg_histogram(charts / "margin.svg", {"correct": correct_margins, "other": other_margins}, "Top-two margins", 0.0, 1.0, x_tick_interval=0.1)
    svg_histogram(charts / "margin_010_100.svg", {"correct": correct_margins, "other": other_margins}, "Top-two margins (0.10–1.00)", 0.1, 1.0, x_tick_interval=0.1, note="Scores below 0.10 are omitted from this chart.")
    if not matrix:
        return
    margin_values = sorted({row["margin_threshold"] for row in sweep})
    support_values = sorted({row["min_support"] for row in sweep})
    quality_values = sorted({row["quality_threshold"] for row in sweep})
    curve = [row for row in sweep if row["margin_threshold"] == (0.05 if 0.05 in margin_values else margin_values[0]) and row["min_support"] == (1 if 1 in support_values else support_values[0]) and row["quality_threshold"] == (0.25 if 0.25 in quality_values else quality_values[0])]
    curve.sort(key=lambda row: row["similarity_threshold"])
    svg_line_chart(charts / "threshold_curve.svg", curve, "Similarity threshold tradeoffs", "similarity_threshold")
    heatmap_rows = [row for row in sweep if row["min_support"] == (1 if 1 in support_values else support_values[0]) and row["quality_threshold"] == (0.25 if 0.25 in quality_values else quality_values[0])]
    svg_heatmap(charts / "similarity_margin_heatmap.svg", heatmap_rows, "High-confidence precision by similarity and margin")
    focused_heatmap_rows = [row for row in heatmap_rows if 0.25 <= row["similarity_threshold"] <= 0.35]
    svg_heatmap(charts / "similarity_margin_heatmap_025_035.svg", focused_heatmap_rows, "High-confidence precision by similarity and margin (0.25–0.35)", score_precision=4)
    svg_outcome_bars(charts / "outcome_thresholds.svg", curve, "Outcome mix by similarity threshold", "similarity_threshold")


def write_chart_index(output: Path) -> None:
    """Write a visual comparison page for all, reference, and non-reference faces."""
    columns = [("All faces", "all"), ("Faces with reference people", "reference"), ("Faces without reference people", "non_reference")]
    rows = [
        ("Top similarity scores", "similarity.svg", "similarity_020_100.svg"),
        ("Top-two margins", "margin.svg", "margin_010_100.svg"),
        ("Similarity threshold tradeoffs", "threshold_curve.svg", None),
        ("Precision by similarity and margin", "similarity_margin_heatmap.svg", "similarity_margin_heatmap_025_035.svg"),
        ("Outcome mix by similarity threshold", "outcome_thresholds.svg", None),
    ]
    lines = ["<!doctype html>", '<html lang="en"><head><meta charset="utf-8"><title>Calibration chart comparison</title>', "<style>body{font-family:system-ui,sans-serif;margin:1rem} table{border-collapse:collapse;width:100%;table-layout:fixed} th,td{border:1px solid #ccc;vertical-align:top;padding:.5rem} th{background:#f4f4f4} td img{width:100%;height:auto;display:block;margin-bottom:.75rem} h1{font-size:1.4rem} h2{font-size:1rem}</style>", "</head><body>", "<h1>Calibration chart comparison</h1>", "<p>Each column uses the same chart and calibration settings, filtered by evaluation-face population.</p>", '<table><thead><tr><th width="75">Graph</th>']
    lines.extend(f"<th>{html.escape(label)}</th>" for label, _ in columns)
    lines.append("</tr></thead><tbody>")
    for title, full_name, focused_name in rows:
        lines.append(f"<tr><th>{html.escape(title)}</th>")
        for _, directory in columns:
            images = [f'<img src="charts/{directory}/{full_name}" alt="{html.escape(title)} — {directory}">']
            if focused_name:
                images.append(f'<img src="charts/{directory}/{focused_name}" alt="Focused {html.escape(title)} — {directory}">')
            lines.append(f"<td>{''.join(images)}</td>")
        lines.append("</tr>")
    lines += ["</tbody></table>", "</body></html>", ""]
    (output / "charts.html").write_text("\n".join(lines))


def calibrate_experiment(args: argparse.Namespace) -> int:
    experiment = args.experiment.resolve()
    db = connect(experiment)
    init_db(db)
    matrix, reference_people = build_score_matrix(db)
    similarity_values = parse_range(args.similarity_range)
    margin_values = parse_range(args.margin_range)
    support_values = parse_int_values(args.support_values)
    quality_values = parse_float_values(args.quality_values)
    sweep = []
    for similarity in similarity_values:
        for margin in margin_values:
            for support in support_values:
                for quality in quality_values:
                    settings = {"similarity_threshold": similarity, "margin_threshold": margin, "min_support": support, "quality_threshold": quality}
                    sweep.append(evaluate_calibration(matrix, reference_people, settings, args.near_miss_gap))
    stamp = datetime.now().strftime("%Y%m%d_%H%M%S")
    output = experiment / f"calibration-{stamp}"
    charts = output / "charts"
    charts.mkdir(parents=True, exist_ok=True)
    with (output / "score_matrix.jsonl").open("w") as stream:
        for row in matrix:
            stream.write(json.dumps(row) + "\n")
    summary = {"generated_at": utc_now(), "experiment": get_meta(db, "experiment_id"), "reference_people": len(reference_people), "evaluation_faces": len(matrix), "grid": {"similarity": similarity_values, "margin": margin_values, "support": support_values, "quality": quality_values}, "near_miss_gap": args.near_miss_gap, "results": sweep}
    (output / "calibration.json").write_text(json.dumps(summary, indent=2) + "\n")
    populations = {
        "all": matrix,
        "reference": [row for row in matrix if row["represented"]],
        "non_reference": [row for row in matrix if not row["represented"]],
    }
    for name, population in populations.items():
        population_sweep = [evaluate_calibration(population, reference_people, row["settings"], args.near_miss_gap) for row in sweep]
        render_chart_set(charts / name, population, population_sweep)
    write_chart_index(output)
    eligible = [row for row in sweep if row["high_confidence_false_positives"] == 0]
    if eligible:
        best = max(eligible, key=lambda row: (row["accuracy_on_all"] or 0, row["coverage"]))
        recommendation_note = "This operating point has no observed high-confidence false positives in the reviewed dataset."
    else:
        lowest_false_positive_count = min(row["high_confidence_false_positives"] for row in sweep)
        safest = [row for row in sweep if row["high_confidence_false_positives"] == lowest_false_positive_count]
        best = max(safest, key=lambda row: (row["accuracy_on_all"] or 0, row["coverage"]))
        recommendation_note = f"No sweep point eliminated high-confidence false positives; this point has the lowest observed count ({lowest_false_positive_count})."
    lines = ["# Model Settings Calibration Report", "", f"Generated: `{summary['generated_at']}`", "", "## Summary", "", f"- Reference people: {len(reference_people)}", f"- Evaluation faces: {len(matrix)}", f"- Near-miss gap: `{args.near_miss_gap}`", f"- Score matrix: `score_matrix.jsonl`", "", "## Recommended candidate operating point", "", recommendation_note, "", "| Setting | Value |", "|---|---:|"]
    lines.extend(f"| {key} | {value} |" for key, value in best["settings"].items())
    lines += ["", f"The selected point has `{best['high_confidence_false_positives']}` high-confidence false positives, `{best['accuracy_on_all']}` overall accuracy, and `{best['coverage']}` high-confidence coverage.", "", "## Charts", "", "- [HTML chart comparison](charts.html)", "- [All-face charts](charts/all/)", "- [Reference-backed charts](charts/reference/)", "- [Non-reference charts](charts/non_reference/)", "", "## Interpretation", "", "Results are face-level. People without reference examples are reported separately as `known_person_without_reference` and are not a fair recognition-accuracy test. Validate the selected point using candidate reveal and human review before adopting it.", "", "## Best settings result", "", "| Metric | Value |", "|---|---:|"]
    lines.extend(f"| {key} | {value} |" for key, value in best.items() if key != "settings")
    (output / "report.md").write_text("\n".join(lines) + "\n")
    print(json.dumps({"output": str(output), "evaluation_faces": len(matrix), "sweep_points": len(sweep), "recommended": best["settings"]}, indent=2))
    return 0


def analyze_db(db: sqlite3.Connection, settings: dict[str, Any]) -> dict[str, Any]:
    rows = db.execute("SELECT p.*,l.kind AS truth_kind,l.person_id AS truth_person_id FROM predictions p JOIN labels l ON l.face_id=p.face_id WHERE l.role='evaluation'").fetchall()
    reference_people = {row["person_id"] for row in db.execute("SELECT DISTINCT person_id FROM labels WHERE role='reference' AND kind='person'")}
    counts = {"evaluation_faces": len(rows), "missed_faces_recorded": db.execute("SELECT COUNT(*) FROM missed_faces").fetchone()[0], "correct": 0, "unknown_correct": 0, "incorrect_prediction": 0, "known_person_without_reference": 0, "review_required": 0, "unknown_prediction": 0, "high_confidence": 0, "high_confidence_false_positive": 0}
    post_review_names = {"incorrect": "incorrect_review", "appropriately_unknown": "appropriately_unknown_review", "too_cautious": "too_cautious", "too_confident": "too_confident"}
    for row in db.execute("SELECT outcome,COUNT(*) count FROM post_reviews GROUP BY outcome"):
        counts[post_review_names.get(row["outcome"], f"{row['outcome']}_review")] = row["count"]
    by_quality: dict[str, dict[str, int]] = {}
    for row in rows:
        bucket = "low" if row["quality"] < 0.35 else "medium" if row["quality"] < 0.65 else "high"
        by_quality.setdefault(bucket, {"total": 0, "correct": 0, "incorrect": 0})["total"] += 1
        if row["category"] == "review":
            counts["review_required"] += 1
        elif row["category"] == "unknown":
            counts["unknown_prediction"] += 1
        elif row["category"] == "high_confidence":
            counts["high_confidence"] += 1
        truth_person = row["truth_person_id"] if row["truth_kind"] == "person" else None
        if truth_person is not None and truth_person not in reference_people:
            classification = "known_person_without_reference"
        elif row["person_id"] == truth_person and truth_person is not None:
            classification = "correct"
        elif row["person_id"] is None and truth_person is None:
            classification = "unknown_correct"
        else:
            classification = "incorrect_prediction"
        counts[classification] += 1
        if classification in ("correct", "unknown_correct"):
            by_quality[bucket]["correct"] += 1
        else:
            by_quality[bucket]["incorrect"] += 1
        if row["category"] == "high_confidence" and classification not in ("correct", "unknown_correct"):
            counts["high_confidence_false_positive"] += 1
    counts["accuracy_on_all"] = round((counts["correct"] + counts["unknown_correct"]) / len(rows), 4) if rows else None
    return {"settings": settings, "counts": counts, "quality_buckets": by_quality}


def markdown_report(result: dict[str, Any]) -> str:
    settings = result["settings"]
    counts = result["counts"]
    lines = ["# Simplified Model-Settings Review Report", "", f"Generated: `{result.get('generated_at', utc_now())}`", "", "## Settings", "", "| Setting | Value |", "|---|---:|"]
    lines.extend(f"| {html.escape(key)} | {value} |" for key, value in settings.items())
    lines += ["", "## Results", "", "| Metric | Value |", "|---|---:|"]
    lines.extend(f"| {key.replace('_', ' ')} | {value} |" for key, value in counts.items())
    lines += ["", "## Quality buckets", "", "| Bucket | Total | Correct | Incorrect |", "|---|---:|---:|---:|"]
    for bucket, values in result["quality_buckets"].items():
        lines.append(f"| {bucket} | {values['total']} | {values['correct']} | {values['incorrect']} |")
    return "\n".join(lines) + "\n"


def analyze_experiment(args: argparse.Namespace) -> int:
    db = connect(args.experiment.resolve())
    init_db(db)
    settings = latest_settings(db)
    settings.update({k: v for k, v in {"similarity_threshold": args.similarity_threshold, "margin_threshold": args.margin_threshold, "min_support": args.min_support, "quality_threshold": args.quality_threshold}.items() if v is not None})
    sweep_results = []
    if args.sweep:
        for similarity in (0.40, 0.45, 0.50):
            for margin in (0.03, 0.05, 0.10):
                for support in (1, 3):
                    for quality in (0.20, 0.25, 0.35):
                        candidate = {"similarity_threshold": similarity, "margin_threshold": margin, "min_support": support, "quality_threshold": quality}
                        generate_predictions(db, candidate)
                        sweep_results.append(analyze_db(db, candidate))
        generate_predictions(db, settings)
    elif not db.execute("SELECT 1 FROM predictions LIMIT 1").fetchone() or any(v is not None for v in (args.similarity_threshold, args.margin_threshold, args.min_support, args.quality_threshold)):
        generate_predictions(db, settings)
    result = analyze_db(db, settings)
    stamp = datetime.now().strftime("%Y%m%d_%H%M%S")
    result["experiment"] = get_meta(db, "experiment_id")
    result["generated_at"] = utc_now()
    result["settings"] = settings
    if sweep_results:
        result["sweep"] = sweep_results
    (args.experiment / f"report-{stamp}.json").write_text(json.dumps(result, indent=2) + "\n")
    c = result["counts"]
    lines = ["# Simplified Model-Settings Review Report", "", f"Generated: `{result['generated_at']}`", "", "## Settings", "", "| Setting | Value |", "|---|---:|"]
    lines.extend(f"| {html.escape(k)} | {v} |" for k, v in settings.items())
    lines += ["", "## Results", "", "| Metric | Value |", "|---|---:|"]
    for key, value in c.items():
        lines.append(f"| {key.replace('_', ' ')} | {value} |")
    lines += ["", "## Quality buckets", "", "| Bucket | Total | Correct | Incorrect |", "|---|---:|---:|---:|"]
    for bucket, values in result["quality_buckets"].items():
        lines.append(f"| {bucket} | {values['total']} | {values['correct']} | {values['incorrect']} |")
    (args.experiment / f"report-{stamp}.md").write_text("\n".join(lines) + "\n")
    print(json.dumps(result, indent=2))
    return 0


HTML = r'''<!doctype html><html><head><meta charset="utf-8"><title>Model settings review</title>
<style>body{font:14px system-ui;margin:0;color:#222}header{padding:12px 18px;background:#243447;color:white}main{display:grid;grid-template-columns:250px 1fr 330px;height:calc(100vh - 64px)}aside,section{overflow:auto;padding:12px;border-right:1px solid #ddd}.photo{padding:8px;border-bottom:1px solid #ddd;cursor:pointer}.photo.selected{background:#dfeeff}.viewer{position:relative;display:inline-block;max-width:100%}.viewer img{max-width:100%;max-height:65vh}.box{position:absolute;border:2px solid #f00;color:#fff;background:rgba(204, 0, 0, 0.15);padding:1px;font-size:12px}.face{padding:8px;border:1px solid #ddd;margin:6px 0}.hidden{display:none}button,select,input{margin:4px;padding:6px}#status{margin-left:20px}.warn{color:#a33}</style></head><body><header><b>Simplified model-settings review</b><button id="reveal">Reveal candidates</button><button id="analyze">Analyze</button><span id="status"></span></header><main><aside><h3>Photos</h3><div id="photos"></div></aside><section><h3 id="title">Select a photo</h3><div id="viewer"></div></section><aside><h3>Faces</h3><div id="faces"></div><hr><h3>People</h3><input id="newperson" placeholder="New person label"><button id="create">Create</button><div id="people"></div></aside></main><script>
let state={photos:[],people:[],selected:null,faces:[]};
const $=id=>document.getElementById(id); const api=(u,o)=>fetch(u,o).then(r=>r.json());
function load(){return Promise.all([api('/api/summary').then(s=>{$('status').textContent=`${s.labeled}/${s.faces} labeled; ${s.people} people`; }),api('/api/photos').then(p=>{state.photos=p; $('photos').innerHTML=p.map(x=>`<div class="photo" data-id="${x.id}">${x.relative_path}<br><small>${x.face_count} faces</small></div>`).join('');document.querySelectorAll('.photo').forEach(x=>x.onclick=()=>select(+x.dataset.id));}),api('/api/people').then(p=>{state.people=p;$('people').innerHTML=p.map(x=>`<div>${x.label}</div>`).join('');})]);}
function select(id){state.selected=id;api('/api/photo/'+id).then(x=>{state.faces=x.faces;$('title').textContent=x.relative_path;$('viewer').innerHTML=`<div class="viewer"><img src="/image/${id}">${x.faces.map(f=>{let b=f.box;return `<span class="box" style="left:${b[0]/x.width*100}%;top:${b[1]/x.height*100}%;width:${(b[2]-b[0])/x.width*100}%;height:${(b[3]-b[1])/x.height*100}%">${f.index+1}</span>`}).join('')}</div>`;renderFaces();});}
function renderFaces(){let p=state.people; $('faces').innerHTML=state.faces.map(f=>`<div class="face"><b>Face ${f.index+1}</b> quality ${f.quality}<br><select id="person-${f.id}"><option value="">Unknown / none</option>${p.map(x=>`<option value="${x.id}" ${f.person_id==x.id?'selected':''}>${x.label}</option>`).join('')}</select><select id="kind-${f.id}"><option value="person">Person</option><option value="unknown">Unknown</option><option value="ambiguous">Ambiguous</option><option value="false_detection">False detection</option></select><select id="role-${f.id}"><option value="evaluation">Evaluation</option><option value="reference">Reference</option></select><button onclick="save(${f.id})">Save</button><div class="prediction">${f.prediction?`Model: <b>${f.prediction.person_label||'unknown'}</b> (${f.prediction.category}, ${Number(f.prediction.similarity).toFixed(3)})`:''}</div></div>`).join('');}
function save(id){let person=$('person-'+id).value;let kind=$('kind-'+id).value;if(kind==='person'&&!person){alert('Choose a person or select another kind');return}api('/api/label',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({face_id:id,person_id:person?+person:null,kind,role:$('role-'+id).value})}).then(x=>{if(x.error)alert(x.error);else load().then(()=>select(state.selected));});}
$('create').onclick=()=>{let label=$('newperson').value.trim();if(!label)return;api('/api/people',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({label})}).then(()=>{$('newperson').value='';load();});};
$('reveal').onclick=()=>api('/api/reveal',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({})}).then(x=>{if(x.error)alert(x.error);else{alert('Candidates revealed');load().then(()=>state.selected&&select(state.selected));}});
$('analyze').onclick=()=>api('/api/analyze',{method:'POST'}).then(x=>alert(x.error||'Reports written to the experiment directory'));
load();</script></body></html>'''

STATIC_DIR = Path(__file__).resolve().parents[1] / "static"

class Handler(http.server.BaseHTTPRequestHandler):
    db_path: Path
    root: Path

    def log_message(self, fmt: str, *args: Any) -> None:
        print(fmt % args)

    def send_json(self, value: Any, status: int = 200) -> None:
        data = json.dumps(value).encode()
        self.send_response(status); self.send_header("Content-Type", "application/json"); self.send_header("Content-Length", str(len(data))); self.end_headers(); self.wfile.write(data)

    def body(self) -> dict[str, Any]:
        length = int(self.headers.get("Content-Length", "0")); return json.loads(self.rfile.read(length) or b"{}")

    def do_GET(self) -> None:  # noqa: N802
        db = connect(self.root)
        try:
            if self.path.startswith("/person/"):
                static_file = STATIC_DIR / "person.html"
                data = static_file.read_bytes()
                self.send_response(200); self.send_header("Content-Type", "text/html"); self.send_header("Content-Length", str(len(data))); self.end_headers(); self.wfile.write(data); return
            if self.path in ("/", "/index.html"):
                static_file = STATIC_DIR / "index.html"
                data = static_file.read_bytes() if static_file.is_file() else HTML.encode()
                self.send_response(200); self.send_header("Content-Type", "text/html"); self.send_header("Content-Length", str(len(data))); self.end_headers(); self.wfile.write(data); return
            if urlparse(self.path).path == "/api/summary":
                faces, labels = reviewed(db)
                photo_progress = db.execute("SELECT COUNT(*) total,COALESCE(SUM(CASE WHEN face_count=labeled_count THEN 1 ELSE 0 END),0) done FROM (SELECT i.id,COUNT(DISTINCT f.id) face_count,COUNT(DISTINCT l.face_id) labeled_count FROM images i LEFT JOIN faces f ON f.image_id=i.id LEFT JOIN labels l ON l.face_id=f.id GROUP BY i.id)").fetchone()
                query = parse_qs(urlparse(self.path).query)
                filter_text = query.get("path", [""])[0]
                filter_status = query.get("status", ["all"])[0]
                filtered_rows = db.execute("SELECT COUNT(DISTINCT f.id) face_count,COUNT(DISTINCT l.face_id) labeled_count FROM images i LEFT JOIN faces f ON f.image_id=i.id LEFT JOIN labels l ON l.face_id=f.id WHERE i.relative_path LIKE ? GROUP BY i.id", (f"%{filter_text}%",)).fetchall()
                if filter_status == "pending":
                    filtered_rows = [row for row in filtered_rows if row["face_count"] > row["labeled_count"]]
                elif filter_status == "done":
                    filtered_rows = [row for row in filtered_rows if row["face_count"] == row["labeled_count"]]
                filtered_total = len(filtered_rows)
                filtered_done = sum(1 for row in filtered_rows if row["face_count"] == row["labeled_count"])
                self.send_json({"faces": faces, "labeled": labels, "people": db.execute("SELECT COUNT(*) FROM people").fetchone()[0], "missed_faces": db.execute("SELECT COUNT(*) FROM missed_faces").fetchone()[0], "photos_total": photo_progress["total"], "photos_done": photo_progress["done"], "filtered_photos_total": filtered_total, "filtered_photos_done": filtered_done, "revealed": bool(db.execute("SELECT 1 FROM reveal_events LIMIT 1").fetchone())}); return
            if urlparse(self.path).path == "/api/photos":
                parsed = urlparse(self.path)
                query = parse_qs(parsed.query)
                sort = query.get("sort", ["filename"])[0]
                order_column = "i.capture_date" if sort == "creation_date" else "i.relative_path"
                filter_text = query.get("path", [""])[0]
                status = query.get("status", ["all"])[0]
                result_type = query.get("result", ["all"])[0]
                status_having = ""
                if status == "pending":
                    status_having = " HAVING COUNT(DISTINCT f.id) > COUNT(DISTINCT l.face_id)"
                elif status == "done":
                    status_having = " HAVING COUNT(DISTINCT f.id) = COUNT(DISTINCT l.face_id)"
                result_conditions = {
                    "missed_faces_recorded": "EXISTS (SELECT 1 FROM missed_faces mf WHERE mf.image_id=i.id)",
                    "incorrect_prediction": "EXISTS (SELECT 1 FROM predictions pr JOIN faces rf ON rf.id=pr.face_id JOIN labels tl ON tl.face_id=rf.id WHERE rf.image_id=i.id AND tl.role='evaluation' AND ((tl.kind='person' AND EXISTS (SELECT 1 FROM labels ref WHERE ref.person_id=tl.person_id AND ref.kind='person' AND ref.role='reference') AND (pr.person_id IS NULL OR pr.person_id<>tl.person_id)) OR (tl.kind<>'person' AND pr.person_id IS NOT NULL)))",
                    "known_person_without_reference": "EXISTS (SELECT 1 FROM predictions pr JOIN faces rf ON rf.id=pr.face_id JOIN labels tl ON tl.face_id=rf.id WHERE rf.image_id=i.id AND tl.role='evaluation' AND tl.kind='person' AND NOT EXISTS (SELECT 1 FROM labels ref WHERE ref.person_id=tl.person_id AND ref.kind='person' AND ref.role='reference'))",
                    "unknown_correct": "EXISTS (SELECT 1 FROM predictions pr JOIN faces rf ON rf.id=pr.face_id JOIN labels tl ON tl.face_id=rf.id WHERE rf.image_id=i.id AND tl.role='evaluation' AND tl.kind<>'person' AND pr.person_id IS NULL)",
                    "review_required": "EXISTS (SELECT 1 FROM predictions pr JOIN faces rf ON rf.id=pr.face_id WHERE rf.image_id=i.id AND pr.category='review')",
                    "unknown_prediction": "EXISTS (SELECT 1 FROM predictions pr JOIN faces rf ON rf.id=pr.face_id WHERE rf.image_id=i.id AND pr.category='unknown')",
                    "correct": "EXISTS (SELECT 1 FROM predictions pr JOIN faces rf ON rf.id=pr.face_id JOIN labels tl ON tl.face_id=rf.id WHERE rf.image_id=i.id AND tl.role='evaluation' AND tl.kind='person' AND pr.person_id=tl.person_id)",
                    "high_confidence": "EXISTS (SELECT 1 FROM predictions pr JOIN faces rf ON rf.id=pr.face_id WHERE rf.image_id=i.id AND pr.category='high_confidence')",
                    "high_confidence_false_positive": "EXISTS (SELECT 1 FROM predictions pr JOIN faces rf ON rf.id=pr.face_id JOIN labels tl ON tl.face_id=rf.id WHERE rf.image_id=i.id AND tl.role='evaluation' AND pr.category='high_confidence' AND ((tl.kind='person' AND (pr.person_id IS NULL OR pr.person_id<>tl.person_id)) OR (tl.kind<>'person' AND pr.person_id IS NOT NULL)))",
                    "incorrect_review": "EXISTS (SELECT 1 FROM post_reviews rv JOIN faces rf ON rf.id=rv.face_id WHERE rf.image_id=i.id AND rv.outcome='incorrect')",
                    "appropriately_unknown_review": "EXISTS (SELECT 1 FROM post_reviews rv JOIN faces rf ON rf.id=rv.face_id WHERE rf.image_id=i.id AND rv.outcome='appropriately_unknown')",
                    "too_cautious": "EXISTS (SELECT 1 FROM post_reviews rv JOIN faces rf ON rf.id=rv.face_id WHERE rf.image_id=i.id AND rv.outcome='too_cautious')",
                    "too_confident": "EXISTS (SELECT 1 FROM post_reviews rv JOIN faces rf ON rf.id=rv.face_id WHERE rf.image_id=i.id AND rv.outcome='too_confident')",
                }
                result_where = f" AND {result_conditions[result_type]}" if result_type in result_conditions else ""
                rows = db.execute(
                    f"SELECT i.id,i.relative_path,i.capture_date,i.width,i.height,COUNT(DISTINCT f.id) face_count,COUNT(DISTINCT l.face_id) labeled_count,COUNT(DISTINCT CASE WHEN l.kind='person' AND l.person_id IS NOT NULL THEN f.id END) assigned_count FROM images i LEFT JOIN faces f ON f.image_id=i.id LEFT JOIN labels l ON l.face_id=f.id WHERE i.relative_path LIKE ?{result_where} GROUP BY i.id{status_having} ORDER BY {order_column} COLLATE NOCASE, i.relative_path COLLATE NOCASE",
                    (f"%{filter_text}%",),
                ).fetchall(); self.send_json([dict(r) for r in rows]); return
            if self.path == "/api/people":
                people = [dict(r) for r in db.execute("SELECT p.id,p.label,MAX(l.created_at) last_used,COUNT(DISTINCT l.face_id) face_count FROM people p LEFT JOIN labels l ON l.person_id=p.id AND l.kind='person' GROUP BY p.id ORDER BY p.label COLLATE NOCASE")]
                recent_ids = {person["id"] for person in sorted(people, key=lambda item: (item["last_used"] or "", item["label"].lower()), reverse=True)[:10] if person["last_used"]}
                for person in people:
                    person["recent"] = person["id"] in recent_ids
                people.sort(key=lambda item: (not item["recent"], item["label"].lower()))
                self.send_json(people); return
            if self.path.startswith("/api/person/") and self.path.endswith("/photos"):
                person_id = int(self.path.split("/")[3])
                person = db.execute("SELECT p.id,p.label,COUNT(DISTINCT CASE WHEN l.role='reference' THEN l.face_id END) reference_count FROM people p LEFT JOIN labels l ON l.person_id=p.id AND l.kind='person' WHERE p.id=? GROUP BY p.id", (person_id,)).fetchone()
                if not person:
                    self.send_json({"error": "person not found"}, 404); return
                rows = db.execute("SELECT i.id image_id,i.relative_path,i.width,i.height,f.id face_id,f.x1,f.y1,f.x2,f.y2,f.face_index,l.role FROM images i JOIN faces f ON f.image_id=i.id JOIN labels l ON l.face_id=f.id AND l.person_id=? AND l.kind='person' ORDER BY i.relative_path,f.face_index", (person_id,)).fetchall()
                self.send_json({"person": dict(person), "photos": [dict(row) for row in rows]}); return
            if self.path == "/api/clusters": self.send_json(provisional_clusters(db)); return
            if self.path.startswith("/api/photo/"):
                image_id = int(self.path.rsplit("/", 1)[1]); image = db.execute("SELECT * FROM images WHERE id=?", (image_id,)).fetchone(); faces=[]
                for f in db.execute("SELECT f.*,l.kind,l.role,l.person_id,p.label person_label,pr.person_label prediction_label,pr.category,pr.similarity,pr.candidates_json FROM faces f LEFT JOIN labels l ON l.face_id=f.id LEFT JOIN people p ON p.id=l.person_id LEFT JOIN predictions pr ON pr.face_id=f.id WHERE f.image_id=? ORDER BY f.face_index", (image_id,)):
                    item = {
                        "id": f["id"],
                        "index": f["face_index"],
                        "box": [f["x1"], f["y1"], f["x2"], f["y2"]],
                        "detection_confidence": f["detection_confidence"],
                        "quality": f["quality"],
                        "kind": f["kind"],
                        "role": f["role"],
                        "person_id": f["person_id"],
                        "person_label": f["person_label"],
                    }
                    if f["prediction_label"]:
                        item["prediction"] = {
                            "person_label": f["prediction_label"],
                            "category": f["category"],
                            "similarity": f["similarity"],
                            "candidates": json.loads(f["candidates_json"]),
                        }
                    else:
                        item["prediction"] = None
                    faces.append(item)
                self.send_json({"id":image_id,"relative_path":image["relative_path"],"width":image["width"],"height":image["height"],"faces":faces}); return
            if self.path.startswith("/image/"):
                image = db.execute("SELECT absolute_path FROM images WHERE id=?", (int(self.path.rsplit("/",1)[1]),)).fetchone(); path=Path(image["absolute_path"])
                if not path.is_file(): self.send_error(404); return
                data=path.read_bytes(); self.send_response(200); self.send_header("Content-Type",mimetypes.guess_type(path.name)[0] or "application/octet-stream"); self.send_header("Content-Length",str(len(data))); self.end_headers(); self.wfile.write(data); return
            self.send_error(404)
        finally: db.close()

    def do_POST(self) -> None:  # noqa: N802
        db=connect(self.root)
        try:
            payload=self.body()
            if self.path == "/api/people":
                label=str(payload.get("label", "")).strip()
                if not label: self.send_json({"error":"label required"},400); return
                try: db.execute("INSERT INTO people(label,created_at) VALUES(?,?)",(label,utc_now())); db.commit(); self.send_json({"ok":True})
                except sqlite3.IntegrityError: self.send_json({"error":"person already exists"},409)
                return
            if self.path == "/api/label":
                face_id=int(payload["face_id"]); kind=payload["kind"]; role=payload["role"]; person_id=payload.get("person_id")
                if kind=="person" and not person_id: self.send_json({"error":"person required"},400); return
                if kind!="person": person_id=None
                db.execute("INSERT OR REPLACE INTO labels(face_id,role,kind,person_id,reviewer,created_at) VALUES(?,?,?,?,?,?)",(face_id,role,kind,person_id,"local-user",utc_now())); db.commit(); self.send_json({"ok":True}); return
            if self.path == "/api/reveal": self.send_json(generate_predictions(db, {**default_settings(), **{k: payload[k] for k in ("similarity_threshold", "margin_threshold", "min_support", "quality_threshold") if k in payload}})); return
            if self.path == "/api/post-review":
                db.execute("INSERT OR REPLACE INTO post_reviews(face_id,outcome,note,created_at) VALUES(?,?,?,?)", (int(payload["face_id"]), str(payload["outcome"]), payload.get("note"), utc_now())); db.commit(); self.send_json({"ok": True}); return
            if self.path == "/api/unassign":
                db.execute("DELETE FROM labels WHERE face_id=? AND kind='person'", (int(payload["face_id"]),)); db.commit(); self.send_json({"ok": True}); return
            if self.path == "/api/set-role":
                role = str(payload["role"])
                if role not in ("reference", "evaluation"):
                    self.send_json({"error": "role must be reference or evaluation"}, 400); return
                db.execute("UPDATE labels SET role=?,created_at=? WHERE face_id=? AND kind='person'", (role, utc_now(), int(payload["face_id"]))); db.commit(); self.send_json({"ok": True}); return
            if self.path == "/api/missed":
                db.execute("INSERT INTO missed_faces(image_id,note,reviewer,created_at) VALUES(?,?,?,?)", (int(payload["image_id"]), payload.get("note"), "local-user", utc_now())); db.commit(); self.send_json({"ok": True}); return
            if self.path == "/api/analyze":
                stamp=datetime.now().strftime("%Y%m%d_%H%M%S"); settings=latest_settings(db); result=analyze_db(db,settings); result["generated_at"] = utc_now(); (self.root/f"report-{stamp}.json").write_text(json.dumps(result,indent=2)+"\n"); (self.root/f"report-{stamp}.md").write_text(markdown_report(result)); self.send_json({"ok":True,"report":f"report-{stamp}"}); return
            self.send_error(404)
        except Exception as exc:
            self.send_json({"error":f"{type(exc).__name__}: {exc}"},400)
        finally: db.close()


def serve(args: argparse.Namespace) -> int:
    experiment=args.experiment.resolve(); db=connect(experiment); init_db(db); db.close()
    Handler.db_path=experiment/"experiment.sqlite3"; Handler.root=experiment
    server=http.server.ThreadingHTTPServer((args.host,args.port),Handler)
    print(f"Open http://{args.host}:{args.port}/ (Ctrl-C to stop)")
    try: server.serve_forever()
    except KeyboardInterrupt: pass
    finally: server.server_close()
    return 0


def main() -> int:
    parser=argparse.ArgumentParser(description=__doc__); sub=parser.add_subparsers(dest="command",required=True)
    imp=sub.add_parser("import",help="import photos and run inference"); imp.add_argument("directory",type=Path); imp.add_argument("--experiment",type=Path,required=True); imp.add_argument("--provider",choices=("auto","coreml","cpu"),default="auto"); imp.add_argument("--det-size",type=int,default=640); imp.add_argument("--omit-paths",action="store_true",help="do not store source paths; disables browser image display"); imp.set_defaults(func=import_experiment)
    dates=sub.add_parser("backfill-dates",help="populate empty capture_date values from file creation times"); dates.add_argument("--experiment",type=Path,required=True); dates.set_defaults(func=backfill_dates)
    cal=sub.add_parser("calibrate",help="build a score matrix and sweep model settings"); cal.add_argument("--experiment",type=Path,required=True); cal.add_argument("--similarity-range",default="0.25:0.55:0.01",help="START:STOP:STEP"); cal.add_argument("--margin-range",default="0.00:0.15:0.01",help="START:STOP:STEP"); cal.add_argument("--support-values",default="1,2,3",help="comma-separated support counts"); cal.add_argument("--quality-values",default="0.20,0.25,0.30,0.35",help="comma-separated quality thresholds"); cal.add_argument("--near-miss-gap",type=float,default=0.05); cal.set_defaults(func=calibrate_experiment)
    group=sub.add_parser("group-benchmark",help="benchmark mutual-kNN face grouping"); group.add_argument("--experiment",type=Path,required=True); group.add_argument("--k-values",default="5,10,20,40",help="comma-separated k values"); group.add_argument("--similarity-threshold",type=float,default=0.45); group.add_argument("--review-quality-threshold",type=float,default=0.70); group.add_argument("--review-detection-threshold",type=float,default=0.70); group.add_argument("--batch-size",type=int,default=512); group.set_defaults(func=group_benchmark_experiment)
    srv=sub.add_parser("serve",help="start local review service"); srv.add_argument("--experiment",type=Path,required=True); srv.add_argument("--host",default="127.0.0.1"); srv.add_argument("--port",type=int,default=8765); srv.set_defaults(func=serve)
    ana=sub.add_parser("analyze",help="write analysis reports"); ana.add_argument("--experiment",type=Path,required=True); ana.add_argument("--similarity-threshold",type=float); ana.add_argument("--margin-threshold",type=float); ana.add_argument("--min-support",type=int); ana.add_argument("--quality-threshold",type=float); ana.add_argument("--sweep",action="store_true",help="evaluate a standard threshold/margin/support/quality grid"); ana.set_defaults(func=analyze_experiment)
    args=parser.parse_args()
    if args.command == "group-benchmark":
        args.k_values = [int(value) for value in args.k_values.split(",") if value.strip()]
    return args.func(args)


if __name__ == "__main__": raise SystemExit(main())
