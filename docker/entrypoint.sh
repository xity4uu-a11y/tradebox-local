#!/usr/bin/env bash
set -euo pipefail

export DISPLAY="${DISPLAY:-:99}"
export WINEPREFIX="${WINEPREFIX:-/mt5-data/wineprefix}"
export MT5_DIR="${MT5_DIR:-/mt5-data/terminal}"
export TRADEBOX_CONFIG="${TRADEBOX_CONFIG:-/mt5-data/tradebox.json}"

mkdir -p "${WINEPREFIX}" "${MT5_DIR}/MQL5/Logs" "${MT5_DIR}/Logs"

# Clean stale X11 lock files after unexpected container restarts
rm -f /tmp/.X99-lock

# 1. Start headless virtual framebuffer + lightweight Fluxbox window manager
Xvfb "${DISPLAY}" -screen 0 "${SCREEN_WIDTH:-1366}x${SCREEN_HEIGHT:-768}x${SCREEN_DEPTH:-24}" -ac &
XVFB_PID=$!
sleep 1

fluxbox -display "${DISPLAY}" >/tmp/fluxbox.log 2>&1 &

# 2. Start x11vnc bound exclusively to container loopback (exposed only through noVNC/websockify)
VNC_AUTH_ARGS="-nopw"
if [[ -n "${VNC_PASSWORD:-}" ]]; then
  mkdir -p "${HOME}/.vnc"
  x11vnc -storepasswd "${VNC_PASSWORD}" "${HOME}/.vnc/passwd" >/dev/null
  VNC_AUTH_ARGS="-rfbauth ${HOME}/.vnc/passwd"
fi

# shellcheck disable=SC2086
x11vnc -display "${DISPLAY}" -localhost -forever -shared -rfbport 5900 ${VNC_AUTH_ARGS} >/tmp/x11vnc.log 2>&1 &

# 3. Start browser-accessible noVNC gateway on port 6080
websockify --web=/usr/share/novnc/ 6080 127.0.0.1:5900 >/tmp/novnc.log 2>&1 &

# 4. Initialize Wine prefix cleanly on first boot
if [[ ! -f "${WINEPREFIX}/system.reg" ]]; then
  echo "[entrypoint] Initializing Wine64 prefix in ${WINEPREFIX}..."
  wineboot --init >/tmp/wineboot.log 2>&1 || true
fi

# 5. Optional automatic download of official MetaQuotes MT5 installer on first boot
if [[ "${AUTO_DOWNLOAD_MT5:-false}" == "true" && ! -f "${MT5_DIR}/terminal64.exe" && ! -f "${MT5_DIR}/mt5setup.exe" ]]; then
  echo "[entrypoint] Downloading official MetaTrader 5 installer into ${MT5_DIR}/mt5setup.exe..."
  curl -fsSL "https://download.mql5.com/cdn/web/metaquotes.software.corp/mt5/mt5setup.exe" \
    -o "${MT5_DIR}/mt5setup.exe" || echo "[entrypoint] Download skipped; place terminal64.exe in ./mt5-data/terminal manually."
fi

# 6. Generate container-ready tradebox.json if not supplied via volume mount
if [[ ! -f "${TRADEBOX_CONFIG}" ]]; then
  cat >"${TRADEBOX_CONFIG}" <<EOF
{
  "terminal_command": "wine",
  "terminal_args": ["${MT5_DIR}/terminal64.exe", "/portable"],
  "working_dir": "${MT5_DIR}",
  "auto_start": ${AUTO_START_MT5:-false},
  "mt5_logs_dir": "${MT5_DIR}",
  "brokers": [
    {
      "name": "MetaQuotes-Demo-EU",
      "host": "access.metatrader5.com",
      "port": 443,
      "max_latency_ms": 120,
      "timeout_ms": 3000
    },
    {
      "name": "Network-Baseline-CF",
      "host": "1.1.1.1",
      "port": 443,
      "max_latency_ms": 80,
      "timeout_ms": 3000
    }
  ],
  "restart_policy": {
    "max_restarts_per_hour": 12,
    "backoff_base_sec": 2,
    "backoff_max_sec": 60,
    "check_interval_sec": 5,
    "stable_reset_sec": 60
  },
  "latency_check_sec": 15,
  "journal_poll_sec": 2,
  "alerts": {
    "cooldown_sec": 300,
    "telegram": {
      "enabled": false,
      "bot_token": "\${TRADEBOX_TELEGRAM_BOT_TOKEN}",
      "chat_id": "\${TRADEBOX_TELEGRAM_CHAT_ID}"
    },
    "discord": {
      "enabled": false,
      "webhook_url": "\${TRADEBOX_DISCORD_WEBHOOK_URL}"
    },
    "slack": {
      "enabled": false,
      "webhook_url": "\${TRADEBOX_SLACK_WEBHOOK_URL}"
    },
    "webhook": {
      "enabled": false,
      "url": "\${TRADEBOX_WEBHOOK_URL}",
      "secret": "\${TRADEBOX_WEBHOOK_SECRET}"
    }
  },
  "novnc_url": "http://127.0.0.1:6080/vnc.html?autoconnect=1&resize=scale",
  "http_addr": "0.0.0.0:8092",
  "plugins_dir": "/opt/tradebox/plugins"
}
EOF
fi

cleanup() {
  kill "${XVFB_PID}" 2>/dev/null || true
}
trap cleanup EXIT

exec /usr/local/bin/tradebox serve \
  --config "${TRADEBOX_CONFIG}" \
  --addr "0.0.0.0:8092" \
  --plugins-dir "/opt/tradebox/plugins"
