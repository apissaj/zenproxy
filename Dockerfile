# Build stage
FROM golang:1.26-alpine AS builder
WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /zenproxy .

# Runtime stage
FROM alpine:3.21
RUN adduser -D -u 10001 zenproxy
WORKDIR /app
COPY --from=builder /zenproxy /app/zenproxy
COPY config.example.json /app/config.example.json
USER zenproxy
EXPOSE 8000
ENTRYPOINT ["/app/zenproxy"]
CMD ["-port", "8000", "-config", "/app/config.json"]
