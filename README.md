# gophprofile

## Local development

Start the full stack (PostgreSQL, RabbitMQ, MinIO, migrations, server, worker,
and the observability stack):

```bash
docker compose up --build
```

This automatically applies database migrations and creates the `avatars`
MinIO bucket. Once the stack is healthy:

- API / web UI: http://localhost:8080 (health check at `/health`, metrics at
  `/metrics`)
- RabbitMQ management UI: http://localhost:15672 (gophprofile / gophprofile)
- MinIO console: http://localhost:9001 (gophprofile / gophprofile123)

Stop everything with `docker compose down` (add `-v` to also drop the
Postgres/MinIO volumes).

## Observability

The stack is instrumented end-to-end with OpenTelemetry tracing,
Prometheus metrics, and structured `log/slog` JSON logging correlated with
trace IDs. The worker exposes its own `/health` and `/metrics` on
http://localhost:9091.

- Jaeger UI (traces): http://localhost:16686
- Prometheus UI (metrics + alert rules): http://localhost:9090
- Alertmanager UI (firing alerts): http://localhost:9093
- Grafana (dashboards + Explore for logs/traces): http://localhost:3000
  (admin / admin) — datasources (Prometheus, Loki, Jaeger) and two
  dashboards ("GophProfile - Service Overview" and "GophProfile - Business
  KPIs") are provisioned automatically.

Logs are shipped by Promtail (reading the Docker socket) into Loki; open
Grafana → Explore → Loki to search them, or follow the "View Trace" link on
a log line's `trace_id` to jump straight to the matching Jaeger trace.