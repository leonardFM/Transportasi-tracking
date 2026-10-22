# Dockerfile untuk seluruh service Go pada Transjakarta Fleet.
#
# Satu image dipakai untuk tiga binary (api, worker, publisher). Service mana
# yang dijalankan dipilih lewat `command:` pada docker-compose.yml.
#
#   docker build -t transjakarta-fleet .
#   docker run --rm transjakarta-fleet /app/worker

FROM golang:1.23-alpine AS builder

WORKDIR /src

# Download dependency lebih dulu agar layer cache sering terpakai.
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Build ketiga binary
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/api       ./cmd/api && \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/worker    ./cmd/worker && \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/publisher ./cmd/publisher

# Final stage
FROM alpine:3.20

RUN apk --no-cache add ca-certificates tzdata

WORKDIR /app

COPY --from=builder /out/api       /app/api
COPY --from=builder /out/worker    /app/worker
COPY --from=builder /out/publisher /app/publisher

ENV TZ=Asia/Jakarta

EXPOSE 8000

CMD ["/app/api"]
