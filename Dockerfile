# Build stage
FROM golang:alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY main.go .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -trimpath -buildvcs=false -tags netgo -o main .
# Run stage
FROM alpine:latest
RUN apk add --no-cache tzdata
# Set the timezone to UTC if not specified
ARG TZ=UTC
ENV TZ=${TZ}
WORKDIR /app
COPY --from=builder /app/main .
RUN adduser -D -u 10001 appuser && chown appuser:appuser /app/main
USER appuser
EXPOSE 53/udp
CMD ["./main"]