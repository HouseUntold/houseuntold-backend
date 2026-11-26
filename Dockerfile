FROM golang:1.26.11-alpine AS builder

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/venu-api ./cmd/api \
    && CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/venu-migrate ./cmd/migrate

FROM gcr.io/distroless/static-debian12:nonroot

WORKDIR /app

COPY --from=builder /out/venu-api /app/venu-api
COPY --from=builder /out/venu-migrate /app/venu-migrate
COPY --from=builder /src/db/migrations /app/db/migrations

ENV PORT=8080 \
    APP_ENV=production \
    RUN_MIGRATIONS_ON_START=true \
    MIGRATIONS_DIR=/app/db/migrations

EXPOSE 8080

USER nonroot:nonroot

ENTRYPOINT ["/app/venu-api"]
