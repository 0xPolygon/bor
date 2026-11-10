# Verify and install a Polygon private release

Use this guide to check that a Bor or Heimdall download is genuine, then install it. **Bor `v2.10.1-priv` and Heimdall `v0.12.1-priv` are examples only.** Replace them with the version Polygon announces for your network.

For most operators:

1. Complete [first-time setup](#first-time-setup) to download the tools and check the public key.
2. Run **one** [installer command](#recommended-use-the-installer) for your product and installation type.
3. Wait for the success message. If anything fails, stop and read [troubleshooting](#if-something-goes-wrong).

Use [manual verification](#manual-verification-and-installation) only if you prefer to run the commands yourself. You do not need to follow both methods.

## Before you start

- These commands are for Linux nodes. You need Bash 4 or later, `curl`, GnuPG (`gpg` and `gpgconf`), `sha256sum`, `awk`, and standard Linux utilities.
- Packages require a Debian-based system, `sudo`, `dpkg`, `dpkg-query`, and `systemctl`.
- Raw binaries require `sudo`, `install`, `mktemp`, `mv`, and `systemctl`, plus an existing installation and service. Allow space for both the old and new binary.
- Docker requires a working Docker installation and permission to load images.
- Package/binary upgrades briefly stop `bor` or `heimdalld` **after verification**, if it is running. Plan for this downtime. A service that isn't running, such as a standby validator, stays stopped. Docker mode only loads an image; it does not replace running containers.
- All downloads use HTTPS through `curl`. If access is denied, contact Polygon through your established node-operator support channel.

## First-time setup

Only get the installer and public key from Polygon's official location—not a script sent through Telegram, email, or a direct message. The public key checks who signed the release; its fingerprint is its unique identifier. Confirm the entire primary fingerprint through a separate, trusted Polygon source:

```text
Primary key:    79E0B57D414727B48F3F9BA3759DA68F40672C2C
Signing subkey: 36839E0B2513D894CA7C90F4474EB7D4682EE64D
```

Open a Bash terminal in the directory where you want to save the tools. If unsure which shell you are using, run `bash` first. Paste the complete block below, including the opening and closing parentheses. It downloads the script, public key, and this guide into your current directory, replacing existing copies with those names. It stops if a download fails or the key does not match.

```bash
(
  set -euo pipefail
  for file in polygon-private-install.sh polygon-private-releases-public.asc README.md; do
    curl --proto '=https' --proto-redir '=https' --tlsv1.2 -fL \
      --retry 5 --retry-connrefused --retry-max-time 300 \
      --connect-timeout 30 --max-time 1800 \
      "https://storage.googleapis.com/pos-mainnet-snapshots/private-release-tools/${file}" \
      -o "./${file}"
  done
  KEY_DIR="$(mktemp -d)"
  trap 'rm -rf -- "$KEY_DIR"' EXIT
  PRIMARY="$(gpg --homedir "$KEY_DIR" --batch --with-colons \
    --show-keys ./polygon-private-releases-public.asc |
    awk -F: '$1 == "fpr" && !seen { print $10; seen=1 }')"
  test "$PRIMARY" = "79E0B57D414727B48F3F9BA3759DA68F40672C2C"
  echo "Polygon public key is correct"
)
```

Continue only when you see `Polygon public key is correct`. Keep `polygon-private-install.sh` and `polygon-private-releases-public.asc` in the same directory. Refresh them when Polygon announces a script or key update.

The script itself does not have a separate signature. Checking the key does not prove that the script is genuine, which is why you must download it from the official location above.

Keep the download options as written: they require HTTPS with TLS 1.2 or later, follow redirects, and stop on HTTP errors. Temporary errors such as a refused connection may be retried up to five times, with increasing delays. New retries stop after five minutes, but an attempt already running can finish. Each connection has a 30-second timeout; each download attempt has a 30-minute limit.

## Recommended: use the installer

Run **one** command below from the directory where you saved the tools. Replace the example version first. The installer detects AMD64 or ARM64 automatically; you do not need to choose it. No `chmod +x` step is needed because the commands use `bash`.

| Choose | Use it when |
| --- | --- |
| `package` | You install Bor or Heimdall as a Debian `.deb` package. |
| `binary` | You want to replace only the executable on an existing node. |
| `docker` | You run the node in Docker and want to load the release image. |

These commands **install or load** the release after verification; they are not verification-only commands.

### Packages

Bor:
```bash
bash ./polygon-private-install.sh bor v2.10.1-priv package
```

Heimdall:
```bash
bash ./polygon-private-install.sh heimdall v0.12.1-priv package
```

### Raw binaries

Bor:
```bash
bash ./polygon-private-install.sh bor v2.10.1-priv binary
```

Heimdall:
```bash
bash ./polygon-private-install.sh heimdall v0.12.1-priv binary
```

Binary mode replaces `/usr/bin/bor` or `/usr/bin/heimdalld`; your service must use that path. It does not create configuration or service files. Debian's package manager may still show the old package version, and a later package install may replace the binary again.

### Docker images

Bor:
```bash
bash ./polygon-private-install.sh bor v2.10.1-priv docker
```

Heimdall:
```bash
bash ./polygon-private-install.sh heimdall v0.12.1-priv docker
```

### Keep verified downloads

By default, downloaded files are removed when the installer exits. Add `--keep-downloads` to any mode if you want to inspect the verified files later:

Bor:
```bash
bash ./polygon-private-install.sh bor v2.10.1-priv package --keep-downloads
```

Heimdall:
```bash
bash ./polygon-private-install.sh heimdall v0.12.1-priv package --keep-downloads
```

The installer prints where it saved the release file, checksum list (manifest), signature, and selected checksum. The location is `./polygon-private-release-downloads/PRODUCT/VERSION/MODE/ARCH/`, relative to the directory where you ran it.

Files are kept only after verification succeeds, including if installation later fails. The temporary GPG keyring is still removed. Saved directories/files use permissions `700`/`600` (accessible only to their owner). Delete saved downloads when no longer needed to free disk space.

### What happens during an upgrade?

The installer checks that the public key and signer match Polygon's approved fingerprints, then checks that the downloaded file matches the signed checksum list. A checksum is a fingerprint of a file's contents. Successful verification prints `Polygon signature and artifact checksum verified`; installation follows, so wait for the final result too.

- **Package:** check the package version and the packaged executable's version → stop the service → install → check both again → restart it if it was running → confirm it is active.
- **Binary:** prepare a temporary copy beside the installed executable and check its checksum and version → stop the service → replace the executable with a single rename → check its version again → restart it if it was running and confirm it is active. If preparing the copy fails, the old executable and running service are untouched.
- **Docker:** load the verified image only. Update your running container separately using your normal deployment process.

A completed package/binary upgrade prints `The bor service is active` or `The heimdalld service is active`. Docker prints the result of loading the image.

The installer does **not automatically restore the previous version** if replacement or a later check fails. Investigate before restarting manually. Preparing the new binary before replacing the old one reduces the risk of an incomplete copy; it cannot guarantee recovery from filesystem damage or power loss.

## Direct downloads: packages, binaries, and Docker images

Skip this section if you are using the installer. To download files yourself, run `uname -m`: `x86_64` means AMD64; `aarch64` or `arm64` means ARM64.

Choose **one** command below and edit the values for your node:

- `PRODUCT`: `bor` or `heimdall`.
- `VERSION`: the announced release, including its leading `v`.
- `MODE`: `package` for a `.deb`, `binary` for the executable, or `docker` for a `.tar.gz` image archive.
- `ARCH`: `amd64` or `arm64`, based on the machine where you will install it.

Bor:
```bash
PRODUCT=bor VERSION=v2.10.1-priv MODE=package ARCH=amd64
```

Heimdall:
```bash
PRODUCT=heimdall VERSION=v0.12.1-priv MODE=package ARCH=amd64
```

For example, change `MODE=package` to `MODE=binary` or `MODE=docker` for those downloads, and use `ARCH=arm64` for an ARM64 node.

Run this block in the same Bash terminal, in the directory containing `polygon-private-releases-public.asc`. It downloads **three files** into your current directory (`pwd`): your selected release file, its checksum manifest, and the manifest's signature. Existing files with the same names are overwritten. Nothing is installed or loaded into Docker.

```bash
(
  set -euo pipefail
  umask 077
  : "${PRODUCT:?Choose bor or heimdall above}"
  : "${VERSION:?Set the release version above}"
  : "${MODE:?Choose package, binary, or docker above}"
  : "${ARCH:?Choose amd64 or arm64 above}"
  case "$PRODUCT" in bor|heimdall) ;; *) echo "Unsupported product"; exit 1 ;; esac
  case "$ARCH" in amd64|arm64) ;; *) echo "Unsupported architecture"; exit 1 ;; esac
  [[ "$VERSION" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[A-Za-z0-9.-]+)?$ ]] || {
    echo "Invalid release version"; exit 1;
  }
  case "$MODE" in
    package) EXT=".deb"; KIND=PACKAGES ;;
    binary) EXT=""; KIND=BINARIES ;;
    docker) EXT=".tar.gz"; KIND=DOCKER ;;
    *) echo "Unsupported mode"; exit 1 ;;
  esac
  BASE_URL="https://storage.googleapis.com/pos-mainnet-snapshots/${PRODUCT}-private"
  PREFIX="${PRODUCT}-${VERSION}"
  MANIFEST="${PREFIX}-${KIND}-SHA256SUMS"
  echo "Downloading into: $PWD"
  for file in "${PREFIX}-${ARCH}${EXT}" "$MANIFEST" "${MANIFEST}.asc"; do
    curl --proto '=https' --proto-redir '=https' --tlsv1.2 -fL \
      --retry 5 --retry-connrefused --retry-max-time 300 \
      --connect-timeout 30 --max-time 1800 \
      "${BASE_URL}/${file}" -o "${PWD}/${file}"
  done
  echo "Downloads complete: $PWD"
  echo "These files have NOT been verified. Do not install or load them yet."
)
```

If downloading fails, incomplete files may remain—do not use them. Rerun the download block to replace them. A successful download is still not verification: check the public key, signature, and selected file's checksum before installing or loading it. Each installation type has its own manifest; do not mix types, versions, or architectures. Remove downloaded release files when no longer needed.

Continue to the [manual procedure below](#manual-verification-and-installation) in the same directory. You can also download the files yourself; keep the original filenames and place the release file, manifest, signature, and trusted public key together in your current directory. The manual checks never download files.

## Manual verification and installation

If you have not downloaded the release files yet, follow [Direct downloads](#direct-downloads-packages-binaries-and-docker-images) first, then return here.

Complete first-time setup, then open Bash in the directory containing the trusted public key. Choose **one** command from the three sections below. These commands only select the product, version, and file type; they do not download or install anything yet.

Replace the example version, then continue to [Run the manual checks](#run-the-manual-checks) in the **same terminal**. There is one shared procedure for both products, so you do not need to change filenames throughout the guide.

### Manual package verification and installation

Bor:
```bash
PRODUCT=bor VERSION=v2.10.1-priv MODE=package
```

Heimdall:
```bash
PRODUCT=heimdall VERSION=v0.12.1-priv MODE=package
```

### Verify and install a raw binary manually

Bor:
```bash
PRODUCT=bor VERSION=v2.10.1-priv MODE=binary
```

Heimdall:
```bash
PRODUCT=heimdall VERSION=v0.12.1-priv MODE=binary
```

### Manual Docker image verification and loading

Bor:
```bash
PRODUCT=bor VERSION=v2.10.1-priv MODE=docker
```

Heimdall:
```bash
PRODUCT=heimdall VERSION=v0.12.1-priv MODE=docker
```

### Run the manual checks

The block below checks files you have already downloaded. **By default, it only verifies them—nothing is downloaded, installed, or loaded into Docker, and no service is stopped.** It detects AMD64/ARM64 automatically and stops on any failure. Installation is an explicit option below. Paste the whole block, including the parentheses; these keep a failure from closing your terminal.

Run the checks from the directory containing your downloaded release file, manifest, signature, and `polygon-private-releases-public.asc`. Use `pwd` to see your current directory and `ls` to list its files. The release files must match the selected product, version, mode, and this machine's architecture. No download-folder variable is needed. The commands copy the release files to a temporary working directory before verifying; your originals are left unchanged. Missing or invalid files stop the process—there is no fallback download.

Allow enough free space in the system's temporary directory (usually `/tmp`) for an extra copy of the release file, manifest, and signature. Docker archives can be large. This temporary copy is removed when the checks finish; your original downloads remain.

To check the files **without installing or stopping a service**, run this first:

```bash
VERIFY_ONLY=1
```

To verify **and install**, run this instead:

```bash
VERIFY_ONLY=0
```

`VERIFY_ONLY` applies only to the manual block, not to `polygon-private-install.sh`. Temporary working files are removed afterward; your original downloads are never removed. Use the installer if you want downloading, verification, and installation handled automatically.

<details>
<summary>Show the manual verification and installation commands</summary>

```bash
(
  set -euo pipefail
  umask 077
  : "${PRODUCT:?Choose your product above}"
  : "${VERSION:?Choose your version above}"
  : "${MODE:?Choose package, binary, or docker above}"
  case "${VERIFY_ONLY:-1}" in 0|1) ;; *) echo "Set VERIFY_ONLY to 1 or 0"; exit 1 ;; esac
  [[ "$VERSION" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[A-Za-z0-9.-]+)?$ ]]
  case "$PRODUCT" in
    bor) SERVICE=bor; BINARY=/usr/bin/bor ;;
    heimdall) SERVICE=heimdalld; BINARY=/usr/bin/heimdalld ;;
    *) echo "Unsupported product"; exit 1 ;;
  esac
  case "$(uname -m)" in
    x86_64) ARCH=amd64 ;;
    aarch64|arm64) ARCH=arm64 ;;
    *) echo "Unsupported architecture"; exit 1 ;;
  esac
  echo "Detected architecture: $ARCH"
  case "$MODE" in
    package) ARTIFACT="${PRODUCT}-${VERSION}-${ARCH}.deb"; KIND=PACKAGES ;;
    binary) ARTIFACT="${PRODUCT}-${VERSION}-${ARCH}"; KIND=BINARIES ;;
    docker) ARTIFACT="${PRODUCT}-${VERSION}-${ARCH}.tar.gz"; KIND=DOCKER ;;
    *) echo "Unsupported mode"; exit 1 ;;
  esac
  MANIFEST="${PRODUCT}-${VERSION}-${KIND}-SHA256SUMS"
  PUBLIC_KEY="${PWD}/polygon-private-releases-public.asc"
  DOWNLOAD_PWD="$PWD"
  WORK_DIR="$(mktemp -d)"
  export GNUPGHOME="${WORK_DIR}/gnupg"
  STAGED_BINARY=""
  cleanup() {
    if [[ -n "$STAGED_BINARY" ]]; then
      sudo rm -f -- "$STAGED_BINARY" || echo "Could not remove $STAGED_BINARY" >&2
    fi
    gpgconf --kill all >/dev/null 2>&1 || true
    rm -rf -- "$WORK_DIR"
  }
  trap cleanup EXIT
  trap 'exit 130' INT
  trap 'exit 143' TERM
  mkdir -m 700 "$GNUPGHOME"
  cd "$WORK_DIR"

  # Verify the public key before trusting any signature.
  PRIMARY="$(gpg --batch --with-colons --show-keys "$PUBLIC_KEY" |
    awk -F: '$1 == "fpr" && !seen { print $10; seen=1 }')"
  test "$PRIMARY" = "79E0B57D414727B48F3F9BA3759DA68F40672C2C"
  gpg --batch --import "$PUBLIC_KEY"
  for file in "$ARTIFACT" "$MANIFEST" "${MANIFEST}.asc"; do
    cp -- "${DOWNLOAD_PWD}/${file}" "$file"
  done

  # Require one valid signature from the approved signing subkey.
  gpg --batch --status-fd 1 --verify "${MANIFEST}.asc" "$MANIFEST" > status.txt
  awk '
    $1 == "[GNUPG:]" && $2 == "GOODSIG" { good++ }
    $1 == "[GNUPG:]" && $2 == "VALIDSIG" {
      valid++; if ($3 != "36839E0B2513D894CA7C90F4474EB7D4682EE64D") bad=1
    }
    $1 == "[GNUPG:]" && $2 ~ /^(REVKEYSIG|EXPKEYSIG|EXPSIG|BADSIG|ERRSIG)$/ { bad=1 }
    END { exit !(good == 1 && valid == 1 && !bad) }
  ' status.txt
  awk -v file="$ARTIFACT" '
    $2 == file { print; count++ }
    END { exit !(count == 1) }
  ' "$MANIFEST" > selected.sha256
  sha256sum --check selected.sha256
  echo "Polygon signature and artifact checksum verified"

  if [[ "${VERIFY_ONLY:-1}" == 1 ]]; then
    exit 0
  fi

  if [[ "$MODE" == docker ]]; then
    docker load --input "$ARTIFACT"
    exit 0
  fi

  check_version() {
    local path="$1" expected="${VERSION#v}" actual
    if [[ "$PRODUCT" == bor ]]; then
      expected="${expected%%-*}-private"
      actual="$("$path" version | awk -F': *' '$1 == "Version" { print $2; exit }')"
    else
      actual="$("$path" version)"
    fi
    printf 'Expected: %s; actual: %s\n' "$expected" "$actual"
    test "$actual" = "$expected"
  }
  test "$(systemctl show --property=LoadState --value "$SERVICE")" = loaded
  # Pre-releases package with "~" so they sort below the final release;
  # packages built before that carry the plain tag.
  PKG_VERSION="${VERSION#v}"
  if [[ "$PKG_VERSION" == *-* && ! "${PKG_VERSION#*-}" =~ ^priv([.-].*)?$ ]]; then
    PKG_VERSION="${PKG_VERSION%%-*}~${PKG_VERSION#*-}"
  fi
  check_package() {
    [[ "$1" == "$PKG_VERSION" || "$1" == "${VERSION#v}" ]]
  }
  if [[ "$MODE" == package ]]; then
    check_package "$(dpkg-deb --field "$ARTIFACT" Version)"
    dpkg-deb --extract "$ARTIFACT" package
    # Run it from beside the destination: /tmp may be noexec.
    STAGED_BINARY="$(sudo mktemp "${BINARY}.new.XXXXXX")"
    sudo install -o root -g root -m 755 "package${BINARY}" "$STAGED_BINARY"
    check_version "$STAGED_BINARY"
    sudo rm -f -- "$STAGED_BINARY"
    STAGED_BINARY=""
  fi
  if [[ "$MODE" == binary ]]; then
    test -x "$BINARY"
    STAGED_BINARY="$(sudo mktemp "${BINARY}.new.XXXXXX")"
    sudo install -o root -g root -m 755 "$ARTIFACT" "$STAGED_BINARY"
    EXPECTED_CHECKSUM="$(awk '{ print $1 }' selected.sha256)"
    printf '%s  %s\n' "$EXPECTED_CHECKSUM" "$STAGED_BINARY" | sha256sum --check -
    check_version "$STAGED_BINARY"
  fi

  # A service that isn't running (a standby validator) stays stopped. Stop
  # it either way, which also cancels a pending auto-restart.
  WAS_ACTIVE=0
  ACTIVE_STATE="$(systemctl show --property=ActiveState --value "$SERVICE")"
  case "$ACTIVE_STATE" in
    active|activating|reloading) WAS_ACTIVE=1 ;;
  esac
  sudo systemctl stop "$SERVICE"
  if [[ "$MODE" == binary ]]; then
    sudo mv -fT -- "$STAGED_BINARY" "$BINARY"
    STAGED_BINARY=""
  else
    sudo dpkg --install "$ARTIFACT"
    INSTALLED="$(dpkg-query --show --showformat='${Version}' "$PRODUCT")"
    check_package "$INSTALLED"
  fi
  check_version "$BINARY"
  if (( WAS_ACTIVE )); then
    sudo systemctl start "$SERVICE"
    sudo systemctl is-active --quiet "$SERVICE"
    echo "$PRODUCT installed and service active"
  else
    echo "$PRODUCT installed; $SERVICE was not running and was left stopped"
    # A failed unit isn't a deliberate standby: check it and start it manually.
    test "$ACTIVE_STATE" != failed
  fi
)
```

</details>

The checksum list covers both architectures. The commands check only your selected file, so the other architecture does not need to be downloaded. The `.asc` file is Polygon's signature on that list.

## If something goes wrong

- **Download/access error:** stop and check the announced release, network connection, and access instructions. Never bypass a failed download or verification.
- **Wrong fingerprint, rejected signature, or failed checksum:** do not install or load the artifact. Contact Polygon.
- **GPG “unknown trust” warning:** expected in a fresh keyring. It is not a substitute for the checks above; the pinned fingerprints and valid signature must still match. The installer hides this warning on success and shows GPG details on failure.
- **Installation/version/start failure:** do not blindly restart or retry. Inspect `systemctl status bor` or `systemctl status heimdalld` and the error first; a failure after stopping can leave the service down. There is no automatic rollback.
- **Docker image loaded:** update your running container separately using your normal deployment process.

Share relevant errors through your established Polygon support channel. Remove credentials, access tokens, and private links before sharing logs.
