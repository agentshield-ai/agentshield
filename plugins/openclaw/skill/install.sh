#!/bin/bash
set -e

# AgentShield Installer - v2.0.0
REPO="agentshield-ai/agentshield"
RULES_REPO="agentshield-ai/sigma-ai"
BINARY_NAME="agentshield-engine"
INSTALL_DIR="${AGENTSHIELD_HOME:-$HOME/.agentshield}"
CONFIG_FILE="$INSTALL_DIR/config.yaml"
SERVICE_NAME="agentshield-engine"

# Cleanup temporary files on exit
CLEANUP_FILES=()
cleanup() { for f in "${CLEANUP_FILES[@]}"; do rm -f "$f"; done; }
trap cleanup EXIT

# Colors
RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'; NC='\033[0m'
log() { echo -e "${GREEN}[AgentShield]${NC} $1"; }
warn() { echo -e "${YELLOW}[Warning]${NC} $1"; }
error() { echo -e "${RED}[Error]${NC} $1"; exit 1; }

# Detect platform
detect_platform() {
    OS=$(uname -s | tr '[:upper:]' '[:lower:]')
    ARCH=$(uname -m)
    case "$OS" in linux*) OS="linux";; darwin*) OS="darwin";; *) error "Unsupported OS: $OS";; esac
    case "$ARCH" in x86_64|amd64) ARCH="amd64";; aarch64|arm64) ARCH="arm64";; *) error "Unsupported arch: $ARCH";; esac
    PLATFORM="${OS}_${ARCH}"
    log "Detected platform: $PLATFORM"
}

# Install via Go fallback
install_via_go() {
    command -v go >/dev/null 2>&1 || error "Go not found and no pre-built binary available"
    log "Installing via Go..."
    go install "github.com/$REPO/cmd/agentshield@latest" || error "Go install failed"
    GOBIN=${GOBIN:-$(go env GOPATH)/bin}
    mkdir -p "$INSTALL_DIR"
    cp "$GOBIN/agentshield" "$INSTALL_DIR/$BINARY_NAME" || error "Failed to copy Go binary"
}

# Download binary
download_binary() {
    if [ -x "$INSTALL_DIR/$BINARY_NAME" ]; then
        log "Binary already present at $INSTALL_DIR/$BINARY_NAME — skipping download"
        return
    fi
    log "Downloading AgentShield binary..."
    LATEST_URL="https://api.github.com/repos/$REPO/releases/latest"
    DOWNLOAD_URL=$(curl -s "$LATEST_URL" | grep "browser_download_url.*${PLATFORM}" | cut -d'"' -f4)
    if [ -z "$DOWNLOAD_URL" ]; then warn "No pre-built binary found for $PLATFORM"; install_via_go; return; fi
    
    TEMP_FILE=$(mktemp "${TMPDIR:-/tmp}/agentshield-XXXXXX.tar.gz")
    CLEANUP_FILES+=("$TEMP_FILE")
    curl -L -o "$TEMP_FILE" "$DOWNLOAD_URL" || { warn "Download failed"; install_via_go; return; }

    # I9: Checksum verification
    RELEASE_TAG=$(curl -s "https://api.github.com/repos/$REPO/releases/latest" | grep '"tag_name"' | cut -d'"' -f4)
    CHECKSUMS_URL="https://github.com/$REPO/releases/download/${RELEASE_TAG}/SHA256SUMS"
    CHECKSUMS_FILE=$(mktemp "${TMPDIR:-/tmp}/agentshield-checksums-XXXXXX")
    CLEANUP_FILES+=("$CHECKSUMS_FILE")
    if curl -sfL -o "$CHECKSUMS_FILE" "$CHECKSUMS_URL"; then
        EXPECTED_HASH=$(grep "${PLATFORM}" "$CHECKSUMS_FILE" | awk '{print $1}')
        if [ -n "$EXPECTED_HASH" ]; then
            if command -v sha256sum >/dev/null 2>&1; then
                ACTUAL_HASH=$(sha256sum "$TEMP_FILE" | awk '{print $1}')
            elif command -v shasum >/dev/null 2>&1; then
                ACTUAL_HASH=$(shasum -a 256 "$TEMP_FILE" | awk '{print $1}')
            else
                warn "No sha256sum or shasum available — cannot verify binary integrity"
            fi
            if [ -n "$ACTUAL_HASH" ]; then
                if [ "$ACTUAL_HASH" != "$EXPECTED_HASH" ]; then
                    error "Checksum mismatch! Expected $EXPECTED_HASH, got $ACTUAL_HASH"
                fi
                log "Checksum verified"
            fi
        else
            warn "No checksum entry for $PLATFORM in SHA256SUMS — skipping verification"
        fi
    else
        warn "SHA256SUMS not available for this release — skipping checksum verification"
    fi

    mkdir -p "$INSTALL_DIR/bin"
    tar -xzf "$TEMP_FILE" -C "$INSTALL_DIR/bin" || { warn "Extract failed"; install_via_go; return; }

    if [ -f "$INSTALL_DIR/bin/agentshield" ]; then
        chmod +x "$INSTALL_DIR/bin/agentshield"
        ln -sf "$INSTALL_DIR/bin/agentshield" "$INSTALL_DIR/$BINARY_NAME"
    else
        error "Expected binary 'agentshield' not found in archive"
    fi
    log "Binary downloaded and installed"
}

# Generate token
generate_token() {
    if command -v openssl >/dev/null 2>&1; then openssl rand -hex 32
    elif command -v python3 >/dev/null 2>&1; then python3 -c "import secrets; print(secrets.token_hex(32))"
    else head -c 32 /dev/urandom | base64 | tr -d '\n' | head -c 64; fi
}

# Setup directories and rules
setup_files() {
    log "Setting up directory structure..."
    mkdir -p "$INSTALL_DIR/rules"
    OLD_UMASK_DB=$(umask)
    umask 077
    touch "$INSTALL_DIR/agentshield.db"
    umask "$OLD_UMASK_DB"
    
    log "Setting up security rules..."
    if command -v git >/dev/null 2>&1; then
        git clone --depth 1 "https://github.com/$RULES_REPO.git" "$INSTALL_DIR/rules-tmp" 2>/dev/null && {
            cp -r "$INSTALL_DIR/rules-tmp/"* "$INSTALL_DIR/rules/" 2>/dev/null || true
            rm -rf "$INSTALL_DIR/rules-tmp"; log "Downloaded latest rules"; return
        }
    fi
    
    # Fallback basic rule
    cat > "$INSTALL_DIR/rules/basic.yml" << 'EOF'
title: Basic AgentShield Rules
id: basic-rules
description: Essential security rules
rules:
  - id: file-access-monitor
    title: Suspicious File Access
    logsource: {category: agent-tool}
    detection:
      selection: {tool: file_operation, path|contains: ['/etc/passwd', '/etc/shadow', '.ssh/']}
      condition: selection
    level: medium
EOF
    log "Created basic security rules"
}

# Create config
create_config() {
    log "Creating configuration..."
    AUTH_TOKEN=$(generate_token)
    OLD_UMASK=$(umask)
    umask 077
    cat > "$CONFIG_FILE" << EOF
server:
  addr: "127.0.0.1"
  port: ${AGENTSHIELD_PORT:-8433}
auth:
  token: "$AUTH_TOKEN"
rules:
  dir: "$INSTALL_DIR/rules"
  hot_reload: true
store:
  sqlite_path: "$INSTALL_DIR/agentshield.db"
evaluation_mode: "enforce"
log_level: "info"
# triage:
#   enabled: true
#   provider: "openai"
#   model: "gpt-4o-mini"
#   api_key: "your-key"
#   max_tokens: 500
#   timeout_sec: 10
EOF
    umask "$OLD_UMASK"
    log "Configuration created"
}

# Setup service (systemd on Linux, launchd on macOS)
setup_service() {
    if [ "${AGENTSHIELD_E2E_MODE:-0}" = "1" ]; then
        log "E2E mode: skipping service registration, starting directly"
        "$INSTALL_DIR/$BINARY_NAME" serve --config "$CONFIG_FILE" --daemon=false \
            >"$INSTALL_DIR/engine.log" 2>&1 &
        echo $! > "$INSTALL_DIR/agentshield-e2e.pid"
        sleep 2
        return
    fi
    if [ "$OS" = "darwin" ]; then setup_launchd
    elif command -v systemctl >/dev/null 2>&1; then setup_systemd
    else warn "No service manager found — you'll need to start the engine manually"; fi
}

setup_systemd() {
    log "Creating systemd service..."
    mkdir -p "$HOME/.config/systemd/user"
    cat > "$HOME/.config/systemd/user/$SERVICE_NAME.service" << EOF
[Unit]
Description=AgentShield Detection Engine
After=network.target
[Service]
Type=simple
ExecStart="$INSTALL_DIR/$BINARY_NAME" serve --config "$CONFIG_FILE"
Restart=always
RestartSec=5
[Install]
WantedBy=default.target
EOF
    systemctl --user daemon-reload
    systemctl --user enable "$SERVICE_NAME" >/dev/null 2>&1 || warn "Failed to enable service"
    log "Systemd service configured"
}

setup_launchd() {
    log "Creating launchd service..."
    PLIST_DIR="$HOME/Library/LaunchAgents"
    PLIST_FILE="$PLIST_DIR/ai.agentshield.engine.plist"
    mkdir -p "$PLIST_DIR"
    cat > "$PLIST_FILE" << EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>ai.agentshield.engine</string>
    <key>ProgramArguments</key>
    <array>
        <string>$INSTALL_DIR/$BINARY_NAME</string>
        <string>serve</string>
        <string>--config</string>
        <string>$CONFIG_FILE</string>
    </array>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <true/>
    <key>StandardOutPath</key>
    <string>$INSTALL_DIR/engine.log</string>
    <key>StandardErrorPath</key>
    <string>$INSTALL_DIR/engine.log</string>
</dict>
</plist>
EOF
    log "Launchd service configured at $PLIST_FILE"
}

# Install plugin code into OpenClaw and patch config
patch_openclaw_config() {
    log "Configuring OpenClaw integration..."
    AUTH_TOKEN=$(grep -m1 "^  token:" "$CONFIG_FILE" | awk '{print $2}' | tr -d '"')
    if ! command -v openclaw >/dev/null 2>&1; then
        warn "OpenClaw CLI not available - manual plugin configuration required"
        return
    fi

    # Step 1: Install the published npm package so OpenClaw can load it.
    log "Installing @agentshield-ai/openclaw-plugin from npm..."
    if openclaw plugins install @agentshield-ai/openclaw-plugin 2>/dev/null; then
        log "OpenClaw plugin installed"
    else
        warn "openclaw plugins install failed — plugin may not load"
    fi

    # Step 2: Set config keys individually (openclaw config set accepts one key=value).
    local ENDPOINT="http://127.0.0.1:${AGENTSHIELD_PORT:-8433}/api/v1/evaluate"
    local failed=0
    openclaw config set plugins.entries.agentshield.enabled true 2>/dev/null || failed=1
    openclaw config set plugins.entries.agentshield.config.enabled true 2>/dev/null || failed=1
    openclaw config set plugins.entries.agentshield.config.endpoint "$ENDPOINT" 2>/dev/null || failed=1
    openclaw config set plugins.entries.agentshield.config.auth_token "$AUTH_TOKEN" 2>/dev/null || failed=1
    openclaw config set plugins.entries.agentshield.config.timeout_ms 200 2>/dev/null || failed=1
    openclaw config set plugins.entries.agentshield.config.timeout_policy block 2>/dev/null || failed=1
    if [ "$failed" -eq 0 ]; then
        log "OpenClaw configuration updated"
    else
        warn "Some OpenClaw config keys failed to set — check openclaw config manually"
    fi
}

# Start and check
start_and_check() {
    if [ "${AGENTSHIELD_E2E_MODE:-0}" != "1" ]; then
        log "Starting AgentShield..."
        if [ "$OS" = "darwin" ]; then
            launchctl load "$HOME/Library/LaunchAgents/ai.agentshield.engine.plist" 2>/dev/null || warn "Failed to load launchd service"
            sleep 2
        elif command -v systemctl >/dev/null 2>&1; then
            systemctl --user start "$SERVICE_NAME" || warn "Failed to start service"; sleep 2
        else "$INSTALL_DIR/$BINARY_NAME" serve --config "$CONFIG_FILE" --daemon & sleep 3; fi
    fi

    log "Health check..."
    AUTH_TOKEN=$(grep -m1 "^  token:" "$CONFIG_FILE" | awk '{print $2}' | tr -d '\"')
    AUTH_HEADER_FILE=$(mktemp "${TMPDIR:-/tmp}/agentshield-header-XXXXXX")
    CLEANUP_FILES+=("$AUTH_HEADER_FILE")
    printf 'Authorization: Bearer %s' "$AUTH_TOKEN" > "$AUTH_HEADER_FILE"
    for _i in $(seq 1 10); do
        curl -s -H @"$AUTH_HEADER_FILE" "http://127.0.0.1:${AGENTSHIELD_PORT:-8433}/api/v1/health" >/dev/null 2>&1 && {
            rm -f "$AUTH_HEADER_FILE"
            log "✓ Engine is healthy"; return 0
        }
        sleep 1
    done
    rm -f "$AUTH_HEADER_FILE"
    warn "Health check failed"
}

# Main
main() {
    log "Starting AgentShield installation..."
    detect_platform; download_binary; setup_files; create_config; setup_service; patch_openclaw_config; start_and_check
    
    log "✅ Installation complete!"
    echo -e "\n📋 Next Steps:"
    echo "  • Check status: $INSTALL_DIR/$BINARY_NAME status"
    if [ "$OS" = "darwin" ]; then
        echo "  • View logs: tail -f $INSTALL_DIR/engine.log"
        echo "  • Stop:  launchctl unload ~/Library/LaunchAgents/ai.agentshield.engine.plist"
        echo "  • Start: launchctl load ~/Library/LaunchAgents/ai.agentshield.engine.plist"
    elif command -v systemctl >/dev/null 2>&1; then
        echo "  • View logs: journalctl --user -u $SERVICE_NAME -f"
        echo "  • Stop:  systemctl --user stop $SERVICE_NAME"
        echo "  • Start: systemctl --user start $SERVICE_NAME"
    fi
    echo "  • Edit config: $CONFIG_FILE"
    echo "  • Add rules: $INSTALL_DIR/rules/"
    echo -e "\n⚠️  Restart your OpenClaw session to load the plugin."
    echo -e "🔒 Auth token is in config — keep it secure!"
}

main "$@"