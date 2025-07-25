ARG TARGETOS=linux
ARG TARGETARCH=amd64

FROM golang:alpine AS builder
RUN apk add -U tzdata
WORKDIR /app
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod/ \
    --mount=type=bind,source=go.sum,target=go.sum \
    --mount=type=bind,source=go.mod,target=go.mod \
    go mod download -x
ENV GOCACHE=/root/.cache/go-build
COPY . .
RUN --mount=type=cache,target=/go/pkg/mod/ \
    --mount=type=cache,target="/root/.cache/go-build" \
    CGO_ENABLED=0 \
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
ENV GOGC=100
ENV GOMAXPROCS=4
WORKDIR /app
COPY --from=builder /usr/share/zoneinfo /usr/share/zoneinfo
COPY --from=builder /app/main .
USER 10001:10001
EXPOSE 53/udp 8080
CMD ["./main"]
