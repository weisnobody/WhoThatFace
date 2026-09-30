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


## Structure ##

face_me_bro/
├── AGENTS.md
├── docs/
│   ├── DESIGN.md
│   ├── SPEC.md
│   ├── PLAN.md
│   └── plans.md
│       └── YYYY-MM-DD-description.md 
├── README.md
├── schemas/
│   ├── schema/
│   └── migrations/
├── cli/
│   ├── README.md
│   ├── cmd/
│   └── internal/
├── worker/
│   ├── README.md
│   ├── requirements.txt
│   ├── src/
│   ├── tests/
│   ├── docs/
└── ...


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
