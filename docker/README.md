# Optional worker container

The Docker image is an optional worker environment; the normal installation
uses a system Python 3.11 interpreter and a virtual environment.

Build it from the repository root:

```sh
docker build -f docker/Dockerfile -t face-me-bro-worker .
```

The build downloads `buffalo_l` and records checksums in the image. Run it
with the NDJSON protocol on stdin/stdout, or use the Go worker command with a
deployment-specific process wrapper.
