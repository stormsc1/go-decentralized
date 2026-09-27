# Modules that own relational data

Status: decided 2026-09-28, from studying `gogreenlight/platform` (the DDD codebase to port). Built. **Open** marks what's still to decide.

The entity store is a document store with indexes. Modules with real relational data need their own tables, their own SQL and their own migrations. This is about how they get them without the node standing between them and the database.

## Findings: gogreenlight/platform

**Shape.** 109k lines of Go in 948 files. Hexagonal, enforced by `arch_test.go`: `api` (13.7k, huma + chi HTTP and WebSocket) → `application` (33.5k, 43 use-case packages) → `domain` (12k: models, 38 repository interfaces, 6 ports) ← `adapter` (26.7k: `repository/postgres` in 54 files, AI, S3 through gocloud, Firebase auth, mail, a notification hub).

**Database access.** pgx v5 with `pgxpool`, and nothing else: no `database/sql`, no ORM, no sqlc. Hand-written SQL: 322 `QueryRow`, 141 `Query`, 228 `Exec`, 64 transactions, 182 named arguments (`@name`), 6 batches. Postgres-specific throughout: JSONB in 60 files, `gen_random_uuid()`, `FOR UPDATE SKIP LOCKED` queues, recursive CTEs and window functions, an extension, and `LISTEN/NOTIFY` with a dedicated listener connection fanning notifications out to SSE clients.

**Migrations.** goose: 112 timestamped SQL files with `-- +goose Up` and `Down`, one directory, one database. They run as a separate Cloud Run Job (its own image, the DSN from a secret), not at application start. The binary embeds the migrations and refuses to start if `goose_db_version` is behind what it was built against.

**One relational core.** 110 tables and 397 foreign keys, 319 of them across migration files: the `domain` migration (projects and what belongs to them) is referenced 168 times, `identity` (person, user, organization, membership) 92 times. 33 of the 54 repositories join across areas, mostly to `"user"`, `project` and `organization`. Application packages import `core` (28×) and `media` (17×). Tenancy is a foreign key to the project everywhere, resolved per request. The verticals are not independent databases; they are one schema with a shared core, by design.

## What follows

1. **Proxying SQL through the node is out.** A `sql.query` capability over JSON-RPC can't carry this code: it uses pgx's own API (named arguments, batches, a listener connection), not `database/sql`, so a shim wouldn't fit, and re-encoding rows as JSON for 700 query sites would cost type fidelity and put the node on every query's path. The module talks to the database itself, as the platform does today.
2. **A connection string is configuration, not a store.** The node holds nothing and serves nothing for such a database; it passes a string along. Calling that a mounted store dressed configuration up as infrastructure, so it isn't one: modules have an environment instead.
3. **Module boundaries should follow the schema, not the 43 packages.** With the core referenced from everywhere, one module per application package would need cross-module foreign keys and joins on nearly every table.

## Decided

- **Modules have a declared, validated environment.** A module's block in the node definition is its environment; the manifest's `config` schema says what it may hold, and the node checks the block against it before serving the module, the schema-first, fail-at-startup way everything else works. A Postgres connection string is a property of that block (`database_url: ${DATABASE_URL}`), and the module connects with pgx, or any client in any language, and does everything else itself. Nothing new crosses the wire: `module.start` carries the block as it always has. Nodes also tell modules their data directory, so a module keeping a SQLite file of its own knows where.
- **The node's `stores:` are only what the node operates:** entity and kv stores on its drivers, and `local`. A `postgres` driver comes when the node itself needs Postgres, for entity and kv kinds and events across a pool.
- **No isolation between modules sharing a database, for now.** Modules given the same connection string share it. Schemas per module, roles and grants can come later without changing a module's side; so can canary or ring deployments.
- **Migrations are external.** Operators run them, as the platform's Cloud Run Job does today with goose. The node doesn't run, check or know about them. SDK helpers (apply at start under a lock, a startup version check) can come when wanted.
- **The platform is ported as one module first**, `greenlight`: `adapter`, `application` and `domain` unchanged, `api` replaced by capabilities in `module.yaml` (the DTOs' JSON schemas can be generated from the Go types) with thin handlers calling the use cases. Modules get carved along the schema clusters later, `core` first, when there's a reason.

```yaml
# module.yaml
config:
  type: object
  required: [database_url]
  properties:
    database_url: {type: string, format: uri, description: Postgres, for the module's own tables.}

# node definition
modules:
  - name: greenlight
    config: {database_url: "${DATABASE_URL}"}
```

**Pools and Cloud Run.** Every replica gets the same connection string, so nothing changes; `LISTEN/NOTIFY` works per module as it does in the platform's notification hub.

**S3 and blobs.** The same: a bucket and credentials are configuration a module takes; the module uses gocloud or the AWS SDK as it does now. A portable `blob` kind the node operates can come on top later.

## Open

- **Recognising what a property is:** if the node should ever know that a config value is a Postgres it shares with another module (for isolation or pool bookkeeping), a well-known `$ref` in the config schema would tell it, without changing modules.
- **Isolation later:** schema per module with `search_path`, or roles and grants; and whether the node or the module's migrations create schemas.
- **Migration helpers later:** applying at start under a session lock (goose supports it), and refusing to start a module whose schema is behind, as the platform's binary does.
- **Secrets:** connection strings hold passwords; the node reads them from its definition's `${VAR}`s today. A secret store, or platform identity (Cloud SQL IAM), when nodes run where secrets are managed.
