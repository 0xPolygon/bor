# Docker Build and Push Scripts

로컬에서 Bor를 빌드하고 Docker Hub에 푸시하는 스크립트입니다.

## 사용 방법

### 옵션 1: Docker 멀티스테이지 빌드 사용 (권장)

Docker 내부에서 빌드하므로 크로스 컴파일 문제가 없습니다.

```bash
# 기본 사용법
./scripts/docker-build-push.sh

# 버전 지정
VERSION=v1.0.0 ./scripts/docker-build-push.sh

# Docker Hub 사용자명 지정
DOCKER_USERNAME=yourusername VERSION=v1.0.0 ./scripts/docker-build-push.sh
```

### 옵션 2: 로컬 빌드 후 Docker 이미지 생성

로컬에서 바이너리를 먼저 빌드한 후 Docker 이미지를 만듭니다.

```bash
# 기본 사용법
./scripts/build-and-push-docker.sh

# 버전 지정
VERSION=v1.0.0 ./scripts/build-and-push-docker.sh

# Docker Hub 사용자명 지정
DOCKER_USERNAME=yourusername VERSION=v1.0.0 ./scripts/build-and-push-docker.sh
```

**참고:** macOS에서는 linux-amd64 크로스 컴파일을 위해 적절한 툴체인이 필요할 수 있습니다.

## 환경 변수

- `DOCKER_USERNAME`: Docker Hub 사용자명 (기본값: `bbaktaeho`)
- `VERSION`: 이미지 버전 태그 (기본값: git 태그/커밋 해시)

## 생성되는 이미지 태그

스크립트 실행 시 다음 태그들이 생성됩니다:

- `{DOCKER_USERNAME}/bor:{VERSION}`
- `{DOCKER_USERNAME}/bor:latest`
- `{DOCKER_USERNAME}/bor:fix-concurrent-map`

## Docker Hub 로그인

스크립트 실행 중 Docker Hub 로그인이 필요합니다. 아직 로그인하지 않았다면:

```bash
docker login
```

## 이미지 사용

Docker Hub에 푸시된 이미지를 사용하려면:

```bash
# 이미지 pull
docker pull bbaktaeho/bor:v0.0.1-bbak-2

# 실행
docker run bbaktaeho/bor:v0.0.1-bbak-2 --help

# 데몬으로 실행
docker run -d \
  --name bor-node \
  -p 8545:8545 \
  -p 8546:8546 \
  -p 30303:30303 \
  -v /your/data/path:/var/lib/bor \
  bbaktaeho/bor:v0.0.1-bbak-2 \
  server --config /var/lib/bor/config.toml
```

## 문제 해결

### Docker가 실행되지 않음
```bash
# macOS
open -a Docker

# Linux
sudo systemctl start docker
```

### 디스크 공간 부족
```bash
# Docker 정리
docker system prune -af
docker volume prune -f
```

### 빌드 실패
```bash
# 빌드 캐시 삭제 후 재시도
docker builder prune -af
./scripts/docker-build-push.sh
```

