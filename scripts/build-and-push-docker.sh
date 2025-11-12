#!/bin/bash

# Build and push Docker image for linux-amd64
set -e

# Color output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# Configuration
DOCKER_USERNAME="${DOCKER_USERNAME:-bbaktaeho}"
IMAGE_NAME="bor"
VERSION="${VERSION:-$(git describe --tags --always)}"

echo -e "${GREEN}=== Building Bor for linux-amd64 ===${NC}"
echo "Version: ${VERSION}"
echo "Docker Image: ${DOCKER_USERNAME}/${IMAGE_NAME}:${VERSION}"
echo ""

# Check if Docker is running
if ! docker info > /dev/null 2>&1; then
    echo -e "${RED}Error: Docker is not running${NC}"
    exit 1
fi

# Build the binary for linux-amd64
echo -e "${YELLOW}Step 1: Building linux-amd64 binary...${NC}"
GOOS=linux GOARCH=amd64 CGO_ENABLED=1 go build \
    -tags netgo \
    -ldflags "-s -w -extldflags '-static'" \
    -o build/bin/bor-linux-amd64 \
    ./cmd/cli/main.go

if [ ! -f build/bin/bor-linux-amd64 ]; then
    echo -e "${RED}Error: Binary build failed${NC}"
    exit 1
fi

echo -e "${GREEN}✓ Binary built successfully${NC}"
echo ""

# Create temporary directory for Docker build
echo -e "${YELLOW}Step 2: Preparing Docker build context...${NC}"
TEMP_DIR=$(mktemp -d)
trap "rm -rf ${TEMP_DIR}" EXIT

cp build/bin/bor-linux-amd64 ${TEMP_DIR}/bor
cp builder/files/genesis-amoy.json ${TEMP_DIR}/
cp builder/files/genesis-mainnet-v1.json ${TEMP_DIR}/
cp builder/files/genesis-testnet-v4.json ${TEMP_DIR}/

# Create Dockerfile
cat > ${TEMP_DIR}/Dockerfile << 'EOF'
FROM alpine:latest

ARG BOR_DIR=/var/lib/bor/
ENV BOR_DIR=$BOR_DIR

RUN apk add --no-cache ca-certificates && mkdir -p ${BOR_DIR}

WORKDIR ${BOR_DIR}
COPY bor /usr/bin/
COPY genesis-amoy.json ${BOR_DIR}
COPY genesis-mainnet-v1.json ${BOR_DIR}
COPY genesis-testnet-v4.json ${BOR_DIR}

EXPOSE 8545 8546 8547 30303 30303/udp

ENTRYPOINT ["bor"]
EOF

echo -e "${GREEN}✓ Docker build context prepared${NC}"
echo ""

# Build Docker image
echo -e "${YELLOW}Step 3: Building Docker image...${NC}"
docker build \
    --platform linux/amd64 \
    -t ${DOCKER_USERNAME}/${IMAGE_NAME}:${VERSION} \
    -t ${DOCKER_USERNAME}/${IMAGE_NAME}:latest \
    -t ${DOCKER_USERNAME}/${IMAGE_NAME}:fix-concurrent-map \
    ${TEMP_DIR}

echo -e "${GREEN}✓ Docker image built successfully${NC}"
echo ""

# Ask for confirmation before pushing
echo -e "${YELLOW}Step 4: Pushing to Docker Hub...${NC}"
echo "The following images will be pushed:"
echo "  - ${DOCKER_USERNAME}/${IMAGE_NAME}:${VERSION}"
echo "  - ${DOCKER_USERNAME}/${IMAGE_NAME}:latest"
echo "  - ${DOCKER_USERNAME}/${IMAGE_NAME}:fix-concurrent-map"
echo ""

read -p "Do you want to push to Docker Hub? (y/N): " -n 1 -r
echo
if [[ ! $REPLY =~ ^[Yy]$ ]]; then
    echo -e "${YELLOW}Push cancelled${NC}"
    exit 0
fi

# Check if logged in to Docker Hub
if ! docker info | grep -q "Username: ${DOCKER_USERNAME}"; then
    echo -e "${YELLOW}Logging in to Docker Hub...${NC}"
    docker login
fi

# Push images
docker push ${DOCKER_USERNAME}/${IMAGE_NAME}:${VERSION}
docker push ${DOCKER_USERNAME}/${IMAGE_NAME}:latest
docker push ${DOCKER_USERNAME}/${IMAGE_NAME}:fix-concurrent-map

echo ""
echo -e "${GREEN}=== Build and Push Completed ===${NC}"
echo ""
echo "Images pushed:"
echo "  - ${DOCKER_USERNAME}/${IMAGE_NAME}:${VERSION}"
echo "  - ${DOCKER_USERNAME}/${IMAGE_NAME}:latest"
echo "  - ${DOCKER_USERNAME}/${IMAGE_NAME}:fix-concurrent-map"
echo ""
echo "To pull the image:"
echo "  docker pull ${DOCKER_USERNAME}/${IMAGE_NAME}:${VERSION}"

