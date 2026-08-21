#!/bin/sh
# Runs after install or upgrade.
set -e

# The unit file may be new or changed, so systemd must re-read it before
# anything tries to start or restart the service.
if command -v systemctl >/dev/null 2>&1; then
    systemctl daemon-reload >/dev/null 2>&1 || true

    # An upgrade restarts a running service; a fresh install leaves it stopped
    # so an operator can review the configuration first.
    if systemctl is-active --quiet apm2go 2>/dev/null; then
        systemctl restart apm2go >/dev/null 2>&1 || true
        echo "apm2go restarted."
    else
        cat <<'MESSAGE'

apm2go is installed.

  Review the configuration:  /etc/apm2go/config.yaml
  Start it:                  systemctl enable --now apm2go
  Then open:                 http://<this-host>:8080

apm2go will discover the Java processes on this host and instrument them
without restarting them. Run `apm2go list` to see what it finds first.

MESSAGE
    fi
fi
