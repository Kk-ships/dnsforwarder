ARG TARGETOS=linux
ARG TARGETARCH=amd64

FROM golang:alpine AS builder
RUN apk add -U tzdata
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 \
    GOOS=${TARGETOS} \
    GOARCH=${TARGETARCH} \
    go build \
        -ldflags="-s -w" \
        -trimpath \
        -buildvcs=false \
        -tags netgo \
        -o main .
# Run stage
FROM scratch AS final
ENV TZ=UTC
WORKDIR /app
COPY --from=builder /usr/share/zoneinfo /usr/share/zoneinfo
COPY --from=builder /app/main .
USER 10001:10001
EXPOSE 53/udp
CMD ["./main"]