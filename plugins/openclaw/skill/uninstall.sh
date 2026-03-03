#!/bin/bash

set -e

# AgentShield Uninstaller - v2.0.0
# Clean removal of single Go binary architecture

INSTALL_DIR="${AGENTSHIELD_HOME:-$HOME/.agentshield}"
SERVICE_NAME="agentshield-engine"

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

log() { echo -e "${GREEN}[AgentShield]${NC} $1"; }
warn() { echo -e "${YELLOW}[Warning]${NC} $1"; }
error() { echo -e "${RED}[Error]${NC} $1"; }

# Confirmation prompt
confirm_removal() {
    echo -e "\n${YELLOW}WARNING: This will remove AgentShield and all its data.${NC}"
    echo "The following will be deleted:"
    echo "  • AgentShield binary and configuration"
    echo "  • Security rules and database"
    echo "  • Systemd service (if installed)"
    echo "  • OpenClaw plugin configuration"
    echo
    
    read -p "Continue? [y/N]: " -n 1 -r
    echo
    
    if [[ ! $REPLY =~ ^[Yy]$ ]]; then
        log "Uninstall cancelled"
        exit 0
    fi
}

# Stop and disable service (systemd on Linux, launchd on macOS)
stop_service() {
    local os
    os=$(uname -s | tr '[:upper:]' '[:lower:]')

    # Kill E2E process if running
    local e2e_pid_file="$INSTALL_DIR/agentshield-e2e.pid"
    if [ -f "$e2e_pid_file" ]; then
        local e2e_pid
        e2e_pid=$(cat "$e2e_pid_file")
        if kill -0 "$e2e_pid" 2>/dev/null; then
            log "Stopping E2E process (PID $e2e_pid)..."
            kill "$e2e_pid" 2>/dev/null || true
        fi
        rm -f "$e2e_pid_file"
    fi

    if [ "$os" = "darwin" ]; then
        log "Stopping AgentShield launchd service..."
        PLIST="$HOME/Library/LaunchAgents/ai.agentshield.engine.plist"
        launchctl unload "$PLIST" 2>/dev/null || true
        rm -f "$PLIST"
        log "Launchd service stopped and removed"
    elif command -v systemctl >/dev/null 2>&1; then
        log "Stopping AgentShield systemd service..."
        systemctl --user stop "$SERVICE_NAME" 2>/dev/null || true
        systemctl --user disable "$SERVICE_NAME" 2>/dev/null || true
        rm -f "$HOME/.config/systemd/user/$SERVICE_NAME.service"
        systemctl --user daemon-reload 2>/dev/null || true
        log "Systemd service stopped and disabled"
    else
        pkill -f "agentshield.*serve" 2>/dev/null || true
        log "Stopped AgentShield processes"
    fi
}

# Remove installation directory
remove_files() {
    if [ -d "$INSTALL_DIR" ]; then
        log "Removing AgentShield files..."
        rm -rf "$INSTALL_DIR"
        log "Installation directory removed"
    else
        warn "Installation directory not found: $INSTALL_DIR"
    fi
}

# Uninstall plugin from OpenClaw and revert configuration
revert_openclaw_config() {
    log "Reverting OpenClaw configuration..."

    if ! command -v openclaw >/dev/null 2>&1; then
        warn "OpenClaw CLI not available - you may need to manually remove AgentShield from OpenClaw config"
        return
    fi

    # Step 1: Uninstall the plugin package.
    if openclaw plugins uninstall @agentshield-ai/openclaw-plugin 2>/dev/null; then
        log "OpenClaw plugin uninstalled"
    else
        warn "openclaw plugins uninstall failed — you may need to remove the plugin manually"
    fi

    # Step 2: Disable the config entry.
    if openclaw config patch plugins.entries.agentshield.enabled=false 2>/dev/null; then
        log "OpenClaw configuration reverted via CLI"
    else
        warn "Failed to disable agentshield in OpenClaw config"
    fi
}

# Remove from PATH if added
cleanup_path() {
    # Check common shell rc files for any AgentShield PATH additions
    for rcfile in "$HOME/.bashrc" "$HOME/.zshrc" "$HOME/.profile"; do
        if [ -f "$rcfile" ] && grep -q "agentshield" "$rcfile"; then
            warn "Found AgentShield references in $rcfile - you may want to clean them up manually"
        fi
    done
}

# Main uninstallation
main() {
    log "AgentShield Uninstaller"
    
    # Check if AgentShield is installed
    if [ ! -d "$INSTALL_DIR" ] && [ ! -f "$HOME/.config/systemd/user/$SERVICE_NAME.service" ] && [ ! -f "$HOME/Library/LaunchAgents/ai.agentshield.engine.plist" ]; then
        log "AgentShield doesn't appear to be installed"
        exit 0
    fi
    
    confirm_removal
    
    log "Starting AgentShield removal..."
    
    stop_service
    remove_files
    revert_openclaw_config
    cleanup_path
    
    log "✅ AgentShield has been completely removed!"
    echo
    echo "📋 Cleanup complete:"
    echo "  • Service stopped and disabled"
    echo "  • All files and configuration removed"
    echo "  • OpenClaw plugin configuration reverted"
    echo
    echo "Thank you for using AgentShield! 🛡️"
}

main "$@"