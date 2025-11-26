# ─── BUILDER STAGE ───────────────────────────────────────────────────────────────
FROM golang:1.24-alpine AS builder

ARG BOR_DIR=/var/lib/bor/
ENV BOR_DIR=$BOR_DIR

RUN apk add --no-cache build-base git linux-headers curl

# Install Rust toolchain for building triedb-ffi
RUN curl --proto '=https' --tlsv1.2 -sSf https://sh.rustup.rs | sh -s -- -y
ENV PATH="/root/.cargo/bin:${PATH}"

WORKDIR ${BOR_DIR}

COPY . .

# Initialize git submodules, download Go dependencies, and build (includes triedb-ffi)
RUN --mount=type=ssh \
    --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/root/.cargo/registry \
    git submodule update --init --recursive && \
    go mod download && \
    make bor

# ─── RUNTIME STAGE ────────────────────────────────────────────────────────────────
FROM alpine:latest

ARG BOR_DIR=/var/lib/bor/
ENV BOR_DIR=$BOR_DIR

RUN apk add --no-cache bash ca-certificates libgcc && \
    mkdir -p ${BOR_DIR}

WORKDIR ${BOR_DIR}

COPY --from=builder ${BOR_DIR}/build/bin/bor /usr/bin/

EXPOSE 8545 8546 8547 30303 30303/udp

ENTRYPOINT ["bor"]