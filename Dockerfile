FROM golang:latest AS build
WORKDIR /go/src
COPY . .

# Install build dependencies for sqlite3
RUN apt-get update && apt-get install -y \
    ca-certificates \
    gcc \
    libc6-dev \
    && rm -rf /var/lib/apt/lists/*

# Enable CGO for sqlite3 support
ENV CGO_ENABLED=1

RUN go get
RUN go build -o app .

FROM ubuntu:latest AS runtime

# Install runtime dependencies
RUN apt-get update && apt-get install -y \
    ca-certificates \
    && rm -rf /var/lib/apt/lists/*

COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=build /go/src/app ./
RUN mkdir -p /media

ENTRYPOINT ["./app"]
