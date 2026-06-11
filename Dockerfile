# Build stage
FROM golang:1.24-alpine AS builder
WORKDIR /app
COPY src/go.mod ./
RUN go mod download
COPY src/ .
RUN CGO_ENABLED=0 GOOS=linux go build -o proxy .

# Run stage
FROM alpine:3.20
RUN apk --no-cache add ca-certificates curl
WORKDIR /app
COPY --from=builder /app/proxy .
EXPOSE 11111
ENV PORT=11111
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
  CMD curl -f http://localhost:${PORT}/health || exit 1
CMD ["./proxy"]
