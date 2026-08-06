# gophprofile

## Local development

Start the full stack (PostgreSQL, RabbitMQ, MinIO, migrations, server, worker):

```bash
docker compose up --build
```

This automatically applies database migrations and creates the `avatars`
MinIO bucket. Once the stack is healthy:

- API / web UI: http://localhost:8080 (health check at `/health`)
- RabbitMQ management UI: http://localhost:15672 (gophprofile / gophprofile)
- MinIO console: http://localhost:9001 (gophprofile / gophprofile123)

Stop everything with `docker compose down` (add `-v` to also drop the
Postgres/MinIO volumes).