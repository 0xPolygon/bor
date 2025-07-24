FROM golang:1.24-alpine AS builder

ARG BOR_DIR=/var/lib/bor/
ENV BOR_DIR=$BOR_DIR

# Install only essential build dependencies (much faster than apt-get).
RUN apk add --no-cache build-base git linux-headers

WORKDIR ${BOR_DIR}

# Copy go.mod and go.sum first to leverage Docker's cache.
COPY go.mod go.sum ./

# Download dependencies with build cache mount for faster rebuilds.
RUN --mount=type=ssh \
    --mount=type=cache,target=/go/pkg/mod \
    go mod download

# Copy the source code.
COPY . .

# Build with cache mounts and optimized settings.
RUN --mount=type=ssh \
    --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    make bor

# Final stage - minimal runtime image.
FROM alpine:3.22

ARG BOR_DIR=/var/lib/bor/
ENV BOR_DIR=$BOR_DIR

# Install only runtime dependencies.
RUN apk add --no-cache ca-certificates && \
    mkdir -p ${BOR_DIR}

WORKDIR ${BOR_DIR}

# Copy binary from builder stage.
COPY --from=builder ${BOR_DIR}/build/bin/bor /usr/bin/

EXPOSE 8545 8546 8547 30303 30303/udp

ENTRYPOINT ["bor"]
