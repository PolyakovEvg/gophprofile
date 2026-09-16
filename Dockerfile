# Build stage
FROM golang:1.26-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o server ./cmd/server
RUN CGO_ENABLED=0 GOOS=linux go build -o worker ./cmd/worker
RUN CGO_ENABLED=0 GOOS=linux go build -o migrate ./cmd/migrate

# Runtime stage
FROM alpine:latest
RUN apk --no-cache add ca-certificates tzdata \
    && addgroup -S gophprofile \
    && adduser -S -G gophprofile -H -h /app gophprofile

WORKDIR /app
COPY --from=builder --chown=gophprofile:gophprofile /app/server .
COPY --from=builder --chown=gophprofile:gophprofile /app/worker .
COPY --from=builder --chown=gophprofile:gophprofile /app/migrate .
COPY --from=builder --chown=gophprofile:gophprofile /app/web ./web/
COPY --from=builder --chown=gophprofile:gophprofile /app/api ./api/

USER gophprofile:gophprofile

EXPOSE 8080

CMD ["./server"]
