# Build stage
FROM golang:alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY main.go .
RUN go build -o main .

# Run stage
FROM alpine:latest
WORKDIR /app
COPY --from=builder /app/main .
RUN adduser -D -u 10001 appuser && chown appuser:appuser /app/main
USER appuser
EXPOSE 53/udp
CMD ["./main"]