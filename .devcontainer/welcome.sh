#!/usr/bin/env bash
set -euo pipefail

echo ""
echo "╔══════════════════════════════════════════════════════════════╗"
echo "║          docker-gateway — demo environment                   ║"
echo "╚══════════════════════════════════════════════════════════════╝"
echo ""
echo "▶ Starting demo containers..."

docker compose -f docker-compose.demo.yml up -d --build

echo ""
echo "⏳ Waiting for gateway to be ready..."

MAX=60
COUNT=0
until curl -sf http://localhost:8080/_status/api > /dev/null 2>&1; do
  if [ "$COUNT" -ge "$MAX" ]; then
    echo "✗ Gateway did not start within ${MAX}s. Check: docker compose -f docker-compose.demo.yml logs gateway"
    exit 1
  fi
  sleep 2
  COUNT=$((COUNT + 2))
done

echo ""
echo "╔══════════════════════════════════════════════════════════════╗"
echo "║  ✓ Gateway is ready! Open the preview on port 8080          ║"
echo "╠══════════════════════════════════════════════════════════════╣"
echo "║                                                              ║"
echo "║  Scenarios to try (all on port 8080):                       ║"
echo "║                                                              ║"
echo "║  /_status          → Admin dashboard                        ║"
echo "║  /                 → Gateway home (redirects by Host)       ║"
echo "║                                                              ║"
echo "║  Send a request with Host header to wake a container:       ║"
echo "║  curl -H 'Host: slow-app.localhost' http://localhost:8080/  ║"
echo "║  curl -H 'Host: fail-app.localhost' http://localhost:8080/  ║"
echo "║  curl -H 'Host: dashboard.localhost' http://localhost:8080/ ║"
echo "║                                                              ║"
echo "║  Or open DEMO.md for full instructions.                     ║"
echo "╚══════════════════════════════════════════════════════════════╝"
echo ""

# Open DEMO.md in VS Code if available
if command -v code &> /dev/null; then
  code DEMO.md
fi
