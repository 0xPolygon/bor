# ─── BUILDER STAGE ───────────────────────────────────────────────────────────────
FROM golang:1.24-alpine AS builder

ARG BOR_DIR=/var/lib/bor/
ENV BOR_DIR=$BOR_DIR

ARG EVMONE_REPO=https://github.com/ethereum/evmone.git
ARG EVMONE_REF=v0.18.0

RUN apk add --no-cache build-base git linux-headers cmake ninja

WORKDIR ${BOR_DIR}

COPY go.mod go.sum ./

RUN --mount=type=ssh \
    --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY . .

RUN --mount=type=ssh \
    --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    make bor

# Build evmone shared library to ship with the runtime image.
WORKDIR /tmp
RUN git clone --branch ${EVMONE_REF} ${EVMONE_REPO} && \
    cd evmone && \
    git submodule update --init --recursive && \
    cmake -S . -B build -G Ninja -DEVMONE_BUILD_SHARED=ON && \
    cmake --build build --target evmone

# ─── RUNTIME STAGE ────────────────────────────────────────────────────────────────
FROM alpine:latest

ARG BOR_DIR=/var/lib/bor/
ENV BOR_DIR=$BOR_DIR
ENV BOR_EVM_SO=/opt/evmone/libevmone.so

RUN apk add --no-cache bash ca-certificates libstdc++ && \
    mkdir -p ${BOR_DIR}

WORKDIR ${BOR_DIR}

COPY --from=builder ${BOR_DIR}/build/bin/bor /usr/bin/
COPY --from=builder /tmp/evmone/build/lib/libevmone.so ${BOR_EVM_SO}

EXPOSE 8545 8546 8547 30303 30303/udp

ENTRYPOINT ["bor"]