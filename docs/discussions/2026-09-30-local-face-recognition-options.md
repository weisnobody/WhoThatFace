# Local Face Recognition Options

## Prompt:
9/30/2026, 3:22:34 PM

Is it possible to do face identification and recognition locally?  On MacOS and don't want to use the Apple Photos app.

## Response:
9/30/2026, 3:22:35 PM · gpt-5-6

> **Searched 3 websites**
>
> 
>
> Worked for a couple of seconds

Yes. You can do the entire face-detection/recognition pipeline locally on macOS without Apple Photos and without sending images to a cloud service.

The basic pipeline is:

**photos → detect faces → generate face embeddings → cluster/match embeddings → assign names → store results locally**

Once you name a person, you can use their embeddings to identify that person in other photos.

### Good options on macOS

| Option | Best for | Apple Silicon | Local | UI |
|---|---|---:|---:|---:|
| **InsightFace** | Building your own system | Yes | Yes | Python + optional GUI |
| **Immich** | Apple Photos-like photo library | Yes | Yes | Excellent web UI |
| **face_recognition/dlib** | Simple Python scripts | Mostly | Yes | CLI/Python |
| **CompreFace** | Local recognition API | Via Docker* | Yes | Web/API |

\*CompreFace's documented requirements have historically been more x86-oriented, so it wouldn't be my first choice on an M-series Mac.

### For something you're building: InsightFace

[InsightFace](https://github.com/deepinsight/insightface?utm_source=chatgpt.com) is probably where I'd start.

It provides detection, alignment, and recognition, and its current tooling supports fully local recognition workflows, including comparing a face against a local collection. ([GitHub](https://github.com/deepinsight/insightface?utm_source=chatgpt.com))

Conceptually you'd store something like:

```text
Person
  id
  name

Face
  person_id
  photo_path
  bounding_box
  embedding[512]
```

When scanning a new photograph:

```text
image
  ↓
face detector
  ↓
crop/align each face
  ↓
ArcFace
  ↓
512-dimensional embedding
  ↓
cosine similarity
  ↓
known person / unknown person
```

The useful part is that you're **not actually comparing photographs** after indexing. You're comparing compact numerical embeddings representing the faces.

That means you can index 50,000 photos once and subsequently search the embedding database rather than repeatedly processing all the images.

### If you want an actual Photos replacement: Immich

[Immich](https://immich.app?utm_source=chatgpt.com) is worth a serious look if your goal is less "write face-recognition software" and more "organize my photo collection without Apple Photos."

It automatically detects faces, clusters photographs by person, lets you name people, merges duplicate identities, and lets you browse all photographs containing someone. ([Immich](https://immich.app/docs/features/facial-recognition?utm_source=chatgpt.com))

Everything can be self-hosted.

The experience is basically:

```text
People

Meg             PJ              Unknown
(1,482)         (3,211)         (84)

       ↓ click PJ

2022      2023      2024      2025
[photos containing PJ...]
```

### The older/simple Python route

There's also [face_recognition](https://github.com/ageitgey/face_recognition?utm_source=chatgpt.com). It has a wonderfully simple API:

```python
known = face_recognition.face_encodings(known_image)[0]
unknown = face_recognition.face_encodings(photo)[0]

distance = face_recognition.face_distance(
    [known],
    unknown
)
```

It supports macOS and can identify faces entirely locally. ([GitHub](https://github.com/ageitgey/face_recognition?utm_source=chatgpt.com))

However, it uses **dlib**, and I'd choose InsightFace for a new project. There have also historically been some dlib/Apple-Silicon installation headaches. ([GitHub](https://github.com/ageitgey/face_recognition/issues/1412?utm_source=chatgpt.com))

### An interesting architecture for what I suspect you're after

If you want to manage an existing directory tree rather than importing everything into another photo-management application, I'd build a small local index:

```text
~/Pictures
    ↓
recursive scanner
    ↓
InsightFace
    ├── face detection
    └── face embedding
    ↓
SQLite
    ├── photos
    ├── faces
    ├── people
    └── embeddings
```

Then you'd have commands along the lines of:

```bash
photos scan ~/Pictures

photos people
# 1  PJ       3,421 photos
# 2  Meg      2,182 photos
# 3  Unknown  143 photos

photos identify 3 "John Smith"

photos find "John Smith"

photos find "PJ" --after 2024-01-01
```

You can also **cluster unknown faces automatically**. So rather than manually providing reference pictures for everyone, it can discover:

> "These 417 faces appear to be the same person. Who is this?"

You name that cluster once and those photos become associated with that person.

For a technically comfortable user on an M-series Mac, **InsightFace + SQLite + a small Go/Python application** would give you a very capable completely local system without adopting another photo ecosystem. If you're interested in building that, I can sketch out a particularly nice architecture that keeps the main application in **Go** while isolating the ML portion, including how I'd handle clustering, confidence thresholds, multiple faces per photo, and incremental scanning.

---

**Sources:**

- [GitHub - deepinsight/insightface: State-of-the-art 2D and 3D Face Analysis Project · GitHub](https://github.com/deepinsight/insightface?utm_source=chatgpt.com)
- [Facial Recognition | Immich](https://immich.app/docs/features/facial-recognition?utm_source=chatgpt.com)

## Prompt:
9/30/2026, 3:34:02 PM

If we go with insightface what parts would be in Go and what parts in python?

## Response:
9/30/2026, 3:34:04 PM · gpt-5-6

I’d keep **almost the entire application in Go** and make Python a small, replaceable ML service whose only responsibility is turning images into face embeddings.

The split would look like this:

```text
┌─────────────────────────────── GO ───────────────────────────────┐
│                                                                  │
│  CLI / UI                                                        │
│     │                                                            │
│     ▼                                                            │
│  Photo Scanner ──────── File metadata / hashes / EXIF            │
│     │                                                            │
│     ▼                                                            │
│  Recognition Client ─────────────────────┐                       │
│                                         │                       │
│  SQLite                                 │                       │
│   ├── photos                            │                       │
│   ├── faces                             │                       │
│   ├── people                            │                       │
│   ├── embeddings                        │                       │
│   └── scan state                        │                       │
│                                         │                       │
│  Matching / clustering / identification │                       │
│                                         │                       │
└─────────────────────────────────────────┼───────────────────────┘
                                          │
                                local IPC │
                                          ▼
┌─────────────────────────── PYTHON ───────────────────────────────┐
│                                                                  │
│  InsightFace                                                     │
│     │                                                            │
│     ├── load image                                               │
│     ├── detect faces                                             │
│     ├── align faces                                              │
│     └── generate embeddings                                      │
│                                                                  │
└──────────────────────────────────────────────────────────────────┘
```

### Python: keep it deliberately tiny

Python exists because InsightFace and ONNX Runtime have excellent Python support. I wouldn't let Python own the photo library, database, people, or matching logic.

Give it essentially one operation:

```python
analyze_image("/photos/IMG_1234.jpg")
```

returning something like:

```json
{
  "width": 4032,
  "height": 3024,
  "faces": [
    {
      "bbox": [1240, 422, 1688, 1012],
      "confidence": 0.9987,
      "embedding": [0.0123, -0.1842, 0.0341, "...512 values..."]
    },
    {
      "bbox": [2211, 510, 2550, 971],
      "confidence": 0.9912,
      "embedding": ["..."]
    }
  ]
}
```

Python doesn't need to know that the first face is "PJ" or that you've seen it 8,000 times before.

Its contract is simply:

> Here's an image. Tell me where the faces are and give me the embedding for each face.

That separation will make the project much easier to maintain.

## Go owns everything else

I'd put all application logic in Go.

For example:

```text
cmd/
  photos/
    main.go

internal/
  scanner/
    scanner.go

  photos/
    photo.go
    exif.go
    hash.go

  faces/
    face.go
    matcher.go
    cluster.go

  people/
    person.go
    service.go

  recognition/
    client.go
    types.go

  database/
    sqlite.go
    photos.go
    faces.go
    people.go

  thumbnail/
    thumbnail.go
```

Go handles directory traversal, change detection, hashing, EXIF, concurrency, database access, and the user-facing CLI/UI.

It also handles **matching**.

InsightFace gives us a normalized 512-dimensional vector:

```go
type Embedding []float32

type Face struct {
    ID         int64
    PhotoID    int64
    PersonID   *int64
    X1         int
    Y1         int
    X2         int
    Y2         int
    Confidence float32
    Embedding  Embedding
}
```

Then matching is just vector math:

```go
func CosineSimilarity(a, b []float32) float32 {
    var dot, normA, normB float32

    for i := range a {
        dot += a[i] * b[i]
        normA += a[i] * a[i]
        normB += b[i] * b[i]
    }

    return dot / (sqrt(normA) * sqrt(normB))
}
```

So there's no reason to cross into Python every time you want to ask:

```text
Does this face look like PJ?
```

We only need Python when generating a **new embedding**.

### Communication between Go and Python

I'd avoid HTTP initially.

Instead, I'd make the Python process a long-running worker and communicate over stdin/stdout using newline-delimited JSON:

```text
Go
 │
 │ {"id":42,"command":"analyze","path":"/photos/a.jpg"}
 │
 ▼
Python / InsightFace
 │
 │ {"id":42,"faces":[...]}
 │
 ▼
Go
```

Go launches it:

```go
cmd := exec.Command(
    "python3",
    "recognizer.py",
)

stdin, _ := cmd.StdinPipe()
stdout, _ := cmd.StdoutPipe()

cmd.Start()
```

The important thing is that the Python process stays alive.

You **don't** want this:

```text
photo
  ↓
start python
  ↓
load InsightFace model
  ↓
analyze
  ↓
exit

repeat 40,000 times
```

Model startup is expensive.

Instead:

```text
start application
       ↓
start Python worker
       ↓
load InsightFace once
       ↓
 ┌──────────────────┐
 │ photo 1          │
 │ photo 2          │
 │ photo 3          │
 │ ...              │
 │ photo 50,000     │
 └──────────────────┘
       ↓
shutdown
```

We could eventually run multiple workers if throughput warrants it.

## SQLite

I'd probably start with four core tables:

```sql
CREATE TABLE photos (
    id          INTEGER PRIMARY KEY,
    path        TEXT UNIQUE NOT NULL,
    file_hash   TEXT,
    modified_at INTEGER,
    width       INTEGER,
    height      INTEGER
);

CREATE TABLE people (
    id      INTEGER PRIMARY KEY,
    name    TEXT NOT NULL
);

CREATE TABLE faces (
    id          INTEGER PRIMARY KEY,
    photo_id    INTEGER NOT NULL,
    person_id   INTEGER,
    x1          INTEGER,
    y1          INTEGER,
    x2          INTEGER,
    y2          INTEGER,
    confidence  REAL,

    FOREIGN KEY(photo_id) REFERENCES photos(id),
    FOREIGN KEY(person_id) REFERENCES people(id)
);

CREATE TABLE embeddings (
    face_id     INTEGER PRIMARY KEY,
    embedding   BLOB NOT NULL,

    FOREIGN KEY(face_id) REFERENCES faces(id)
);
```

A 512-element `float32` embedding is only:

```text
512 × 4 bytes = 2 KB
```

Even:

```text
100,000 faces ≈ 195 MB
```

isn't particularly concerning.

And storing them as binary blobs is considerably nicer than JSON arrays.

## One thing I'd change from my earlier description: matching vs clustering

I'd actually divide those two responsibilities.

**Go:** recognition/matching against known people.

```text
new face
   ↓
embedding
   ↓
compare against known-person prototypes
   ↓
PJ        0.81
Meg       0.43
John      0.31
   ↓
PJ
```

**Python:** initially, I'd use Python for large-scale clustering of unknown faces.

Libraries like NumPy/scikit-learn make experimentation with DBSCAN/HDBSCAN much easier.

So we'd have two Python operations:

```text
analyze_image()
cluster_embeddings()
```

Once we're happy with the clustering algorithm, we could move it to Go if there's any benefit.

### The boundary I'd use

That gives us a very clean rule:

> **Python does ML/data-science operations. Go owns the application.**

Specifically:

| Capability | Go | Python |
|---|:---:|:---:|
| Scan directories | ✓ | |
| Detect new/changed files | ✓ | |
| EXIF | ✓ | |
| Hashing | ✓ | |
| SQLite | ✓ | |
| People/names | ✓ | |
| Face database | ✓ | |
| Search | ✓ | |
| CLI/UI | ✓ | |
| Similarity matching | ✓ | |
| Confidence decisions | ✓ | |
| InsightFace model | | ✓ |
| Face detection | | ✓ |
| Face alignment | | ✓ |
| Embedding generation | | ✓ |
| Experimental clustering | | ✓ |

The nice consequence is that **InsightFace isn't really baked into the application**. Go has a generic `Recognizer` interface:

```go
type Recognizer interface {
    Analyze(ctx context.Context, path string) ([]DetectedFace, error)
}
```

Today:

```text
Recognizer
    └── PythonInsightFaceRecognizer
```

Later we could potentially have:

```text
Recognizer
    ├── PythonInsightFaceRecognizer
    ├── ONNXRecognizer
    └── CoreMLRecognizer
```

And that last possibility is interesting on macOS: once the system works, we could investigate running the underlying ONNX models **directly from Go**, potentially eliminating Python entirely. I wouldn't start there, though. Python + InsightFace gives us a known-good baseline against which a native implementation can be tested.

Given the Go work you've been doing lately, I'd strongly favor this architecture over making Python the primary application. It leaves Python as roughly a **100-200 line ML adapter** rather than turning the project into a mixed Go/Python codebase.

## Prompt:
9/30/2026, 3:39:00 PM

That all makes sense.  How do I take this conversation and move it into the codex cli so we can work on the spec together?

## Response:
9/30/2026, 3:39:02 PM · gpt-5-6

> **Searched 1 website**
>
> 
>
> Worked for a couple of seconds

Yes. The cleanest way is to **handoff the design context as a Markdown file in the new repo**, rather than trying to transfer ChatGPT's internal conversation state directly into Codex CLI.

Codex CLI can resume its *own* saved conversations with `codex resume`, but that refers to conversations created in Codex, not this ChatGPT conversation. OpenAI's guidance also recommends keeping persistent project context in files such as `AGENTS.md` and a specification/plan file. ([OpenAI Developers](https://developers.openai.com/fr-FR/docs/projects?utm_source=chatgpt.com))

For this project, I'd structure it like:

```text
face-library/
├── AGENTS.md
├── SPEC.md
├── README.md
├── cmd/
├── internal/
├── python/
└── ...
```

I would put **the decisions we've already made into `SPEC.md`**, rather than dumping the raw conversation into Codex. For example, the initial document would capture:

```markdown
# Local Face Recognition Project

## Goal

Build a completely local macOS application for indexing photographs,
detecting faces, identifying people, and searching a photo collection.

No cloud-based face recognition services.
Apple Photos is not used.

## Initial Architecture

The application consists primarily of a Go application with a small
Python ML worker.

### Go responsibilities

- CLI / application lifecycle
- directory scanning
- incremental file discovery
- EXIF extraction
- image hashing
- SQLite persistence
- photo metadata
- people management
- face records
- embedding storage
- similarity matching
- confidence decisions
- search
- orchestration of Python worker

### Python responsibilities

Python is an ML adapter, not the primary application.

- InsightFace model loading
- face detection
- face alignment
- embedding generation
- experimental clustering

Python should not own:
- SQLite
- photo metadata
- people
- identity assignments
- application state

## Go/Python Interface

Python runs as a persistent subprocess.

Communication initially uses newline-delimited JSON over stdin/stdout.

Go sends:

{
  "id": 42,
  "command": "analyze",
  "path": "/photos/IMG_1234.jpg"
}

Python returns:

{
  "id": 42,
  "width": 4032,
  "height": 3024,
  "faces": [...]
}

Each face contains:

- bounding box
- detection confidence
- 512-dimensional embedding

## Design Principle

Go owns the application.
Python owns ML operations.

The Python component should remain small and replaceable.

Define a Go abstraction similar to:

type Recognizer interface {
    Analyze(ctx context.Context, path string) ([]DetectedFace, error)
}

The initial implementation will be an InsightFace Python worker.

A future implementation may invoke ONNX models directly and eliminate
the Python dependency.

## Storage

SQLite.

Initial entities:

- photos
- faces
- people
- embeddings
- scan state

Embeddings should be stored as binary float32 data rather than JSON.

## Recognition

Similarity matching belongs in Go.

Initially use cosine similarity.

Do not automatically assign an identity solely because it is the
closest match. Recognition must support configurable confidence /
distance thresholds and an "unknown" result.

## Clustering

Initial experimentation with clustering unknown faces can occur in
Python using appropriate scientific Python libraries.

The clustering implementation can later move to Go if desirable.

## Platform

Primary target:

- macOS
- Apple Silicon
- completely local processing
- no Apple Photos dependency
- no cloud processing

## Current Phase

DESIGN ONLY.

Do not begin implementation until the specification has been reviewed
and explicitly approved.

We are currently defining:

1. requirements
2. architecture
3. data model
4. recognition workflow
5. clustering workflow
6. CLI / UX
7. performance requirements
8. privacy requirements
9. test strategy
10. implementation milestones
```

Then initialize the directory and start Codex there:

```bash
mkdir face-library
cd face-library

# save the above as SPEC.md

git init
codex
```

Your **first Codex prompt** could simply be:

```text
Read SPEC.md.

This specification originated from an architectural discussion I had
in ChatGPT.

Do not implement anything yet.

I want to work with you interactively to turn SPEC.md into a complete
technical specification.

First:

1. Review the architecture.
2. Identify important decisions we haven't made yet.
3. Identify risks or assumptions we should validate with prototypes.
4. Propose the sections that the completed SPEC.md should contain.

We'll work through those decisions together before writing code.
```

That last part is important. It prevents Codex from enthusiastically deciding that "review the spec" means "create 37 files." OpenAI's current Codex guidance specifically recommends freezing goals, non-goals, constraints, deliverables, and "done when" criteria in the specification before longer implementation work. ([OpenAI Developers](https://developers.openai.com/blog/run-long-horizon-tasks-with-codex?utm_source=chatgpt.com))

I'd also eventually create an `AGENTS.md`. Codex CLI automatically discovers `AGENTS.md` files and injects their instructions into its context, making it the right place for durable rules such as Go conventions, testing requirements, Python's architectural boundary, and "don't put application logic in the Python worker." ([OpenAI Developers](https://developers.openai.com/api/docs/guides/latest-model?gallery=open&galleryItem=trivia-quiz-game&model=gpt-5.3-codex&translationFallback=de-DE&utm_source=chatgpt.com))

**I wouldn't create `AGENTS.md` yet.** I'd have us finish `SPEC.md` first, then derive `AGENTS.md` from the decisions in the spec. That keeps architectural requirements separate from instructions about *how Codex should work on the repository*.

If you want, I can create a **much more complete `SPEC.md` handoff from everything we've discussed so far**, including an initial set of open design questions. You could drop that directly into a new directory and make it the starting point for the Codex session.

---

**Sources:**

- [Projets et discussions | ChatGPT Learn](https://developers.openai.com/fr-FR/docs/projects?utm_source=chatgpt.com)



---
Powered by [ChatGPT Exporter](https://www.chatgptexporter.com)