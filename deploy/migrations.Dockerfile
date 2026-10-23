# Build context: repository root (docker build -f deploy/migrations.Dockerfile .)
FROM migrate/migrate:v4.20.1@sha256:76cc2074cb6642631f34a898ced71e6aeaa6b1a4d78c4daa743275a22e0c5be7
COPY services/database_middleware/migrations /migrations
