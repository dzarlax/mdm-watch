#!/bin/bash
set -e

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
APP="/usr/local/lib/mdm-notifier.app"
APP_CONTENTS="$APP/Contents"
APP_MACOS="$APP_CONTENTS/MacOS"

echo "▸ Compiling..."
swiftc "$SCRIPT_DIR/main.swift" \
    -o /tmp/mdm-notifier-bin \
    -framework Cocoa \
    -framework UserNotifications

echo "▸ Installing app bundle..."
sudo mkdir -p "$APP_MACOS"
sudo cp /tmp/mdm-notifier-bin "$APP_MACOS/mdm-notifier"
sudo cp "$SCRIPT_DIR/Info.plist" "$APP_CONTENTS/Info.plist"

echo "▸ Signing..."
# Prefer Developer ID (works without Gatekeeper prompt), fall back to ad-hoc
IDENTITY=$(security find-identity -v -p codesigning 2>/dev/null \
    | grep "Developer ID Application" \
    | head -1 \
    | awk -F'"' '{print $2}')

if [ -n "$IDENTITY" ]; then
    echo "  Using: $IDENTITY"
    sudo codesign --sign "$IDENTITY" --force --deep --options runtime "$APP"
else
    echo "  No Developer ID found — using ad-hoc signature."
    echo "  On first run, macOS may ask you to allow it in System Settings → Privacy & Security."
    sudo codesign --sign - --force --deep "$APP"
fi

echo "▸ Installing wrapper..."
sudo tee /usr/local/bin/mdm-notifier > /dev/null << 'EOF'
#!/bin/bash
exec /usr/local/lib/mdm-notifier.app/Contents/MacOS/mdm-notifier "$@"
EOF
sudo chmod +x /usr/local/bin/mdm-notifier

echo ""
echo "✓ Done. Run once to grant notification permission:"
echo "  mdm-notifier -title 'MDM Watch' -message 'Test' -open 'http://localhost:8765'"
