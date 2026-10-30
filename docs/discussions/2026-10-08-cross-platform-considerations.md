# Cross-Platform Considerations

The current specification targets macOS on Apple Silicon, but most of the
application architecture is portable. The recommended approach is to retain a
portable application core and isolate operating-system behavior behind platform
adapters.

## Platform-specific assumptions in the current specification

| Area | Current macOS assumption | Linux considerations | Windows considerations |
| --- | --- | --- | --- |
| Supported platform | macOS on Apple Silicon | Define supported CPU and architecture, likely x86-64 and ARM64 | Define supported x86-64 and ARM64 hardware |
| ML acceleration | CoreML with CPU fallback | CPU, CUDA, ROCm, or another supported ONNX Runtime provider | CPU, CUDA, DirectML, or another supported ONNX Runtime provider |
| Benchmark results | M1/M4 throughput, memory, and output comparisons | Run separate hardware/provider benchmarks | Run separate hardware/provider benchmarks |
| Runtime data path | `~/Library/Application Support/face_me_bro/` | Usually an XDG data directory such as `~/.local/share/face_me_bro/` | Usually `%LOCALAPPDATA%\\face_me_bro\\` |
| Configuration path | macOS application configuration conventions | XDG configuration paths or an explicit configuration file | `%APPDATA%` or `%LOCALAPPDATA%`, plus the explicit configuration-file option |
| Installation | System Python plus a virtual environment | Same general approach, with platform-specific Python and ML wheels | Same general approach, with platform-specific Python and ML wheels |
| Code signing | Apple signing and notarization before external distribution | Package signing or repository-specific distribution mechanisms | Authenticode, MSIX, or another Windows distribution mechanism |
| Activity detection | macOS-specific activity, thermal, power, and load signals | Linux desktop, power, thermal, and load signals vary by distribution | Windows foreground, power, thermal, and load APIs differ from macOS |
| File metadata | macOS file creation/birth timestamps and filesystem behavior | Creation time may be unavailable or filesystem-dependent | Creation time is generally available, but semantics vary by filesystem |
| Filesystem paths | POSIX paths and permissions | POSIX paths, permissions, and filesystem differences | Drive letters, UNC paths, ACLs, reserved names, and case behavior |

## Portable application core

The following components should remain platform-independent:

- Go application services and job orchestration.
- Python worker protocol.
- SQLite schema, migrations, and transaction rules.
- SHA-256 content identity and asset/path deduplication.
- Face, embedding, identity, reference, and clustering data models.
- Recognition thresholds and assignment states.
- Mutual-k-nearest-neighbor grouping.
- Reference recommendations and review history.
- Web review service behavior and authorization rules.
- Export, backup, and migration semantics.
- Local-only processing and privacy requirements.

Platform adapters should provide operating-system behavior without changing
these application-level contracts.

## ML execution providers

The current benchmark uses CoreML with CPU fallback on Apple Silicon. That is
an execution-provider choice, not a requirement of the recognition or
grouping design.

Each supported platform must define and validate its provider policy. A
provider selection should include, at minimum:

- provider name and version;
- hardware and architecture;
- model-file checksums;
- preprocessing and detector configuration;
- embedding dimension, data type, and normalization;
- numerical compatibility results against the reference provider;
- throughput, latency, and peak memory measurements.

The provider and relevant hardware configuration belong in the pipeline ID.
Different providers may produce small numerical differences, so each provider
requires compatibility and calibration testing before being treated as a
production baseline.

Potential provider categories include:

- macOS Apple Silicon: CoreML and CPU fallback;
- Linux: CPU, CUDA, ROCm, or another supported provider appropriate to the
  installed hardware;
- Windows: CPU, CUDA, DirectML, or another supported provider appropriate to
  the installed hardware.

The exact provider matrix should be defined only for platforms the project
chooses to support. CPU fallback remains useful for correctness testing and
for systems without a supported accelerator.

## Runtime directories and configuration

The application should expose platform-neutral configuration concepts:

- runtime data root;
- database path;
- model directory;
- log directory;
- temporary directory;
- export directory;
- backup directory.

Platform defaults may differ, but the CLI option for selecting an alternate
configuration file or location must work on every supported platform. That
configuration may set an alternate runtime data root, including an external
drive or network-mounted location where the filesystem behavior is supported.

Status and diagnostic commands should report the resolved configuration file,
runtime data root, database path, model path, and active pipeline. This avoids
ambiguity when multiple installations or configuration files exist.

## Filesystem and path handling

The core should avoid assuming that paths are simple POSIX strings. The
platform adapter should handle:

- absolute and relative paths;
- path normalization without changing user-visible relative paths;
- case-sensitive and case-insensitive filesystems;
- Unicode normalization differences;
- Windows drive letters and UNC paths;
- permissions and access-denied errors;
- path length limits and reserved names on Windows;
- file replacement and rename behavior;
- symlink and junction policy.

The MVP policy of ignoring symlinks should be expressed through the scanner
abstraction so that Windows junctions and other reparse-point behavior receive
the equivalent treatment.

Content hashes remain the identity mechanism. Path comparison must not be
used as a substitute for content identity across platforms.

## Capture-date portability

Embedded capture date remains the preferred source. A portable fallback order
should be:

1. Embedded capture date.
2. Metadata from the reference still image in a supported motion-photo
   container.
3. Filesystem creation/birth time when reliably available.
4. Filesystem modification time as the final fallback.

The selected date and its source should be stored or derivable so users can
understand why a capture date was chosen. Creation time is not equally
available or reliable on every Linux filesystem, so the fallback must record
when the application had to use modification time instead.

## Live Photos and motion photos

iOS Live Photos and Android-style motion photos are not inherently tied to the
operating system running `face_me_bro`. They are input-format concerns.

The MVP should process only the reference still-image component and ignore the
motion/video component. A portable container/parser layer should:

- identify supported still-image components from the container or file pair;
- preserve the asset/path relationship;
- use the still image for metadata and face analysis;
- record failures when the still component cannot be extracted;
- avoid processing video or animated frames in the MVP.

RAW, video, and animated-image processing remain future extensions on every
platform.

## Background resource control

Automatic throttling is portable as a policy but not as a single
implementation. Each platform adapter may use different signals for:

- recent user activity;
- foreground application activity;
- CPU and GPU load;
- thermal pressure;
- power source and battery state;
- sleep or system-suspend state.

When signals are unavailable or unreliable, the application must expose the
manual full-speed, reduced, and paused controls. Throttling changes throughput
only and must not change analysis, recognition, or grouping results.

## Installation and process management

The default environment remains system Python plus a project virtual
environment on all supported platforms. The setup process must account for:

- platform-specific Python installation and discovery;
- platform-specific dependency wheels;
- model download and checksum verification;
- execution-provider packages;
- subprocess startup and termination behavior;
- environment variables and dynamic-library loading;
- offline runtime after setup.

The NDJSON worker protocol should remain unchanged. Only worker startup,
executable discovery, environment setup, process termination, and diagnostics
should require platform-specific code.

Docker may provide a reproducible Linux environment and may be useful for
development or server deployment. It does not eliminate the need to validate
native macOS and Windows installations if those platforms are supported.

## Packaging and distribution

Code signing and notarization are deferred for the personal-use MVP. Before
external distribution, each platform needs its own release process:

- macOS signing, entitlements, notarization, and Gatekeeper validation;
- Linux package, archive, or container distribution and optional package
  signing;
- Windows executable/package signing, installation, and Defender/SmartScreen
  validation.

The model and dependency license review applies to every distribution target.

## Testing requirements for additional platforms

Adding Linux or Windows support should require platform-specific validation of:

- clean setup and virtual-environment creation;
- dependency and model installation;
- offline processing;
- supported still-image and motion-photo decoding;
- EXIF orientation and capture-date fallback;
- path scanning, duplicate handling, missing paths, and renames;
- worker startup, timeout, cancellation, and restart;
- numerical embedding compatibility;
- recognition and grouping calibration;
- memory, throughput, and review-service latency;
- backup, restore, and migration behavior;
- resource throttling and manual overrides.

The macOS M1/M4 benchmark results must not be used as performance guarantees
for Linux or Windows. Each platform/provider combination requires its own
benchmark record.

## Recommended support strategy

The project should first keep macOS on Apple Silicon as the validated MVP
platform. Linux and Windows can then be added by:

1. preserving the portable Go, SQLite, worker protocol, and data-model layers;
2. adding platform adapters for paths, runtime directories, metadata, process
   control, activity detection, and execution providers;
3. defining a support matrix for hardware and operating-system versions;
4. running the platform-specific setup, compatibility, and performance tests;
5. assigning a pipeline ID to each validated ML provider/configuration.

This avoids creating separate application architectures while making platform
limitations explicit and testable.
