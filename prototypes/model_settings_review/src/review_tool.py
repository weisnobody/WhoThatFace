#!/usr/bin/env python3
"""Local tooling for the simplified face-recognition review protocol."""

from __future__ import annotations

import argparse
import base64
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
from datetime import datetime, timezone
from pathlib import Path
from typing import Any

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
            "INSERT OR IGNORE INTO images(asset_id,root_path,relative_path,absolute_path,path_sha256,status) VALUES(?,?,?,?,?,?)",
            # The review server needs the source path to display the image. The
            # experiment directory is expected to be protected as it contains
            # private dataset metadata.
            (asset["id"], str(root), relative, "" if args.omit_paths else str(file_path), path_hash, "pending"),
        )
        image = db.execute("SELECT * FROM images WHERE path_sha256=?", (path_hash,)).fetchone()
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


def analyze_db(db: sqlite3.Connection, settings: dict[str, Any]) -> dict[str, Any]:
    rows = db.execute("SELECT p.*,l.kind AS truth_kind,l.person_id AS truth_person_id FROM predictions p JOIN labels l ON l.face_id=p.face_id WHERE l.role='evaluation'").fetchall()
    counts = {"evaluation_faces": len(rows), "missed_faces_recorded": db.execute("SELECT COUNT(*) FROM missed_faces").fetchone()[0], "correct": 0, "incorrect": 0, "unknown_correct": 0, "unknown_incorrect": 0, "review": 0, "high_confidence": 0}
    by_quality: dict[str, dict[str, int]] = {}
    for row in rows:
        bucket = "low" if row["quality"] < 0.35 else "medium" if row["quality"] < 0.65 else "high"
        by_quality.setdefault(bucket, {"total": 0, "correct": 0, "incorrect": 0})["total"] += 1
        counts[row["category"]] = counts.get(row["category"], 0) + 1
        truth_person = row["truth_person_id"] if row["truth_kind"] == "person" else None
        if row["person_id"] == truth_person and truth_person is not None:
            counts["correct"] += 1; by_quality[bucket]["correct"] += 1
        elif row["person_id"] is None and truth_person is None:
            counts["unknown_correct"] += 1; by_quality[bucket]["correct"] += 1
        else:
            counts["incorrect"] += 1; by_quality[bucket]["incorrect"] += 1
    counts["accuracy_on_all"] = round((counts["correct"] + counts["unknown_correct"]) / len(rows), 4) if rows else None
    counts["high_confidence_false_positives"] = sum(1 for row in rows if row["category"] == "high_confidence" and row["person_id"] != (row["truth_person_id"] if row["truth_kind"] == "person" else None))
    return {"settings": settings, "counts": counts, "quality_buckets": by_quality}


def analyze_experiment(args: argparse.Namespace) -> int:
    db = connect(args.experiment.resolve())
    init_db(db)
    settings = default_settings()
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
<style>body{font:14px system-ui;margin:0;color:#222}header{padding:12px 18px;background:#243447;color:white}main{display:grid;grid-template-columns:250px 1fr 330px;height:calc(100vh - 64px)}aside,section{overflow:auto;padding:12px;border-right:1px solid #ddd}.photo{padding:8px;border-bottom:1px solid #ddd;cursor:pointer}.photo.selected{background:#dfeeff}.viewer{position:relative;display:inline-block;max-width:100%}.viewer img{max-width:100%;max-height:65vh}.box{position:absolute;border:2px solid #f00;color:#fff;background:#d00b;padding:1px;font-size:12px}.face{padding:8px;border:1px solid #ddd;margin:6px 0}.hidden{display:none}button,select,input{margin:4px;padding:6px}#status{margin-left:20px}.warn{color:#a33}</style></head><body><header><b>Simplified model-settings review</b><button id="reveal">Reveal candidates</button><button id="analyze">Analyze</button><span id="status"></span></header><main><aside><h3>Photos</h3><div id="photos"></div></aside><section><h3 id="title">Select a photo</h3><div id="viewer"></div></section><aside><h3>Faces</h3><div id="faces"></div><hr><h3>People</h3><input id="newperson" placeholder="New person label"><button id="create">Create</button><div id="people"></div></aside></main><script>
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
            if self.path in ("/", "/index.html"):
                static_file = STATIC_DIR / "index.html"
                data = static_file.read_bytes() if static_file.is_file() else HTML.encode()
                self.send_response(200); self.send_header("Content-Type", "text/html"); self.send_header("Content-Length", str(len(data))); self.end_headers(); self.wfile.write(data); return
            if self.path == "/api/summary":
                faces, labels = reviewed(db); self.send_json({"faces": faces, "labeled": labels, "people": db.execute("SELECT COUNT(*) FROM people").fetchone()[0], "missed_faces": db.execute("SELECT COUNT(*) FROM missed_faces").fetchone()[0], "revealed": bool(db.execute("SELECT 1 FROM reveal_events LIMIT 1").fetchone())}); return
            if self.path == "/api/photos":
                rows = db.execute("SELECT i.id,i.relative_path,i.width,i.height,COUNT(f.id) face_count FROM images i LEFT JOIN faces f ON f.image_id=i.id GROUP BY i.id ORDER BY i.relative_path").fetchall(); self.send_json([dict(r) for r in rows]); return
            if self.path == "/api/people": self.send_json([dict(r) for r in db.execute("SELECT id,label FROM people ORDER BY label")]); return
            if self.path == "/api/clusters": self.send_json(provisional_clusters(db)); return
            if self.path.startswith("/api/photo/"):
                image_id = int(self.path.rsplit("/", 1)[1]); image = db.execute("SELECT * FROM images WHERE id=?", (image_id,)).fetchone(); faces=[]
                for f in db.execute("SELECT f.*,l.kind,l.role,l.person_id,p.label person_label,pr.person_label prediction_label,pr.category,pr.similarity,pr.candidates_json FROM faces f LEFT JOIN labels l ON l.face_id=f.id LEFT JOIN people p ON p.id=l.person_id LEFT JOIN predictions pr ON pr.face_id=f.id WHERE f.image_id=? ORDER BY f.face_index", (image_id,)):
                    item=dict(f); item["box"]=[item.pop("x1"),item.pop("y1"),item.pop("x2"),item.pop("y2")]; item["prediction"] = None if not item.pop("prediction_label") else {"person_label": item.pop("prediction_label"),"category":item.pop("category"),"similarity":item.pop("similarity"),"candidates":json.loads(item.pop("candidates_json"))}; faces.append(item)
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
            if self.path == "/api/missed":
                db.execute("INSERT INTO missed_faces(image_id,note,reviewer,created_at) VALUES(?,?,?,?)", (int(payload["image_id"]), payload.get("note"), "local-user", utc_now())); db.commit(); self.send_json({"ok": True}); return
            if self.path == "/api/analyze":
                stamp=datetime.now().strftime("%Y%m%d_%H%M%S"); result=analyze_db(db,default_settings()); (self.root/f"report-{stamp}.json").write_text(json.dumps(result,indent=2)+"\n"); self.send_json({"ok":True}); return
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
    srv=sub.add_parser("serve",help="start local review service"); srv.add_argument("--experiment",type=Path,required=True); srv.add_argument("--host",default="127.0.0.1"); srv.add_argument("--port",type=int,default=8765); srv.set_defaults(func=serve)
    ana=sub.add_parser("analyze",help="write analysis reports"); ana.add_argument("--experiment",type=Path,required=True); ana.add_argument("--similarity-threshold",type=float); ana.add_argument("--margin-threshold",type=float); ana.add_argument("--min-support",type=int); ana.add_argument("--quality-threshold",type=float); ana.add_argument("--sweep",action="store_true",help="evaluate a standard threshold/margin/support/quality grid"); ana.set_defaults(func=analyze_experiment)
    args=parser.parse_args(); return args.func(args)


if __name__ == "__main__": raise SystemExit(main())
