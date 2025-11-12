#!/bin/bash

# Build and push Docker image using multi-stage Docker build
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

echo -e "${GREEN}=== Building and Pushing Bor Docker Image ===${NC}"
echo "Version: ${VERSION}"
echo "Docker Image: ${DOCKER_USERNAME}/${IMAGE_NAME}:${VERSION}"
echo ""

# Check if Docker is running
if ! docker info > /dev/null 2>&1; then
    echo -e "${RED}Error: Docker is not running${NC}"
    exit 1
fi

# Build Docker image using the existing Dockerfile
echo -e "${YELLOW}Building Docker image...${NC}"
docker build \
    --platform linux/amd64 \
    -t ${DOCKER_USERNAME}/${IMAGE_NAME}:${VERSION} \
    -t ${DOCKER_USERNAME}/${IMAGE_NAME}:latest \
    -t ${DOCKER_USERNAME}/${IMAGE_NAME}:fix-concurrent-map \
    -f Dockerfile \
    .

echo -e "${GREEN}✓ Docker image built successfully${NC}"
echo ""

# Ask for confirmation before pushing
echo -e "${YELLOW}Ready to push to Docker Hub${NC}"
echo "The following images will be pushed:"
echo "  - ${DOCKER_USERNAME}/${IMAGE_NAME}:${VERSION}"
echo "  - ${DOCKER_USERNAME}/${IMAGE_NAME}:latest"
echo "  - ${DOCKER_USERNAME}/${IMAGE_NAME}:fix-concurrent-map"
echo ""

read -p "Do you want to push to Docker Hub? (y/N): " -n 1 -r
echo
if [[ ! $REPLY =~ ^[Yy]$ ]]; then
    echo -e "${YELLOW}Push cancelled${NC}"
    echo ""
    echo "Images are available locally:"
    echo "  docker run ${DOCKER_USERNAME}/${IMAGE_NAME}:${VERSION}"
    exit 0
fi

# Check if logged in to Docker Hub
echo -e "${YELLOW}Checking Docker Hub login...${NC}"
if ! docker info 2>/dev/null | grep -q "Username"; then
    echo -e "${YELLOW}Please login to Docker Hub...${NC}"
    docker login
fi

# Push images
echo -e "${YELLOW}Pushing images...${NC}"
docker push ${DOCKER_USERNAME}/${IMAGE_NAME}:${VERSION}
docker push ${DOCKER_USERNAME}/${IMAGE_NAME}:latest
docker push ${DOCKER_USERNAME}/${IMAGE_NAME}:fix-concurrent-map

echo ""
echo -e "${GREEN}=== Build and Push Completed ===${NC}"
echo ""
echo "Images pushed to Docker Hub:"
echo "  - ${DOCKER_USERNAME}/${IMAGE_NAME}:${VERSION}"
echo "  - ${DOCKER_USERNAME}/${IMAGE_NAME}:latest"
echo "  - ${DOCKER_USERNAME}/${IMAGE_NAME}:fix-concurrent-map"
echo ""
echo "To pull and run the image:"
echo "  docker pull ${DOCKER_USERNAME}/${IMAGE_NAME}:${VERSION}"
echo "  docker run ${DOCKER_USERNAME}/${IMAGE_NAME}:${VERSION} --help"

