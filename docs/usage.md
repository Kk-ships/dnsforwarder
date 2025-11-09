# Usage

## 1. Run with Docker

### Build the Docker image

```sh
docker build -t dnsforwarder .
```

### Run the container

```sh
docker run --rm -p 53:53/udp -p 8080:8080 --env-file .env dnsforwarder
```

## 2. Run with Docker Compose (Recommended)

You can also use Docker Compose for easy deployment.

Start with:

```sh
docker compose up --build
```
