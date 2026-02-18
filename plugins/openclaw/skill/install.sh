#!/bin/bash

set -euo pipefail

# AgentShield Installation Script
# Installs AgentShield with OpenClaw integration

VERSION="${AGENTSHIELD_VERSION:-1.0.0}"
REPO_OWNER="${REPO_OWNER:-markbriers}"
REPO_NAME="${REPO_NAME:-agentshield}"
GITHUB_REPO="${REPO_OWNER}/${REPO_NAME}"

# Configuration
AGENTSHIELD_PORT="${AGENTSHIELD_PORT:-8432}"
AGENTSHIELD_SIDECAR_PORT="${AGENTSHIELD_SIDECAR_PORT:-8433}"
AGENTSHIELD_DATA_DIR="${AGENTSHIELD_DATA_DIR:-$HOME/.agentshield}"
OPENCLAW_EXTENSIONS_DIR="${OPENCLAW_EXTENSIONS_DIR:-$HOME/.openclaw/extensions}"
VENV_PATH="${AGENTSHIELD_VENV_PATH:-$AGENTSHIELD_DATA_DIR/venv}"

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

log() {
    echo -e "${GREEN}[$(date +'%Y-%m-%d %H:%M:%S')] $1${NC}" >&2
}

warn() {
    echo -e "${YELLOW}[WARN] $1${NC}" >&2
}

error() {
    echo -e "${RED}[ERROR] $1${NC}" >&2
}

info() {
    echo -e "${BLUE}[INFO] $1${NC}" >&2
}

detect_os_arch() {
    local os
    local arch
    
    case "$(uname -s)" in
        Linux*) os="linux" ;;
        Darwin*) os="darwin" ;;
        *) error "Unsupported OS: $(uname -s)"; exit 1 ;;
    esac
    
    case "$(uname -m)" in
        x86_64|amd64) arch="amd64" ;;
        aarch64|arm64) arch="arm64" ;;
        *) error "Unsupported architecture: $(uname -m)"; exit 1 ;;
    esac
    
    echo "${os}-${arch}"
}

check_dependencies() {
    local missing_deps=()
    
    if ! command -v python3 >/dev/null 2>&1; then
        missing_deps+=("python3")
    fi
    
    if ! command -v curl >/dev/null 2>&1; then
        missing_deps+=("curl")
    fi
    
    if ! command -v systemctl >/dev/null 2>&1 && [[ "$(uname -s)" == "Linux" ]]; then
        warn "systemctl not found - systemd services will not be available"
    fi
    
    if [[ ${#missing_deps[@]} -gt 0 ]]; then
        error "Missing required dependencies: ${missing_deps[*]}"
        error "Please install them and run this script again."
        exit 1
    fi
}

download_sidecar() {
    local os_arch="$1"
    local binary_path="$2"
    
    log "Detecting latest release..."
    local latest_release
    latest_release=$(curl -s "https://api.github.com/repos/${GITHUB_REPO}/releases/latest" | \
                    grep '"tag_name":' | sed -E 's/.*"([^"]+)".*/\1/')
    
    if [[ -z "$latest_release" ]]; then
        warn "Could not detect latest release, using version: v${VERSION}"
        latest_release="v${VERSION}"
    fi
    
    local download_url="https://github.com/${GITHUB_REPO}/releases/download/${latest_release}/agentshield-sidecar-${latest_release#v}-${os_arch}.tar.gz"
    local temp_dir
    temp_dir=$(mktemp -d)
    
    log "Downloading sidecar binary from: $download_url"
    
    if curl -L -f -o "${temp_dir}/sidecar.tar.gz" "$download_url"; then
        log "Extracting sidecar binary..."
        tar -xzf "${temp_dir}/sidecar.tar.gz" -C "$temp_dir"
        
        if [[ -f "${temp_dir}/agentshield-sidecar" ]]; then
            mv "${temp_dir}/agentshield-sidecar" "$binary_path"
            chmod +x "$binary_path"
            log "Sidecar binary installed to: $binary_path"
        else
            error "Sidecar binary not found in downloaded archive"
            return 1
        fi
    else
        warn "Failed to download pre-built sidecar binary"
        return 1
    fi
    
    rm -rf "$temp_dir"
}

build_sidecar_from_source() {
    local binary_path="$1"
    local script_dir="$2"
    
    if ! command -v go >/dev/null 2>&1; then
        error "Go not found and pre-built binary unavailable"
        error "Please install Go (https://golang.org/doc/install) or download manually"
        return 1
    fi
    
    log "Building sidecar from source..."
    local temp_dir
    temp_dir=$(mktemp -d)
    
    # Clone repo or copy local source
    if [[ -d "${script_dir}/../sigmalite-sidecar" ]]; then
        cp -r "${script_dir}/../sigmalite-sidecar" "${temp_dir}/"
    else
        log "Cloning repository..."
        git clone "https://github.com/${GITHUB_REPO}.git" "${temp_dir}/agentshield"
        cp -r "${temp_dir}/agentshield/sigmalite-sidecar" "${temp_dir}/"
    fi
    
    cd "${temp_dir}/sigmalite-sidecar"
    go mod download
    go build -ldflags "-s -w" -o "$binary_path" .
    chmod +x "$binary_path"
    
    log "Sidecar binary built and installed to: $binary_path"
    rm -rf "$temp_dir"
}

install_python_package() {
    local script_dir="$1"
    
    log "Creating Python virtual environment..."
    python3 -m venv "$VENV_PATH"
    
    log "Installing AgentShield Python package..."
    # Use local source if available, otherwise install from git
    if [[ -f "${script_dir}/../pyproject.toml" ]]; then
        "$VENV_PATH/bin/pip" install --upgrade pip
        "$VENV_PATH/bin/pip" install -e "${script_dir}/.."
    else
        "$VENV_PATH/bin/pip" install --upgrade pip
        "$VENV_PATH/bin/pip" install "git+https://github.com/${GITHUB_REPO}.git"
    fi
    
    log "AgentShield Python package installed"
}

copy_openclaw_plugin() {
    local script_dir="$1"
    local plugin_dest="${OPENCLAW_EXTENSIONS_DIR}/agentshield"
    
    log "Installing OpenClaw plugin..."
    mkdir -p "$plugin_dest"
    
    if [[ -d "${script_dir}/../openclaw-plugin" ]]; then
        cp -r "${script_dir}/../openclaw-plugin/"* "$plugin_dest/"
    else
        error "OpenClaw plugin source not found"
        return 1
    fi
    
    log "OpenClaw plugin installed to: $plugin_dest"
}

setup_systemd_services() {
    if ! command -v systemctl >/dev/null 2>&1; then
        warn "systemctl not found - skipping systemd service setup"
        return 0
    fi
    
    local systemd_dir="$HOME/.config/systemd/user"
    mkdir -p "$systemd_dir"
    
    # AgentShield monitor service
    cat > "${systemd_dir}/agentshield-monitor.service" << EOF
[Unit]
Description=AgentShield Security Monitor
After=network.target

[Service]
Type=simple
ExecStart=${VENV_PATH}/bin/agentshield start -f
Restart=always
RestartSec=5
Environment=AGENTSHIELD_DATA_DIR=${AGENTSHIELD_DATA_DIR}
Environment=AGENTSHIELD_LOG_LEVEL=INFO

[Install]
WantedBy=default.target
EOF

    # AgentShield real-time server service
    cat > "${systemd_dir}/agentshield-realtime.service" << EOF
[Unit]
Description=AgentShield Real-time Evaluation Server
After=network.target

[Service]
Type=simple
ExecStart=${VENV_PATH}/bin/agentshield realtime-server --host 127.0.0.1 --port ${AGENTSHIELD_PORT}
Restart=always
RestartSec=5
Environment=AGENTSHIELD_DATA_DIR=${AGENTSHIELD_DATA_DIR}
Environment=AGENTSHIELD_LOG_LEVEL=INFO

[Install]
WantedBy=default.target
EOF

    # AgentShield sidecar service (optional)
    if [[ -f "${AGENTSHIELD_DATA_DIR}/bin/agentshield-sidecar" ]]; then
        cat > "${systemd_dir}/agentshield-sidecar.service" << EOF
[Unit]
Description=AgentShield Sigmalite Sidecar
After=network.target

[Service]
Type=simple
ExecStart=${AGENTSHIELD_DATA_DIR}/bin/agentshield-sidecar --port ${AGENTSHIELD_SIDECAR_PORT}
Restart=always
RestartSec=5
WorkingDirectory=${AGENTSHIELD_DATA_DIR}

[Install]
WantedBy=default.target
EOF
    fi
    
    log "Systemd services created"
    
    # Reload and enable services
    systemctl --user daemon-reload
    systemctl --user enable agentshield-monitor.service agentshield-realtime.service
    
    if [[ -f "${systemd_dir}/agentshield-sidecar.service" ]]; then
        systemctl --user enable agentshield-sidecar.service
        log "Enabled systemd services: monitor, realtime, sidecar"
    else
        log "Enabled systemd services: monitor, realtime"
    fi
}

patch_openclaw_config() {
    local openclaw_config_dir="$HOME/.openclaw"
    local config_file="${openclaw_config_dir}/config.json"
    
    if [[ ! -f "$config_file" ]]; then
        warn "OpenClaw config not found at $config_file"
        warn "You may need to manually enable the AgentShield plugin"
        return 0
    fi
    
    log "Patching OpenClaw configuration..."
    
    # Try using openclaw config patch if available
    if command -v openclaw >/dev/null 2>&1; then
        if openclaw config patch --help >/dev/null 2>&1; then
            log "Using 'openclaw config patch' to enable plugin..."
            openclaw config patch --enable-plugin agentshield || {
                warn "Failed to use openclaw config patch, falling back to manual config"
                patch_config_manually "$config_file"
            }
        else
            patch_config_manually "$config_file"
        fi
    else
        patch_config_manually "$config_file"
    fi
}

patch_config_manually() {
    local config_file="$1"
    
    log "Manually patching OpenClaw config..."
    
    # Create backup
    cp "$config_file" "${config_file}.backup.$(date +%s)"
    
    # Use Python to safely modify JSON
    python3 << EOF
import json
import sys

try:
    with open('$config_file', 'r') as f:
        config = json.load(f)
    
    # Ensure plugins section exists
    if 'plugins' not in config:
        config['plugins'] = {}
    
    # Add AgentShield plugin config
    config['plugins']['agentshield'] = {
        'enabled': True,
        'endpoint': 'http://127.0.0.1:${AGENTSHIELD_PORT}/api/v1/evaluate',
        'timeout_ms': 50,
        'timeout_policy': 'allow',
        'intercept': ['exec', 'write', 'edit', 'browser', 'message', 'sessions_spawn'],
        'skip': ['read', 'session_status']
    }
    
    with open('$config_file', 'w') as f:
        json.dump(config, f, indent=2)
    
    print("OpenClaw config updated successfully")
    
except Exception as e:
    print(f"Error updating config: {e}", file=sys.stderr)
    sys.exit(1)
EOF
    
    log "OpenClaw configuration patched"
}

setup_data_directories() {
    log "Setting up AgentShield directories..."
    mkdir -p "$AGENTSHIELD_DATA_DIR"/{bin,logs,rules}
    
    # Copy default rules if available
    local script_dir="$1"
    if [[ -d "${script_dir}/../rules" ]]; then
        cp -r "${script_dir}/../rules/"* "${AGENTSHIELD_DATA_DIR}/rules/"
        log "Copied default detection rules"
    fi
}

start_services() {
    if command -v systemctl >/dev/null 2>&1; then
        log "Starting AgentShield services..."
        systemctl --user start agentshield-realtime.service || warn "Failed to start realtime service"
        systemctl --user start agentshield-monitor.service || warn "Failed to start monitor service"
        
        if systemctl --user is-enabled agentshield-sidecar.service >/dev/null 2>&1; then
            systemctl --user start agentshield-sidecar.service || warn "Failed to start sidecar service"
        fi
    fi
}

uninstall() {
    log "Uninstalling AgentShield..."
    
    # Stop and disable services
    if command -v systemctl >/dev/null 2>&1; then
        for service in agentshield-monitor agentshield-realtime agentshield-sidecar; do
            systemctl --user stop "${service}.service" 2>/dev/null || true
            systemctl --user disable "${service}.service" 2>/dev/null || true
            rm -f "$HOME/.config/systemd/user/${service}.service"
        done
        systemctl --user daemon-reload
    fi
    
    # Remove virtual environment
    if [[ -d "$VENV_PATH" ]]; then
        rm -rf "$VENV_PATH"
        log "Removed Python virtual environment"
    fi
    
    # Remove OpenClaw plugin
    if [[ -d "${OPENCLAW_EXTENSIONS_DIR}/agentshield" ]]; then
        rm -rf "${OPENCLAW_EXTENSIONS_DIR}/agentshield"
        log "Removed OpenClaw plugin"
    fi
    
    # Remove sidecar binary
    if [[ -f "${AGENTSHIELD_DATA_DIR}/bin/agentshield-sidecar" ]]; then
        rm -f "${AGENTSHIELD_DATA_DIR}/bin/agentshield-sidecar"
    fi
    
    # Optionally remove data directory
    read -p "Remove AgentShield data directory (${AGENTSHIELD_DATA_DIR})? [y/N]: " -n 1 -r
    echo
    if [[ $REPLY =~ ^[Yy]$ ]]; then
        rm -rf "$AGENTSHIELD_DATA_DIR"
        log "Removed AgentShield data directory"
    fi
    
    log "AgentShield uninstalled successfully"
}

main() {
    # Parse command line arguments
    if [[ $# -gt 0 && "$1" == "--uninstall" ]]; then
        uninstall
        exit 0
    fi
    
    log "Starting AgentShield installation..."
    
    # Get script directory
    local script_dir
    script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
    
    # Check dependencies
    check_dependencies
    
    # Setup directories
    setup_data_directories "$script_dir"
    
    # Detect OS and architecture
    local os_arch
    os_arch=$(detect_os_arch)
    log "Detected platform: $os_arch"
    
    # Install sidecar binary
    local sidecar_path="${AGENTSHIELD_DATA_DIR}/bin/agentshield-sidecar"
    mkdir -p "$(dirname "$sidecar_path")"
    
    if ! download_sidecar "$os_arch" "$sidecar_path"; then
        warn "Pre-built binary download failed, attempting to build from source..."
        if ! build_sidecar_from_source "$sidecar_path" "$script_dir"; then
            warn "Sidecar installation failed - AgentShield will work without it"
        fi
    fi
    
    # Install Python package
    install_python_package "$script_dir"
    
    # Copy OpenClaw plugin
    copy_openclaw_plugin "$script_dir"
    
    # Setup systemd services
    setup_systemd_services
    
    # Patch OpenClaw config
    patch_openclaw_config
    
    # Start services
    start_services
    
    log "AgentShield installation completed successfully!"
    echo
    info "Next steps:"
    info "1. Set ANTHROPIC_API_KEY for LLM triage: export ANTHROPIC_API_KEY=sk-ant-..."
    info "2. Check status: ${VENV_PATH}/bin/agentshield status"
    info "3. View alerts: ${VENV_PATH}/bin/agentshield alerts"
    info "4. Generate summary: ${VENV_PATH}/bin/agentshield summary"
    echo
    info "OpenClaw configuration has been updated to enable the AgentShield plugin."
    info "Restart OpenClaw to activate real-time security monitoring."
}

# Handle Ctrl+C gracefully
trap 'echo -e "\n${RED}Installation cancelled.${NC}"; exit 1' INT

main "$@"