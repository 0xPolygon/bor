#!/usr/bin/env bash
set -euo pipefail
umask 077

# Approved Polygon private-release primary and signing-subkey fingerprints.
readonly EXPECTED_GPG_FINGERPRINT="79E0B57D414727B48F3F9BA3759DA68F40672C2C"
readonly SIGNING_SUBKEY_FINGERPRINT="36839E0B2513D894CA7C90F4474EB7D4682EE64D"

# These product locations are maintained by Polygon, not supplied by the node
# operator.
readonly BOR_BASE_URI="https://storage.googleapis.com/pos-mainnet-snapshots/bor-private"
readonly HEIMDALL_BASE_URI="https://storage.googleapis.com/pos-mainnet-snapshots/heimdall-private"

if (( $# < 3 || $# > 4 )); then
  echo "Usage: bash polygon-private-install.sh PRODUCT vX.Y.Z MODE [--keep-downloads]" >&2
  echo "PRODUCT must be bor or heimdall" >&2
  echo "MODE must be package, docker, or binary" >&2
  exit 1
fi

PRODUCT="$1"
VERSION="$2"
MODE="$3"
KEEP_DOWNLOADS=0

if (( $# == 4 )); then
  if [[ "$4" != "--keep-downloads" ]]; then
    echo "Unsupported option: $4" >&2
    echo "The only supported option is --keep-downloads" >&2
    exit 1
  fi

  KEEP_DOWNLOADS=1
fi

if [[ ! "$VERSION" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[A-Za-z0-9.-]+)?$ ]]; then
  echo "Invalid version: $VERSION" >&2
  exit 1
fi

case "$PRODUCT" in
  bor)
    BASE_URI="$BOR_BASE_URI"
    ARTIFACT_PREFIX="bor"
    PACKAGE_NAME="bor"
    SERVICE_NAME="bor"
    BINARY_PATH="/usr/bin/bor"
    ;;
  heimdall)
    BASE_URI="$HEIMDALL_BASE_URI"
    ARTIFACT_PREFIX="heimdall"
    PACKAGE_NAME="heimdall"
    SERVICE_NAME="heimdalld"
    BINARY_PATH="/usr/bin/heimdalld"
    ;;
  *)
    echo "Unsupported product: $PRODUCT" >&2
    exit 1
    ;;
esac

if [[ ! "$SIGNING_SUBKEY_FINGERPRINT" =~ ^[0-9A-F]{40}$ ]]; then
  echo "The shared signing-subkey fingerprint is invalid" >&2
  exit 1
fi

if [[ ! "$EXPECTED_GPG_FINGERPRINT" =~ ^[0-9A-F]{40}$ ]]; then
  echo "The expected Polygon GPG fingerprint is invalid" >&2
  exit 1
fi

case "$(uname -m)" in
  x86_64)        ARCH="amd64" ;;
  aarch64|arm64) ARCH="arm64" ;;
  *)
    echo "Unsupported architecture: $(uname -m)" >&2
    exit 1
    ;;
esac

echo "Detected architecture: $ARCH"

case "$MODE" in
  binary)
    ARTIFACT="${ARTIFACT_PREFIX}-${VERSION}-${ARCH}"
    MANIFEST="${ARTIFACT_PREFIX}-${VERSION}-BINARIES-SHA256SUMS"
    ;;
  package)
    ARTIFACT="${ARTIFACT_PREFIX}-${VERSION}-${ARCH}.deb"
    MANIFEST="${ARTIFACT_PREFIX}-${VERSION}-PACKAGES-SHA256SUMS"
    ;;
  docker)
    ARTIFACT="${ARTIFACT_PREFIX}-${VERSION}-${ARCH}.tar.gz"
    MANIFEST="${ARTIFACT_PREFIX}-${VERSION}-DOCKER-SHA256SUMS"
    ;;
  *)
    echo "Unsupported mode: $MODE" >&2
    exit 1
    ;;
esac

if (( KEEP_DOWNLOADS )); then
  KEEP_DIR="${PWD}/polygon-private-release-downloads/${PRODUCT}/${VERSION}/${MODE}/${ARCH}"
fi

SCRIPT_DIR="$(
  CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd
)"
readonly PUBLIC_KEY="${SCRIPT_DIR}/polygon-private-releases-public.asc"

if [[ ! -f "$PUBLIC_KEY" ]]; then
  echo "Polygon public key is missing beside the script: $PUBLIC_KEY" >&2
  exit 1
fi

required_commands=(curl gpg sha256sum awk)

case "$MODE" in
  binary)
    required_commands+=(sudo install systemctl mktemp mv rm)
    ;;
  package)
    required_commands+=(sudo dpkg dpkg-deb dpkg-query systemctl)
    ;;
  docker)
    required_commands+=(docker)
    ;;
esac

if (( KEEP_DOWNLOADS )); then
  required_commands+=(cp chmod mkdir)
fi

for command_name in "${required_commands[@]}"; do
  command -v "$command_name" >/dev/null 2>&1 || {
    echo "Missing required command: $command_name" >&2
    exit 1
  }
done

if [[ "$MODE" == package || "$MODE" == binary ]]; then
  # Without a loaded unit the upgrade can't stop the running node, so refuse
  # rather than install under it. A failed lookup aborts through set -e.
  load_state="$(systemctl show --property=LoadState --value "$SERVICE_NAME")"
  if [[ "$load_state" != loaded ]]; then
    echo "${MODE} mode requires a configured ${SERVICE_NAME} service (LoadState: ${load_state:-unknown})" >&2
    exit 1
  fi
  if [[ "$MODE" == binary && ! -x "$BINARY_PATH" ]]; then
    echo "Binary mode requires an existing ${BINARY_PATH}" >&2
    exit 1
  fi
fi

WORK_DIR="$(mktemp -d)"
export GNUPGHOME="${WORK_DIR}/gnupg"
mkdir -m 700 "$GNUPGHOME"
SERVICE_STOPPED=0
STAGED_BINARY=""

cleanup() {
  if [[ -n "$STAGED_BINARY" ]]; then
    sudo rm -f -- "$STAGED_BINARY" || echo "Could not remove staged binary: $STAGED_BINARY" >&2
  fi
  gpgconf --kill all >/dev/null 2>&1 || true
  rm -rf -- "$WORK_DIR"

  if (( SERVICE_STOPPED )); then
    echo "The ${SERVICE_NAME} upgrade did not complete successfully; check the service state before recovery." >&2
    echo "Check the installation error before starting the service manually." >&2
  fi
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

actual_fingerprint="$(
  gpg --batch --with-colons \
    --show-keys "$PUBLIC_KEY" |
  awk -F: '$1 == "fpr" && !found { print $10; found = 1 }'
)"

if [[ "$actual_fingerprint" != "$EXPECTED_GPG_FINGERPRINT" ]]; then
  echo "Polygon public-key fingerprint is incorrect" >&2
  exit 1
fi

gpg --batch --import "$PUBLIC_KEY"

SIGNATURE="${MANIFEST}.asc"

for file in "$ARTIFACT" "$MANIFEST" "$SIGNATURE"; do
  echo "Downloading ${BASE_URI}/${file}"
  if ! curl --proto '=https' --proto-redir '=https' --tlsv1.2 \
      --fail --location --show-error \
      --retry 5 --retry-connrefused --retry-max-time 300 \
      --connect-timeout 30 --max-time 1800 \
      --output "${WORK_DIR}/${file}" "${BASE_URI}/${file}"; then
    echo "Could not download ${file}. Check the release version and your network connection." >&2
    echo "If access is denied, contact Polygon through your established support channel." >&2
    exit 1
  fi
done

VERIFY_STATUS_FILE="${WORK_DIR}/gpg-verify-status"
GPG_DIAGNOSTICS_FILE="${WORK_DIR}/gpg-verify-diagnostics"

print_gpg_diagnostics() {
  if [[ -s "$GPG_DIAGNOSTICS_FILE" ]]; then
    while IFS= read -r diagnostic_line; do
      printf '%s\n' "$diagnostic_line" >&2
    done <"$GPG_DIAGNOSTICS_FILE"
  fi
}

if ! gpg --batch --status-fd 1 --verify \
    "${WORK_DIR}/${SIGNATURE}" \
    "${WORK_DIR}/${MANIFEST}" \
    >"$VERIFY_STATUS_FILE" \
    2>"$GPG_DIAGNOSTICS_FILE"
then
  print_gpg_diagnostics
  echo "GPG could not verify the manifest signature" >&2
  exit 1
fi

if ! awk '
  $1 == "[GNUPG:]" && $2 == "GOODSIG" { good++ }
  $1 == "[GNUPG:]" &&
    ($2 == "REVKEYSIG" || $2 == "EXPKEYSIG" || $2 == "EXPSIG" ||
     $2 == "BADSIG" || $2 == "ERRSIG") { rejected = 1 }
  END { exit !(good == 1 && !rejected) }
' "$VERIFY_STATUS_FILE"
then
  print_gpg_diagnostics
  echo "Manifest signature is not from one currently valid signing key" >&2
  exit 1
fi

mapfile -t valid_signing_fingerprints < <(
  awk \
    '$1 == "[GNUPG:]" && $2 == "VALIDSIG" { print $3 }' \
    "$VERIFY_STATUS_FILE"
)

if (( ${#valid_signing_fingerprints[@]} != 1 )) ||
   [[ "${valid_signing_fingerprints[0]}" != "$SIGNING_SUBKEY_FINGERPRINT" ]]; then
  print_gpg_diagnostics
  echo "Manifest was not signed by the approved shared signing subkey" >&2
  exit 1
fi

echo "Polygon GPG signature verified"

mapfile -t checksum_lines < <(
  awk -v file="$ARTIFACT" \
    '$2 == file { print }' \
    "${WORK_DIR}/${MANIFEST}"
)

if (( ${#checksum_lines[@]} != 1 )); then
  echo "Artifact must appear exactly once in the signed manifest" >&2
  exit 1
fi

printf '%s\n' "${checksum_lines[0]}" \
  >"${WORK_DIR}/${ARTIFACT}.sha256"

(
  cd "$WORK_DIR"
  sha256sum --check "${ARTIFACT}.sha256"
)

echo "Polygon signature and artifact checksum verified"

if (( KEEP_DOWNLOADS )); then
  mkdir -p -- "$KEEP_DIR"
  chmod 700 "$KEEP_DIR"

  for file in "$ARTIFACT" "$MANIFEST" "$SIGNATURE" "${ARTIFACT}.sha256"; do
    cp -- "${WORK_DIR}/${file}" "${KEEP_DIR}/${file}"
    chmod 600 "${KEEP_DIR}/${file}"
  done

  echo "Verified release files kept in: $KEEP_DIR"
fi

check_binary_version() {
  local path="$1" expected="${VERSION#v}" actual
  if [[ "$PRODUCT" == bor ]]; then
    expected="${expected%%-*}-private"
    actual="$("$path" version | awk -F': *' '$1 == "Version" { print $2; exit }')"
  else
    actual="$("$path" version)"
  fi
  if [[ "$actual" != "$expected" ]]; then
    echo "Incorrect ${PACKAGE_NAME} binary version at ${path}" >&2
    echo "Expected: $expected; actual: $actual" >&2
    return 1
  fi
  echo "Verified ${PACKAGE_NAME} binary version ${actual}"
}

# Pre-releases sort below the final release in dpkg: v2.10.3-beta-priv
# packages as 2.10.3~beta-priv, below 2.10.3-priv. packager_deb.yml maps
# tags the same way; respins such as -priv.1 keep their hyphen. Packages
# built before the mapping carry the plain tag, so both forms are accepted.
plain_package_version="${VERSION#v}"
expected_package_version="$plain_package_version"
if [[ "$expected_package_version" == *-* && ! "${expected_package_version#*-}" =~ ^priv([.-].*)?$ ]]; then
  expected_package_version="${expected_package_version%%-*}~${expected_package_version#*-}"
fi

check_package_version() {
  local actual="$1" label="$2"
  if [[ "$actual" != "$expected_package_version" && "$actual" != "$plain_package_version" ]]; then
    echo "${label} ${PACKAGE_NAME} package version is incorrect" >&2
    echo "Expected: $expected_package_version" >&2
    echo "Actual:   $actual" >&2
    return 1
  fi
}

if [[ "$MODE" == package ]]; then
  # Check the packaged version and binary before the service is touched. The
  # binary runs from beside the destination, since /tmp may be noexec.
  check_package_version "$(dpkg-deb --field "${WORK_DIR}/${ARTIFACT}" Version)" Packaged
  dpkg-deb --extract "${WORK_DIR}/${ARTIFACT}" "${WORK_DIR}/package"
  STAGED_BINARY="$(sudo mktemp "${BINARY_PATH}.new.XXXXXX")"
  sudo install -o root -g root -m 755 "${WORK_DIR}/package${BINARY_PATH}" "$STAGED_BINARY"
  check_binary_version "$STAGED_BINARY"
  sudo rm -f -- "$STAGED_BINARY"
  STAGED_BINARY=""
fi

if [[ "$MODE" == binary ]]; then
  # Stage beside the destination so mv uses a same-filesystem rename.
  STAGED_BINARY="$(sudo mktemp "${BINARY_PATH}.new.XXXXXX")"
  sudo install -o root -g root -m 755 "${WORK_DIR}/${ARTIFACT}" "$STAGED_BINARY"
  expected_checksum="$(awk '{ print $1 }' "${WORK_DIR}/${ARTIFACT}.sha256")"
  printf '%s  %s\n' "$expected_checksum" "$STAGED_BINARY" | sha256sum --check -
  check_binary_version "$STAGED_BINARY"
fi

case "$MODE" in
  package|binary)
    # A service that isn't running (a standby validator, for one) stays
    # stopped, so the upgrade never starts a second signer. The unit is
    # stopped either way, which also cancels a pending auto-restart.
    # activating covers a pending auto-restart; deactivating means someone
    # is already stopping it. A failed lookup aborts through set -e.
    SERVICE_WAS_ACTIVE=0
    active_state="$(systemctl show --property=ActiveState --value "$SERVICE_NAME")"
    case "$active_state" in
      active|activating|reloading) SERVICE_WAS_ACTIVE=1 ;;
    esac
    if (( SERVICE_WAS_ACTIVE )); then
      echo "Stopping ${SERVICE_NAME} after successful artifact verification"
      SERVICE_STOPPED=1
    else
      echo "${SERVICE_NAME} is not running; it will be left stopped"
    fi
    sudo systemctl stop "$SERVICE_NAME"

    if [[ "$MODE" == binary ]]; then
      sudo mv -fT -- "$STAGED_BINARY" "$BINARY_PATH"
      STAGED_BINARY=""
    else
      sudo dpkg --install "${WORK_DIR}/${ARTIFACT}"
      installed_package_version="$(
        dpkg-query --show \
          --showformat='${Version}\n' \
          "$PACKAGE_NAME"
      )"

      check_package_version "$installed_package_version" Installed

      echo "Installed ${PACKAGE_NAME} package version ${installed_package_version}"
    fi

    if [[ ! -x "$BINARY_PATH" ]]; then
      echo "Installed binary is missing or is not executable: $BINARY_PATH" >&2
      exit 1
    fi

    check_binary_version "$BINARY_PATH"
    if (( ! SERVICE_WAS_ACTIVE )); then
      echo "Installed ${PACKAGE_NAME}; ${SERVICE_NAME} was not running and was left stopped"
      # A unit that had failed (a crash loop past its start limit) isn't a
      # deliberate standby: report it so automation doesn't record success.
      if [[ "$active_state" == failed ]]; then
        echo "${SERVICE_NAME} had failed before the upgrade; check it and start it manually" >&2
        exit 1
      fi
      exit 0
    fi

    sudo systemctl start "$SERVICE_NAME"

    if ! sudo systemctl is-active --quiet "$SERVICE_NAME"; then
      echo "The ${SERVICE_NAME} service did not become active" >&2
      exit 1
    fi

    SERVICE_STOPPED=0
    echo "The ${SERVICE_NAME} service is active"
    ;;
  docker)
    docker load --input "${WORK_DIR}/${ARTIFACT}"
    ;;
esac
