#!/bin/bash
# ELIDA Install Script - Cross-platform service installation
# Usage: ./scripts/install.sh [install|uninstall|status]

set -e

ELIDA_DIR="$(cd "$(dirname "$0")/.." && pwd)"
ELIDA_BIN="$ELIDA_DIR/bin/elida"
ELIDA_CONFIG="$ELIDA_DIR/configs/elida.yaml"

# Detect OS
detect_os() {
    case "$(uname -s)" in
        Darwin*)  echo "macos" ;;
        Linux*)   echo "linux" ;;
        MINGW*|MSYS*|CYGWIN*) echo "windows" ;;
        *)        echo "unknown" ;;
    esac
}

OS=$(detect_os)

# macOS: LaunchAgent
install_macos() {
    PLIST_PATH="$HOME/Library/LaunchAgents/com.elida.proxy.plist"

    cat > "$PLIST_PATH" << EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>com.elida.proxy</string>
    <key>ProgramArguments</key>
    <array>
        <string>$ELIDA_BIN</string>
        <string>-config</string>
        <string>$ELIDA_CONFIG</string>
    </array>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <true/>
    <key>StandardOutPath</key>
    <string>$HOME/.elida/elida.log</string>
    <key>StandardErrorPath</key>
    <string>$HOME/.elida/elida.log</string>
    <key>WorkingDirectory</key>
    <string>$ELIDA_DIR</string>
</dict>
</plist>
EOF

    mkdir -p "$HOME/.elida"
    launchctl load "$PLIST_PATH" 2>/dev/null || true
    echo "ELIDA installed as macOS LaunchAgent"
    echo "  Logs: $HOME/.elida/elida.log"
    echo "  Dashboard: http://localhost:9090"
}

uninstall_macos() {
    PLIST_PATH="$HOME/Library/LaunchAgents/com.elida.proxy.plist"
    launchctl unload "$PLIST_PATH" 2>/dev/null || true
    rm -f "$PLIST_PATH"
    echo "ELIDA LaunchAgent removed"
}

status_macos() {
    if launchctl list | grep -q "com.elida.proxy"; then
        echo "ELIDA is running (macOS LaunchAgent)"
        launchctl list | grep "com.elida.proxy"
    else
        echo "ELIDA is not running"
    fi
}

# Linux: systemd user service
install_linux() {
    SERVICE_DIR="$HOME/.config/systemd/user"
    SERVICE_PATH="$SERVICE_DIR/elida.service"

    mkdir -p "$SERVICE_DIR"

    cat > "$SERVICE_PATH" << EOF
[Unit]
Description=ELIDA - Edge Layer for Intelligent Defense of Agents
After=network.target

[Service]
Type=simple
ExecStart=$ELIDA_BIN -config $ELIDA_CONFIG
Restart=always
RestartSec=5
WorkingDirectory=$ELIDA_DIR

[Install]
WantedBy=default.target
EOF

    mkdir -p "$HOME/.elida"
    systemctl --user daemon-reload
    systemctl --user enable elida
    systemctl --user start elida
    echo "ELIDA installed as systemd user service"
    echo "  Status: systemctl --user status elida"
    echo "  Logs: journalctl --user -u elida -f"
    echo "  Dashboard: http://localhost:9090"
}

uninstall_linux() {
    systemctl --user stop elida 2>/dev/null || true
    systemctl --user disable elida 2>/dev/null || true
    rm -f "$HOME/.config/systemd/user/elida.service"
    systemctl --user daemon-reload
    echo "ELIDA systemd service removed"
}

status_linux() {
    systemctl --user status elida 2>/dev/null || echo "ELIDA is not running"
}

# Windows: Creates a startup script (run as admin for service)
install_windows() {
    STARTUP_DIR="$APPDATA/Microsoft/Windows/Start Menu/Programs/Startup"
    BAT_PATH="$STARTUP_DIR/elida.bat"

    cat > "$BAT_PATH" << EOF
@echo off
start /B "$ELIDA_BIN" -config "$ELIDA_CONFIG"
EOF

    mkdir -p "$HOME/.elida"
    echo "ELIDA startup script created"
    echo "  Location: $BAT_PATH"
    echo "  Dashboard: http://localhost:9090"
    echo ""
    echo "For Windows Service (run as Administrator):"
    echo "  sc create ELIDA binPath=\"$ELIDA_BIN -config $ELIDA_CONFIG\" start=auto"
}

uninstall_windows() {
    STARTUP_DIR="$APPDATA/Microsoft/Windows/Start Menu/Programs/Startup"
    rm -f "$STARTUP_DIR/elida.bat"
    echo "ELIDA startup script removed"
    echo "If installed as service, run as Administrator:"
    echo "  sc stop ELIDA && sc delete ELIDA"
}

status_windows() {
    if tasklist | grep -q "elida"; then
        echo "ELIDA is running"
    else
        echo "ELIDA is not running"
    fi
}

# Environment setup (all platforms)
setup_env() {
    SHELL_RC=""
    case "$SHELL" in
        */zsh)  SHELL_RC="$HOME/.zshrc" ;;
        */bash) SHELL_RC="$HOME/.bashrc" ;;
        *)      SHELL_RC="$HOME/.profile" ;;
    esac

    if ! grep -q "ELIDA proxy" "$SHELL_RC" 2>/dev/null; then
        cat >> "$SHELL_RC" << 'EOF'

# ELIDA proxy - Route AI tools through ELIDA
export ANTHROPIC_BASE_URL=http://localhost:8080
# export OPENAI_BASE_URL=http://localhost:8080
# export MISTRAL_API_ENDPOINT=http://localhost:8080
EOF
        echo "Environment variables added to $SHELL_RC"
        echo "Run: source $SHELL_RC"
    else
        echo "Environment variables already configured in $SHELL_RC"
    fi
}

# Main
case "$1" in
    install)
        echo "Installing ELIDA on $OS..."
        case "$OS" in
            macos)   install_macos ;;
            linux)   install_linux ;;
            windows) install_windows ;;
            *)       echo "Unknown OS: $OS"; exit 1 ;;
        esac
        echo ""
        setup_env
        ;;
    uninstall)
        echo "Uninstalling ELIDA on $OS..."
        case "$OS" in
            macos)   uninstall_macos ;;
            linux)   uninstall_linux ;;
            windows) uninstall_windows ;;
            *)       echo "Unknown OS: $OS"; exit 1 ;;
        esac
        ;;
    status)
        case "$OS" in
            macos)   status_macos ;;
            linux)   status_linux ;;
            windows) status_windows ;;
            *)       echo "Unknown OS: $OS"; exit 1 ;;
        esac
        ;;
    env)
        setup_env
        ;;
    *)
        echo "ELIDA Install Script"
        echo ""
        echo "Usage: $0 [command]"
        echo ""
        echo "Commands:"
        echo "  install    Install ELIDA as a system service"
        echo "  uninstall  Remove ELIDA service"
        echo "  status     Check if ELIDA is running"
        echo "  env        Add environment variables to shell config"
        echo ""
        echo "Detected OS: $OS"
        ;;
esac
