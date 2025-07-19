# Build stage
FROM golang:alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY main.go .
RUN go build -o main .

# Run stage
FROM golang:alpine
WORKDIR /app
COPY --from=builder /app/main .
EXPOSE 53/udp
CMD ["./main"]