# ─── BUILDER STAGE ───────────────────────────────────────────────────────────────
FROM golang:1.24-alpine AS builder

ARG BOR_DIR=/var/lib/bor/
ENV BOR_DIR=$BOR_DIR

RUN apk add --no-cache build-base git linux-headers

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

# ─── RUNTIME STAGE ────────────────────────────────────────────────────────────────
FROM alpine:latest

ARG BOR_DIR=/var/lib/bor/
ENV BOR_DIR=$BOR_DIR

RUN apk add --no-cache bash ca-certificates && \
    mkdir -p ${BOR_DIR}

WORKDIR ${BOR_DIR}

COPY --from=builder ${BOR_DIR}/build/bin/bor /usr/bin/

EXPOSE 8545 8546 8547 30303 30303/udp

ENTRYPOINT ["bor"]
