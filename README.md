# gophprofile

Сервис загрузки и обработки аватарок. HTTP API принимает файл, сохраняет оригинал в S3, ставит задачу на генерацию миниатюр в очередь. Воркер разбирает очередь и сохраняет превью в S3.

## Запуск локально

Поднимает Postgres, RabbitMQ, MinIO, миграции, сервер, воркер и стек наблюдаемости:

```bash
docker compose up --build
```

Миграции и бакет `avatars` в MinIO применяются автоматически.

- API и веб-интерфейс: http://localhost:8080 (health: `/health`, метрики: `/metrics`)
- RabbitMQ management: http://localhost:15672 (gophprofile / gophprofile)
- MinIO console: http://localhost:9001 (gophprofile / gophprofile123)

Остановка: `docker compose down`. С флагом `-v` также удаляются volume Postgres и MinIO.

## Наблюдаемость

Трейсинг через OpenTelemetry, метрики в Prometheus, структурированные JSON-логи через `log/slog` с привязкой к trace_id. У воркера отдельные `/health` и `/metrics` на http://localhost:9091.

- Jaeger (трейсы): http://localhost:16686
- Prometheus (метрики и алерты): http://localhost:9090
- Alertmanager: http://localhost:9093
- Grafana: http://localhost:3000 (admin / admin). Датасорсы и два дашборда (Service Overview, Business KPIs) настраиваются автоматически.

Логи собирает Promtail и передаёт в Loki. Поиск через Grafana → Explore → Loki. Ссылка "View Trace" в строке лога ведёт на соответствующий трейс в Jaeger.

Алерты в `deploy/prometheus/alerts.yml`: `HighErrorRate` (больше 10% ошибок за 5 минут), `HighResponseTime` (p95 HTTP дольше 5 секунд), `ServiceDown` (Prometheus не может достучаться до сервера или воркера минуту). Смотреть в Alertmanager UI или Prometheus → Alerts.

Стек Prometheus/Grafana/Alertmanager/Loki/Jaeger относится только к docker-compose и локальной разработке. В Kubernetes чарт разворачивает только `ServiceMonitor` для уже установленного в кластере Prometheus Operator. Дашборды и алерт-правила туда не доставляются, их нужно завести отдельно: ConfigMap с лейблом `grafana_dashboard` для дашбордов, `PrometheusRule` для алертов.

### Отказоустойчивость

Вызовы к S3, RabbitMQ и Postgres защищены circuit breaker (`internal/breaker`, поверх `sony/gobreaker`). После 5 подряд неудачных запросов breaker открывается на 30 секунд и сразу отдаёт ошибку без обращения к зависимости, затем пропускает один пробный запрос. Для Postgres результат "запись не найдена" не считается отказом.

Текущее состояние каждого breaker в метрике `circuit_breaker_state` (0 closed, 1 half-open, 2 open).

Retry с backoff у воркера это отдельный механизм: повторяет конкретную задачу из очереди с нарастающей паузой. Breaker защищает саму зависимость от нагрузки, retry обрабатывает временный сбой задачи.

На `/api/v1/*` действует rate limit по IP: 120 запросов в минуту по умолчанию (`RATE_LIMIT_REQUESTS` / `RATE_LIMIT_WINDOW`, в Helm `config.rateLimitRequests` / `config.rateLimitWindow`). `/health`, `/livez` и `/metrics` ограничению не подчиняются.

## API

Спецификация OpenAPI 3.0: [`api/openapi.yaml`](api/openapi.yaml). Сервер отдаёт её же по `/api/v1/openapi.yaml`, можно открыть в [editor.swagger.io](https://editor.swagger.io).

Ручки:

- `GET /health`: readiness, проверяет БД, S3 и брокер
- `GET /livez`: liveness, отвечает 200 если процесс жив, зависимости не проверяет
- `GET /metrics`: метрики для Prometheus
- `POST /api/v1/avatars`: загрузка аватарки
- `GET /api/v1/avatars/{id}`, `GET .../metadata`, `DELETE /api/v1/avatars/{id}`
- `GET /api/v1/users/{id}/avatar(s)`, `DELETE /api/v1/users/{id}/avatar`

## Деплой в Kubernetes

Чарт: [`deploy/helm/gophprofile`](deploy/helm/gophprofile). Дефолтные values рассчитаны на локальный кластер (проверено на [Rancher Desktop](https://rancherdesktop.io/)) и разворачивают вместе с сервисом Postgres/RabbitMQ/MinIO, аналогично `docker compose up`, только в кластере. Для прода: оверлей [`values-prod.yaml`](deploy/helm/gophprofile/values-prod.yaml), отключает bundled-инфру и рассчитывает на managed-сервисы снаружи.

### Что нужно

- Кластер (подходит Rancher Desktop со своим k3s; Kubernetes из Docker Desktop тоже работает, но без ingress-контроллера, см. `port-forward` ниже)
- `kubectl` и [`helm`](https://helm.sh) 3-й версии
- Для автосбора метрик через `serviceMonitor`: CRD от [Prometheus Operator](https://github.com/prometheus-operator/kube-prometheus-stack) в кластере. Если их нет, выставить `serviceMonitor.enabled: false`

### Собрать образ

k3s в Rancher Desktop использует тот же container runtime, что и локальный Docker. Образ, собранный локально, доступен кластеру без push:

```bash
docker build -t gophprofile:local .
```

### Установка

```bash
helm install gophprofile ./deploy/helm/gophprofile \
  --namespace gophprofile --create-namespace
```

Создаёт Deployment/Service/ConfigMap/Secret/ServiceAccount для сервера и воркера, Ingress, HPA, NetworkPolicy и (при наличии CRD) ServiceMonitor. По умолчанию также разворачивает однорепликовые Postgres/RabbitMQ/MinIO для локальной разработки. Перед выкладкой подов сервиса выполняются два hook-джоба: миграции (бинарь `cmd/migrate`) и создание бакета в MinIO.

Добавить `gophprofile.local` (или свой `ingress.host`) в `/etc/hosts` на IP ingress-контроллера кластера, открыть `http://gophprofile.local`. Если ingress-контроллера в кластере нет, пробросить порт:

```bash
kubectl port-forward -n gophprofile svc/gophprofile-server 8080:80
```

### Обновление и снос

```bash
helm upgrade gophprofile ./deploy/helm/gophprofile --namespace gophprofile
helm uninstall gophprofile --namespace gophprofile
```

### Прод

```bash
helm install gophprofile ./deploy/helm/gophprofile \
  --namespace gophprofile --create-namespace \
  -f deploy/helm/gophprofile/values-prod.yaml \
  --set image.tag=<ваш-тег>
```

`values-prod.yaml` отключает bundled-инфру (`postgresql/rabbitmq/minio.enabled: false`). Вместо неё указывается `secret.existingSecret`: имя Secret, заведённого отдельно (Vault, External Secrets, sealed-secrets), содержащего ключи `DATABASE_URL`, `RABBITMQ_URL`, `S3_ACCESS_KEY`, `S3_SECRET_KEY`. Боевые креды в values-файле хранить не следует.

### Масштабирование

У сервера и воркера отдельный HPA: 70% CPU, 80% памяти (сервер 2-10 реплик, воркер 2-8 по умолчанию, меняется в values). `livenessProbe` смотрит на `/livez` и не проверяет БД/брокер/S3. `readinessProbe` бьёт в `/health`, который эти зависимости проверяет. Временная недоступность базы или брокера убирает под из Service без рестарт-лупа.

### Безопасность

- NetworkPolicy: сервер принимает трафик только от ingress-контроллера и namespace с мониторингом, воркер только от мониторинга. Исходящий трафик ограничен DNS, портами БД/брокера/S3 и OTLP. Если БД/брокер живут вне кластера (bundled-инфра выключена), egress-правило разрешает порт 5432/5672 на любой адрес, поскольку NetworkPolicy не фильтрует по DNS-имени. Для ограничения по конкретному managed-инстансу нужно добавить `ipBlock` с его CIDR.
- SecurityContext: контейнеры без root (`runAsNonRoot`), без повышения привилегий, `readOnlyRootFilesystem: true` (кроме `/tmp`, смонтирован как `emptyDir`), без Linux capabilities.
- RBAC: сервису не нужен доступ к Kubernetes API. ServiceAccount не получает Role/RoleBinding и не монтирует токен (`automountServiceAccountToken: false`).
- Secrets: креды передаются только через `Secret` (`envFrom.secretRef`). В ConfigMap и в образ секретные данные не попадают.

### Graceful shutdown

Сервер и воркер обрабатывают `SIGTERM`/`SIGINT`, перестают принимать новую работу и завершают начатые HTTP-запросы и сообщения из очереди с таймаутом 10 секунд перед выходом. Это предотвращает обрыв запросов при rolling update или скейл-дауне.
