# adapter-matrix
Matrix.org Interface for CR45 using mautrix-go

## HTTP Routes

This service does **not** expose an HTTP API.

It runs as a background worker that:

- polls configured outbox tables from PostgreSQL
- transforms supported events into Matrix messages
- sends messages to allow-listed Matrix rooms
- writes delivery state and failures back to adapter tables
