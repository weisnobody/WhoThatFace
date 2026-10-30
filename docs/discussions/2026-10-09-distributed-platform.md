# Distributed Platform Considerations

This document describes how a family could share face, person, reference,
and assignment information across multiple computers while keeping original
photos local.

## Recommended architecture

Use a central face/people service with a local processing agent on each
computer:

```text
Computer A                         Computer B
photos + local worker              photos + local worker
        │                                  │
        └──── embeddings, metadata, events ────┐
                                                ▼
                                      Central face service
                                      people, references,
                                      assignments, clusters
```

Each computer:

1. Scans and processes its local photos.
2. Stores or uploads content hashes, photo metadata, face boxes, embeddings,
   pipeline provenance, and local path information.
3. Downloads the central reference gallery.
4. Runs recognition locally against shared reference faces, or sends face
   embeddings to the central service for matching.
5. Uploads recognition results and assignment events.
6. Reprocesses local unconfirmed and provisional faces when the central
   reference gallery changes.

The central service stores people, approved references, assignment history,
permissions, and shared clustering data. It does not need to store original
photos.

## Shared capabilities

### Reference faces

When a reference face is approved on one computer:

- it is added to the central reference gallery;
- other computers receive the updated gallery;
- they rerun recognition against local unconfirmed and provisional faces;
- user-confirmed assignments remain protected;
- reference recommendations and search indexes are refreshed.

### System assignments

A computer can process a local photo and assign a face as `assigned-system`
using references selected on another computer.

The assignment records:

- the central person ID;
- the local face-observation ID;
- similarity and evidence values;
- originating computer;
- model and pipeline ID;
- reference-gallery version used.

### People and names

Person records and names are centralized. Renaming a person on one computer
can therefore propagate to the others.

### Duplicate photos

Central content hashes identify identical files on different computers as the
same asset while retaining separate computer and path records.

## Review availability

A computer cannot review an image that exists only on another computer unless
one of these capabilities is provided:

- the source computer is online and exposes an authenticated image endpoint;
- the source computer uploads thumbnails or review images;
- the original photo library is on shared storage;
- the user reviews the face from the source computer.

The central service may still show face metadata, person candidates,
embeddings, and assignment state while the original image is unavailable.

The review UI should distinguish:

- photo available locally;
- photo available from another online computer;
- photo currently unavailable.

## Clustering across computers

Global clustering is possible, but more involved than shared recognition:

- each computer uploads face embeddings;
- the central service builds clusters across computers;
- local review pages show only photos available on that computer;
- clusters may contain faces whose photos are inaccessible from the current
  computer.

A practical initial approach is local clustering plus central synchronization,
followed later by global clustering.

## Deployment model

The central service could run on:

- a family NAS;
- a home server;
- a dedicated desktop;
- a private cloud VM;
- Docker on a household server.

The service would need:

- authenticated family accounts;
- collection and computer permissions;
- TLS;
- a central database;
- synchronization APIs;
- reference-gallery versioning;
- audit and conflict handling;
- device registration;
- offline queues on each computer.

A SQLite database on a shared network drive is not recommended. Concurrent
access, locking, latency, and failure recovery would be fragile. A central
service should own database access instead.

## Additional data-model requirements

A distributed deployment would add:

- `devices` or `processing_nodes`;
- device-specific collection roots;
- globally unique collection and asset IDs;
- source-device information on analyses and events;
- synchronization cursors or event IDs;
- reference-gallery versions;
- pipeline compatibility per device;
- local asset availability status;
- conflict records;
- last-seen and last-synchronized timestamps.

## Privacy and security

Face embeddings are sensitive biometric-derived data. A central service should
provide:

- encrypted transport;
- encrypted backups;
- per-family-member permissions;
- explicit collection access;
- no raw-photo upload by default;
- optional thumbnail upload only when needed for review;
- audit history for assignments and reference selections.

## Recommended evolution

Distributed support can evolve in stages:

1. Single-computer local database.
2. Central people and reference service with local photo databases.
3. Synchronization of embeddings, assignments, and reference changes.
4. Remote image access for online computers.
5. Global cross-computer clustering.

The key design principle is to centralize identity knowledge while keeping
photo analysis and original images local. Every computer can then benefit from
shared references and person assignments without storing every family photo.
