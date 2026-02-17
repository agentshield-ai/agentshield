#!/bin/bash

set -euo pipefail

# AgentShield Uninstallation Script

# Configuration
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

confirm() {
    local message="$1"
    local default="${2:-N}"
    
    if [[ "$default" == "Y" ]]; then
        local prompt="[Y/n]"
    else
        local prompt="[y/N]"
    fi
    
    read -p "$message $prompt: " -n 1 -r
    echo
    
    if [[ "$default" == "Y" ]]; then
        [[ ! $REPLY =~ ^[Nn]$ ]]
    else
        [[ $REPLY =~ ^[Yy]$ ]]
    fi
}

stop_and_remove_services() {
    if ! command -v systemctl >/dev/null 2>&1; then
        warn "systemctl not found - skipping systemd service cleanup"
        return 0
    fi
    
    log "Stopping and removing AgentShield services..."
    
    local services=(
        "agentshield-monitor"
        "agentshield-realtime"  
        "agentshield-sidecar"
    )
    
    for service in "${services[@]}"; do
        local service_file="$HOME/.config/systemd/user/${service}.service"
        
        if systemctl --user is-active "${service}.service" >/dev/null 2>&1; then
            log "Stopping ${service} service..."
            systemctl --user stop "${service}.service" || warn "Failed to stop ${service}"
        fi
        
        if systemctl --user is-enabled "${service}.service" >/dev/null 2>&1; then
            log "Disabling ${service} service..."
            systemctl --user disable "${service}.service" || warn "Failed to disable ${service}"
        fi
        
        if [[ -f "$service_file" ]]; then
            rm -f "$service_file"
            log "Removed ${service} service file"
        fi
    done
    
    systemctl --user daemon-reload
    log "Systemd services cleaned up"
}

remove_openclaw_plugin() {
    local plugin_dir="${OPENCLAW_EXTENSIONS_DIR}/agentshield"
    
    if [[ -d "$plugin_dir" ]]; then
        log "Removing OpenClaw plugin..."
        rm -rf "$plugin_dir"
        log "OpenClaw plugin removed"
    else
        info "OpenClaw plugin not found - nothing to remove"
    fi
}

remove_python_environment() {
    if [[ -d "$VENV_PATH" ]]; then
        log "Removing Python virtual environment..."
        rm -rf "$VENV_PATH"
        log "Python virtual environment removed"
    else
        info "Python virtual environment not found - nothing to remove"
    fi
}

remove_sidecar_binary() {
    local sidecar_path="${AGENTSHIELD_DATA_DIR}/bin/agentshield-sidecar"
    
    if [[ -f "$sidecar_path" ]]; then
        log "Removing sidecar binary..."
        rm -f "$sidecar_path"
        log "Sidecar binary removed"
    fi
    
    # Remove bin directory if empty
    if [[ -d "${AGENTSHIELD_DATA_DIR}/bin" ]] && [[ -z "$(ls -A "${AGENTSHIELD_DATA_DIR}/bin")" ]]; then
        rmdir "${AGENTSHIELD_DATA_DIR}/bin"
    fi
}

revert_openclaw_config() {
    local openclaw_config_dir="$HOME/.openclaw"
    local config_file="${openclaw_config_dir}/config.json"
    
    if [[ ! -f "$config_file" ]]; then
        info "OpenClaw config not found - nothing to revert"
        return 0
    fi
    
    log "Reverting OpenClaw configuration..."
    
    # Find and use the most recent backup
    local backup_file
    backup_file=$(find "$openclaw_config_dir" -name "config.json.backup.*" -type f | sort -V | tail -1)
    
    if [[ -n "$backup_file" ]] && confirm "Restore OpenClaw config from backup ($backup_file)?"; then
        cp "$backup_file" "$config_file"
        log "OpenClaw config restored from backup"
    else
        # Try to remove plugin config manually
        if command -v python3 >/dev/null 2>&1; then
            python3 << EOF
import json
import sys

try:
    with open('$config_file', 'r') as f:
        config = json.load(f)
    
    # Remove AgentShield plugin config
    if 'plugins' in config and 'agentshield' in config['plugins']:
        del config['plugins']['agentshield']
        
        # Remove plugins section if empty
        if not config['plugins']:
            del config['plugins']
        
        with open('$config_file', 'w') as f:
            json.dump(config, f, indent=2)
        
        print("AgentShield plugin config removed from OpenClaw")
    else:
        print("AgentShield plugin not found in OpenClaw config")
        
except Exception as e:
    print(f"Error updating config: {e}", file=sys.stderr)
    sys.exit(1)
EOF
        else
            warn "Python3 not found - cannot automatically revert OpenClaw config"
            warn "Please manually remove the 'agentshield' section from $config_file"
        fi
    fi
}

remove_data_directory() {
    if [[ ! -d "$AGENTSHIELD_DATA_DIR" ]]; then
        info "AgentShield data directory not found - nothing to remove"
        return 0
    fi
    
    echo
    info "The following AgentShield data will be removed:"
    info "- Configuration files"
    info "- Alert database and logs"
    info "- Detection rules"
    info "- Any customizations"
    echo
    
    if confirm "Remove AgentShield data directory ($AGENTSHIELD_DATA_DIR)?" "N"; then
        log "Removing AgentShield data directory..."
        rm -rf "$AGENTSHIELD_DATA_DIR"
        log "AgentShield data directory removed"
    else
        info "Keeping AgentShield data directory at: $AGENTSHIELD_DATA_DIR"
        info "You can manually remove it later if needed"
    fi
}

cleanup_backups() {
    local openclaw_config_dir="$HOME/.openclaw"
    
    if confirm "Remove OpenClaw config backups created during installation?" "N"; then
        find "$openclaw_config_dir" -name "config.json.backup.*" -type f -delete 2>/dev/null || true
        log "OpenClaw config backups removed"
    fi
}

show_final_message() {
    echo
    log "AgentShield uninstallation completed!"
    echo
    info "What was removed:"
    info "- Systemd services (agentshield-monitor, agentshield-realtime, agentshield-sidecar)"
    info "- OpenClaw plugin (${OPENCLAW_EXTENSIONS_DIR}/agentshield)"
    info "- Python virtual environment"
    info "- Sidecar binary"
    
    if [[ -d "$AGENTSHIELD_DATA_DIR" ]]; then
        echo
        info "What remains:"
        info "- AgentShield data directory: $AGENTSHIELD_DATA_DIR"
        info "  (contains logs, database, and configuration)"
    fi
    
    echo
    info "To complete the removal:"
    info "1. Restart OpenClaw to disable the AgentShield plugin"
    info "2. Remove any remaining references from your shell profile"
    info "3. Optionally remove the data directory manually"
}

main() {
    local force=false
    
    # Parse command line arguments
    while [[ $# -gt 0 ]]; do
        case $1 in
            --force)
                force=true
                shift
                ;;
            --help|-h)
                echo "AgentShield Uninstallation Script"
                echo
                echo "Usage: $0 [OPTIONS]"
                echo
                echo "Options:"
                echo "  --force    Skip confirmation prompts"
                echo "  --help     Show this help message"
                exit 0
                ;;
            *)
                error "Unknown option: $1"
                exit 1
                ;;
        esac
    done
    
    echo "AgentShield Uninstallation"
    echo "========================="
    echo
    
    if [[ "$force" != "true" ]]; then
        warn "This will remove AgentShield and its components from your system."
        echo
        if ! confirm "Do you want to continue?" "N"; then
            info "Uninstallation cancelled."
            exit 0
        fi
        echo
    fi
    
    log "Starting AgentShield uninstallation..."
    
    # Stop and remove systemd services
    stop_and_remove_services
    
    # Remove OpenClaw plugin
    remove_openclaw_plugin
    
    # Remove Python virtual environment
    remove_python_environment
    
    # Remove sidecar binary
    remove_sidecar_binary
    
    # Revert OpenClaw configuration
    revert_openclaw_config
    
    # Remove data directory (with confirmation)
    remove_data_directory
    
    # Cleanup backups (with confirmation)
    cleanup_backups
    
    # Show final message
    show_final_message
}

# Handle Ctrl+C gracefully
trap 'echo -e "\n${RED}Uninstallation cancelled.${NC}"; exit 1' INT

main "$@"