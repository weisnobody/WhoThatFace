# Database schema

The ordered SQL files in `schemas/migrations/` are the canonical schema
history. Each migration is immutable after it has been applied. The current
schema is the result of applying every migration in order.

The MVP uses SQLite. SQL should remain within the portable subset needed for a
future PostgreSQL backend; backend-specific changes belong in explicit,
versioned migrations.
