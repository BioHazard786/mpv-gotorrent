#!/usr/bin/env bash
#===============================================================================
#  mpv-gotorrent Installer & Updater (Linux / macOS / BSD)
#
#  Usage:
#    curl -fsSL https://raw.githubusercontent.com/biohazard786/mpv-gotorrent/main/install.sh | bash
#  or
#    wget -qO- https://raw.githubusercontent.com/biohazard786/mpv-gotorrent/main/install.sh | bash
#
#  Options:
#    -f, --force         Force reinstall or update even if up to date
#    -d, --dir <path>    Specify custom mpv script directory
#    -v, --version <tag> Install specific version (default: latest)
#    -l, --local         Install from current local repository
#    -r, --repo <owner>  GitHub repository (default: biohazard786/mpv-gotorrent)
#    -h, --help          Show help message
#===============================================================================

set -e
set -u

# Defaults
DEFAULT_REPO="biohazard786/mpv-gotorrent"
REPO="${REPO:-$DEFAULT_REPO}"
TARGET_VERSION="${VERSION:-latest}"
FORCE="${FORCE:-0}"
LOCAL_INSTALL="${LOCAL:-0}"
CUSTOM_DIR="${MPV_DIR:-}"

# Colors (disabled if output is not a terminal or NO_COLOR is set)
if [ -t 1 ] && [ -z "${NO_COLOR:-}" ]; then
  C_RESET="$(printf '\033[0m')"
  C_BOLD="$(printf '\033[1m')"
  C_CYAN="$(printf '\033[1;36m')"
  C_GREEN="$(printf '\033[1;32m')"
  C_YELLOW="$(printf '\033[1;33m')"
  C_RED="$(printf '\033[1;31m')"
  C_GRAY="$(printf '\033[0;90m')"
else
  C_RESET=""
  C_BOLD=""
  C_CYAN=""
  C_GREEN=""
  C_YELLOW=""
  C_RED=""
  C_GRAY=""
fi

log_info() {
  printf "%s[INFO]%s %s\n" "${C_CYAN}" "${C_RESET}" "$*"
}

log_ok() {
  printf "%s[OK]%s %s\n" "${C_GREEN}" "${C_RESET}" "$*"
}

log_warn() {
  printf "%s[WARN]%s %s\n" "${C_YELLOW}" "${C_RESET}" "$*"
}

log_err() {
  printf "%s[ERROR]%s %s\n" "${C_RED}" "${C_RESET}" "$*" >&2
}

print_banner() {
  cat <<'EOF'
  __  __ _____ ___      ____ _____ 
 |  \/  |  _ \ \ \    / / _ \_   _|
 | |\/| | |_) | \ \  / / | | || |  
 | |  | |  __/   \ \/ /| |_| || |  
 |_|  |_|_|       \__/  \___/ |_|  mpv-gotorrent
EOF
  printf "%s Cross-platform streaming torrent companion for mpv%s\n\n" "${C_GRAY}" "${C_RESET}"
}

print_help() {
  cat <<EOF
mpv-gotorrent installer & updater

Usage:
  curl -fsSL https://raw.githubusercontent.com/biohazard786/mpv-gotorrent/main/install.sh | bash
  or: ./install.sh [options]

Options:
  -f, --force           Force reinstall or overwrite even if already up to date
  -d, --dir <path>      Specify custom mpv script directory
  -v, --version <tag>   Install specific version tag (default: latest release)
  -l, --local           Install from current local repository / source
  -r, --repo <owner>    GitHub repository (default: biohazard786/mpv-gotorrent)
  -h, --help            Show this help message

Environment Variables:
  MPV_DIR               Target installation directory
  VERSION               Version to install (e.g. v0.1.0 or latest)
  FORCE                 Set to 1 to force reinstallation
  LOCAL                 Set to 1 to install from local source
  REPO                  GitHub repository (owner/repo)
EOF
}

# Parse command-line arguments
while [ $# -gt 0 ]; do
  case "$1" in
    -f|--force)
      FORCE=1
      shift
      ;;
    -d|--dir)
      if [ -n "${2:-}" ]; then
        CUSTOM_DIR="$2"
        shift 2
      else
        log_err "--dir requires a path argument"
        exit 1
      fi
      ;;
    -v|--version)
      if [ -n "${2:-}" ]; then
        TARGET_VERSION="$2"
        shift 2
      else
        log_err "--version requires a version argument"
        exit 1
      fi
      ;;
    -l|--local)
      LOCAL_INSTALL=1
      shift
      ;;
    -r|--repo)
      if [ -n "${2:-}" ]; then
        REPO="$2"
        shift 2
      else
        log_err "--repo requires an owner/repo argument"
        exit 1
      fi
      ;;
    -h|--help)
      print_banner
      print_help
      exit 0
      ;;
    *)
      log_warn "Unknown option: $1"
      shift
      ;;
  esac
done

# Detect operating system
detect_os() {
  local os_name
  os_name="$(uname -s | tr '[:upper:]' '[:lower:]')"
  case "$os_name" in
    linux*)
      echo "linux"
      ;;
    darwin*)
      echo "darwin"
      ;;
    msys*|cygwin*|mingw*)
      echo "windows"
      ;;
    freebsd*)
      echo "freebsd"
      ;;
    *)
      log_err "Unsupported operating system: $os_name"
      exit 1
      ;;
  esac
}

# Detect CPU architecture
detect_arch() {
  local arch_name
  arch_name="$(uname -m)"
  case "$arch_name" in
    x86_64|amd64)
      echo "amd64"
      ;;
    aarch64|arm64)
      echo "arm64"
      ;;
    armv7*|armv6*)
      echo "armv7"
      ;;
    i386|i686)
      echo "386"
      ;;
    *)
      log_err "Unsupported CPU architecture: $arch_name"
      exit 1
      ;;
  esac
}

# Resolve mpv scripts destination directory
detect_install_dir() {
  local os="$1"
  local target_dir=""

  if [ -n "$CUSTOM_DIR" ]; then
    echo "$CUSTOM_DIR"
    return
  fi

  if [ -n "${MPV_HOME:-}" ]; then
    echo "${MPV_HOME}/scripts/mpv-gotorrent"
    return
  fi

  case "$os" in
    darwin)
      if [ -d "${HOME}/.config/mpv" ]; then
        target_dir="${HOME}/.config/mpv/scripts/mpv-gotorrent"
      elif [ -d "${HOME}/Library/Application Support/mpv" ]; then
        target_dir="${HOME}/Library/Application Support/mpv/scripts/mpv-gotorrent"
      else
        target_dir="${HOME}/.config/mpv/scripts/mpv-gotorrent"
      fi
      ;;
    windows)
      if [ -n "${APPDATA:-}" ]; then
        target_dir="${APPDATA}/mpv/scripts/mpv-gotorrent"
      else
        target_dir="${HOME}/.config/mpv/scripts/mpv-gotorrent"
      fi
      ;;
    *)
      local config_home="${XDG_CONFIG_HOME:-${HOME}/.config}"
      target_dir="${config_home}/mpv/scripts/mpv-gotorrent"
      ;;
  esac

  echo "$target_dir"
}

# HTTP fetch helper (curl or wget)
http_get() {
  local url="$1"
  if command -v curl >/dev/null 2>&1; then
    curl -fsSL -H "User-Agent: mpv-gotorrent-installer" "$url"
  elif command -v wget >/dev/null 2>&1; then
    wget -qO- --header="User-Agent: mpv-gotorrent-installer" "$url"
  else
    log_err "Neither curl nor wget was found. Please install curl or wget."
    exit 1
  fi
}

# Download file helper
download_file() {
  local url="$1"
  local dest="$2"
  if command -v curl >/dev/null 2>&1; then
    curl -fsSL -H "User-Agent: mpv-gotorrent-installer" "$url" -o "$dest"
  elif command -v wget >/dev/null 2>&1; then
    wget -q --header="User-Agent: mpv-gotorrent-installer" "$url" -O "$dest"
  else
    log_err "Neither curl nor wget was found. Please install curl or wget."
    exit 1
  fi
}

# Read installed version from target directory
get_installed_version() {
  local target_dir="$1"
  local bin_name="$2"

  if [ -f "${target_dir}/.version" ]; then
    tr -d '[:space:]' < "${target_dir}/.version"
    return
  fi

  if [ -x "${target_dir}/${bin_name}" ]; then
    local v_output
    v_output="$("${target_dir}/${bin_name}" -v 2>/dev/null || true)"
    if [ -n "$v_output" ]; then
      echo "$v_output" | awk '{print $NF}'
      return
    fi
  fi

  echo "unknown"
}

# Check if current directory is the mpv-gotorrent repository
is_local_repo() {
  if [ -f "./cmd/mpv-gotorrent/main.go" ] && { [ -f "./scripts/main.lua" ] || [ -f "./scripts/mpv-gotorrent/main.lua" ]; }; then
    return 0
  fi
  return 1
}

# Perform installation from local repository
install_from_local() {
  local target_dir="$1"
  local bin_filename="$2"

  log_info "Installing from local source repository..."

  local lua_src=""
  if [ -f "./scripts/main.lua" ]; then
    lua_src="./scripts/main.lua"
  elif [ -f "./scripts/mpv-gotorrent/main.lua" ]; then
    lua_src="./scripts/mpv-gotorrent/main.lua"
  else
    log_err "Could not locate main.lua in scripts directory"
    exit 1
  fi

  local local_bin="./bin/${bin_filename}"
  if [ ! -f "$local_bin" ]; then
    if command -v go >/dev/null 2>&1; then
      log_info "Compiling companion binary using local Go compiler..."
      mkdir -p bin
      local local_ver
      local_ver="$(git describe --tags --always --dirty 2>/dev/null || echo "v0.1.0-dev")"
      go build -ldflags="-s -w -X main.Version=${local_ver}" -o "$local_bin" ./cmd/mpv-gotorrent
    else
      log_err "Local binary $local_bin not found and 'go' compiler is not installed."
      exit 1
    fi
  fi

  local ver_stamp
  ver_stamp="$(git describe --tags --always --dirty 2>/dev/null || echo "local-dev")"

  if [ "$IS_INSTALLED" = "1" ] && [ "$FORCE" != "1" ] && [ "$CURRENT_VERSION" = "$ver_stamp" ]; then
    log_ok "mpv-gotorrent is already up to date (${C_BOLD}${CURRENT_VERSION}${C_RESET}) in ${target_dir}"
    printf "%sTo force reinstallation, pass --force or set FORCE=1%s\n" "${C_GRAY}" "${C_RESET}"
    exit 0
  fi

  mkdir -p "$target_dir"
  cp -f "$lua_src" "${target_dir}/main.lua"
  cp -f "$local_bin" "${target_dir}/${bin_filename}"
  chmod +x "${target_dir}/${bin_filename}"
  echo "$ver_stamp" > "${target_dir}/.version"

  printf "\n"
  log_ok "Successfully installed mpv-gotorrent (${C_BOLD}${ver_stamp}${C_RESET}) to ${C_BOLD}${target_dir}${C_RESET}"
  print_usage_tips "$target_dir" "$bin_filename"
}

print_usage_tips() {
  local target_dir="$1"
  local bin_name="$2"

  printf "%sInstalled files in: %s%s\n" "${C_GRAY}" "${target_dir}" "${C_RESET}"
  printf "  %smain.lua%s          - mpv plugin hook script\n" "${C_BOLD}" "${C_RESET}"
  printf "  %s%s%s     - Standalone companion stream engine\n" "${C_BOLD}" "$bin_name" "${C_RESET}"
  printf "\n"
  printf "%sHow to stream:%s\n" "${C_BOLD}" "${C_RESET}"
  printf "  mpv \"magnet:?xt=urn:btih:...\"\n"
  printf "  mpv path/to/movie.torrent\n"
  printf "  mpv \"https://example.com/stream.torrent\"\n"
  printf "\n"
  printf "%sIn-player Controls:%s\n" "${C_BOLD}" "${C_RESET}"
  printf "  Press %sP%s in mpv to toggle live swarm download/upload stats.\n" "${C_CYAN}" "${C_RESET}"
}

# Main installer flow
main() {
  print_banner

  local OS
  local ARCH
  OS="$(detect_os)"
  ARCH="$(detect_arch)"

  local BIN_FILENAME="mpv-gotorrent"
  if [ "$OS" = "windows" ]; then
    BIN_FILENAME="mpv-gotorrent.exe"
  fi

  local TARGET_DIR
  TARGET_DIR="$(detect_install_dir "$OS")"

  log_info "Platform:  ${C_BOLD}${OS}/${ARCH}${C_RESET}"
  log_info "Target:    ${C_BOLD}${TARGET_DIR}${C_RESET}"

  # Check if already installed
  local IS_INSTALLED=0
  local CURRENT_VERSION=""
  if [ -f "${TARGET_DIR}/main.lua" ] && [ -f "${TARGET_DIR}/${BIN_FILENAME}" ]; then
    IS_INSTALLED=1
    CURRENT_VERSION="$(get_installed_version "$TARGET_DIR" "$BIN_FILENAME")"
    log_info "Current installation: ${C_BOLD}${CURRENT_VERSION}${C_RESET}"
  fi

  # Temporary working directory
  local TMP_DIR
  TMP_DIR="$(mktemp -d -t mpv-gotorrent-install.XXXXXX)"
  trap 'rm -rf "$TMP_DIR"' EXIT

  # 1. Local installation if requested
  if [ "$LOCAL_INSTALL" = "1" ]; then
    if is_local_repo; then
      install_from_local "$TARGET_DIR" "$BIN_FILENAME"
      exit 0
    else
      log_err "--local specified but current directory is not the mpv-gotorrent repository"
      exit 1
    fi
  fi

  # 2. Query GitHub for release version
  log_info "Fetching release information from GitHub (${REPO})..."
  local RELEASE_API_URL
  if [ "$TARGET_VERSION" = "latest" ]; then
    RELEASE_API_URL="https://api.github.com/repos/${REPO}/releases/latest"
  else
    RELEASE_API_URL="https://api.github.com/repos/${REPO}/releases/tags/${TARGET_VERSION}"
  fi

  local RELEASE_JSON
  RELEASE_JSON="$(http_get "$RELEASE_API_URL" 2>/dev/null || true)"

  if [ -z "$RELEASE_JSON" ] || echo "$RELEASE_JSON" | grep -q '"message": "Not Found"'; then
    if is_local_repo; then
      log_warn "No GitHub release found for ${REPO}. Detected local repository fallback!"
      install_from_local "$TARGET_DIR" "$BIN_FILENAME"
      exit 0
    fi
    log_err "No release found for ${REPO} (version: ${TARGET_VERSION})."
    log_info "Once a release is tagged on GitHub, prebuilt binaries will be downloaded automatically."
    log_info "To install from source locally: clone repo and run 'make install' or './install.sh --local'"
    exit 1
  fi

  # Extract tag_name from release JSON
  local LATEST_TAG
  LATEST_TAG="$(echo "$RELEASE_JSON" | grep -m1 '"tag_name":' | sed -E 's/.*"tag_name":[[:space:]]*"([^"]+)".*/\1/')"

  if [ -z "$LATEST_TAG" ]; then
    log_err "Failed to parse release tag from GitHub response."
    exit 1
  fi

  log_info "Latest version available: ${C_BOLD}${LATEST_TAG}${C_RESET}"

  # Compare installed version vs latest
  if [ "$IS_INSTALLED" = "1" ] && [ "$FORCE" != "1" ]; then
    local norm_curr="v${CURRENT_VERSION#v}"
    local norm_latest="v${LATEST_TAG#v}"
    if [ "$norm_curr" = "$norm_latest" ]; then
      log_ok "mpv-gotorrent is already up to date (${C_BOLD}${CURRENT_VERSION}${C_RESET}) in ${TARGET_DIR}"
      printf "%sTo force reinstallation or update, re-run with --force or set FORCE=1%s\n" "${C_GRAY}" "${C_RESET}"
      exit 0
    else
      log_info "Update available: ${C_YELLOW}${CURRENT_VERSION}${C_RESET} -> ${C_GREEN}${LATEST_TAG}${C_RESET}"
      log_info "Updating mpv-gotorrent..."
    fi
  else
    log_info "Installing mpv-gotorrent ${C_GREEN}${LATEST_TAG}${C_RESET}..."
  fi

  # Find download asset for current OS/Arch
  local ASSET_URL=""
  local ASSET_NAME=""

  local ASSET_URLS
  ASSET_URLS="$(echo "$RELEASE_JSON" | grep '"browser_download_url":' | sed -E 's/.*"browser_download_url":[[:space:]]*"([^"]+)".*/\1/')"

  for url in $ASSET_URLS; do
    local fname="${url##*/}"
    local fname_lower
    fname_lower="$(echo "$fname" | tr '[:upper:]' '[:lower:]')"

    # Check matching OS
    local os_match=0
    case "$OS" in
      darwin)
        if echo "$fname_lower" | grep -q -E "darwin|macos|osx"; then os_match=1; fi
        ;;
      linux)
        if echo "$fname_lower" | grep -q "linux"; then os_match=1; fi
        ;;
      windows)
        if echo "$fname_lower" | grep -q -E "windows|win"; then os_match=1; fi
        ;;
    esac

    # Check matching Arch
    local arch_match=0
    case "$ARCH" in
      amd64)
        if echo "$fname_lower" | grep -q -E "amd64|x86_64"; then arch_match=1; fi
        ;;
      arm64)
        if echo "$fname_lower" | grep -q -E "arm64|aarch64"; then arch_match=1; fi
        ;;
      armv7)
        if echo "$fname_lower" | grep -q -E "armv7|arm"; then arch_match=1; fi
        ;;
      386)
        if echo "$fname_lower" | grep -q -E "386|x86"; then arch_match=1; fi
        ;;
    esac

    if [ "$os_match" = "1" ] && [ "$arch_match" = "1" ]; then
      ASSET_URL="$url"
      ASSET_NAME="$fname"
      break
    fi
  done

  # Fallback to direct naming convention if not found in assets list
  if [ -z "$ASSET_URL" ]; then
    local EXT=""
    if [ "$OS" = "windows" ]; then EXT=".exe"; fi
    local CANDIDATE_NAME="mpv-gotorrent-${OS}-${ARCH}${EXT}"
    ASSET_URL="https://github.com/${REPO}/releases/download/${LATEST_TAG}/${CANDIDATE_NAME}"
    ASSET_NAME="$CANDIDATE_NAME"
    log_info "Trying direct asset name: ${ASSET_NAME}"
  fi

  # Download binary or archive
  log_info "Downloading companion binary: ${ASSET_NAME}..."
  local DOWNLOAD_DEST="${TMP_DIR}/${ASSET_NAME}"
  if ! download_file "$ASSET_URL" "$DOWNLOAD_DEST"; then
    log_err "Failed to download companion binary from ${ASSET_URL}"
    exit 1
  fi

  local EXTRACTED_BIN="${TMP_DIR}/${BIN_FILENAME}"

  # If archive, unpack
  case "$ASSET_NAME" in
    *.tar.gz|*.tgz)
      tar -xzf "$DOWNLOAD_DEST" -C "$TMP_DIR"
      if [ ! -f "$EXTRACTED_BIN" ]; then
        local found_bin
        found_bin="$(find "$TMP_DIR" -type f -name "$BIN_FILENAME" -o -name "mpv-gotorrent*" | head -n1)"
        if [ -n "$found_bin" ]; then
          mv -f "$found_bin" "$EXTRACTED_BIN"
        fi
      fi
      ;;
    *.zip)
      if command -v unzip >/dev/null 2>&1; then
        unzip -q "$DOWNLOAD_DEST" -d "$TMP_DIR"
        local found_bin
        found_bin="$(find "$TMP_DIR" -type f -name "$BIN_FILENAME" -o -name "mpv-gotorrent*" | head -n1)"
        if [ -n "$found_bin" ]; then
          mv -f "$found_bin" "$EXTRACTED_BIN"
        fi
      else
        log_err "unzip is required to extract ${ASSET_NAME}"
        exit 1
      fi
      ;;
    *)
      mv -f "$DOWNLOAD_DEST" "$EXTRACTED_BIN"
      ;;
  esac

  if [ ! -f "$EXTRACTED_BIN" ]; then
    log_err "Extracted companion binary ${BIN_FILENAME} not found"
    exit 1
  fi
  chmod +x "$EXTRACTED_BIN"

  # Download or extract main.lua
  local LUA_FILE="${TMP_DIR}/main.lua"
  if [ ! -f "$LUA_FILE" ]; then
    local found_lua
    found_lua="$(find "$TMP_DIR" -type f -name "main.lua" | head -n1)"
    if [ -n "$found_lua" ] && [ "$found_lua" != "$LUA_FILE" ]; then
      cp -f "$found_lua" "$LUA_FILE"
    else
      log_info "Fetching mpv Lua script (main.lua)..."
      local RAW_LUA_URL="https://raw.githubusercontent.com/${REPO}/${LATEST_TAG}/scripts/main.lua"
      if ! download_file "$RAW_LUA_URL" "$LUA_FILE" 2>/dev/null; then
        RAW_LUA_URL="https://raw.githubusercontent.com/${REPO}/main/scripts/main.lua"
        if ! download_file "$RAW_LUA_URL" "$LUA_FILE" 2>/dev/null; then
          RAW_LUA_URL="https://raw.githubusercontent.com/${REPO}/main/scripts/mpv-gotorrent/main.lua"
          download_file "$RAW_LUA_URL" "$LUA_FILE"
        fi
      fi
    fi
  fi

  if [ ! -s "$LUA_FILE" ]; then
    log_err "Failed to retrieve valid main.lua script"
    exit 1
  fi

  # Deploy to target directory
  mkdir -p "$TARGET_DIR"
  cp -f "$LUA_FILE" "${TARGET_DIR}/main.lua"
  cp -f "$EXTRACTED_BIN" "${TARGET_DIR}/${BIN_FILENAME}"
  chmod +x "${TARGET_DIR}/${BIN_FILENAME}"
  echo "$LATEST_TAG" > "${TARGET_DIR}/.version"

  # Verify installation
  if [ -x "${TARGET_DIR}/${BIN_FILENAME}" ]; then
    local VERIFIED_VER
    VERIFIED_VER="$("${TARGET_DIR}/${BIN_FILENAME}" -v 2>/dev/null || echo "$LATEST_TAG")"
    log_ok "Installation verified: ${C_BOLD}${VERIFIED_VER}${C_RESET}"
  fi

  printf "\n"
  if [ "$IS_INSTALLED" = "1" ]; then
    log_ok "mpv-gotorrent successfully updated to ${C_BOLD}${LATEST_TAG}${C_RESET}!"
  else
    log_ok "mpv-gotorrent ${C_BOLD}${LATEST_TAG}${C_RESET} successfully installed!"
  fi
  print_usage_tips "$TARGET_DIR" "$BIN_FILENAME"
}

main "$@"
