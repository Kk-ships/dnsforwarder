# Build stage
FROM golang:alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY main.go .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -trimpath -buildvcs=false -tags netgo -o main .
# Run stage
FROM scratch AS final
ENV TZ=UTC
WORKDIR /app
COPY --from=builder /app/main .
USER 10001:10001
EXPOSE 53/udp
CMD ["./main"]