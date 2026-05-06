#!/bin/bash
# Usage: APPLE_ID=you@example.com APP_PASSWORD=xxxx-xxxx-xxxx-xxxx ./release.sh [version]
set -e

APPLE_ID="${APPLE_ID:?Set APPLE_ID=your@apple.id}"
APP_PASSWORD="${APP_PASSWORD:?Set APP_PASSWORD=app-specific-password}"
VERSION="${1:-$(date +%Y.%m.%d)}"

SIGN_ID=$(security find-identity -v -p codesigning 2>/dev/null \
    | grep "Developer ID Application" \
    | head -1 \
    | awk -F'"' '{print $2}')
TEAM_ID=$(echo "$SIGN_ID" | grep -o '([A-Z0-9]*)' | tr -d '()')

if [ -z "$SIGN_ID" ]; then
    echo "Error: no Developer ID Application certificate found in Keychain."
    exit 1
fi
echo "▸ Signing identity: $SIGN_ID"
DIST="/tmp/mdm-watch-dist"
ZIP="/tmp/mdm-watch-${VERSION}.zip"

echo "▸ Version: $VERSION"

# ── 1. Build Go daemon ────────────────────────────────────────────────────────
echo "▸ Building mdm-watch (Go)..."
go build -ldflags "-X main.version=$VERSION" -o "$DIST/mdm-watch" .
codesign --sign "$SIGN_ID" --force --options runtime "$DIST/mdm-watch"

# ── 2. Build Swift notifier ───────────────────────────────────────────────────
echo "▸ Building mdm-notifier (Swift)..."
swiftc mdm-notifier/main.swift \
    -o /tmp/mdm-notifier-bin \
    -framework Cocoa \
    -framework UserNotifications

APP="$DIST/mdm-notifier.app"
mkdir -p "$APP/Contents/MacOS"
cp /tmp/mdm-notifier-bin "$APP/Contents/MacOS/mdm-notifier"
cp mdm-notifier/Info.plist "$APP/Contents/Info.plist"

echo "▸ Signing mdm-notifier.app..."
codesign --sign "$SIGN_ID" --force --deep --options runtime "$APP"

# ── 3. Notarize ───────────────────────────────────────────────────────────────
echo "▸ Notarizing..."
# Notarytool requires a zip
NOTARIZE_ZIP="/tmp/mdm-notifier-notarize.zip"
ditto -c -k --keepParent "$APP" "$NOTARIZE_ZIP"

xcrun notarytool submit "$NOTARIZE_ZIP" \
    --apple-id "$APPLE_ID" \
    --password "$APP_PASSWORD" \
    --team-id "$TEAM_ID" \
    --wait

echo "▸ Stapling..."
xcrun stapler staple "$APP"

# ── 4. Pack release zip ───────────────────────────────────────────────────────
echo "▸ Packing release..."
cp com.dzarlax.mdm-watch.plist "$DIST/"
cat > "$DIST/install.sh" << 'INSTALL'
#!/bin/bash
set -e
DIR="$(cd "$(dirname "$0")" && pwd)"

echo "▸ Installing mdm-watch..."
sudo cp "$DIR/mdm-watch" /usr/local/bin/mdm-watch

echo "▸ Installing mdm-notifier.app..."
sudo cp -R "$DIR/mdm-notifier.app" /usr/local/lib/mdm-notifier.app
sudo tee /usr/local/bin/mdm-notifier > /dev/null << 'EOF'
#!/bin/bash
exec /usr/local/lib/mdm-notifier.app/Contents/MacOS/mdm-notifier "$@"
EOF
sudo chmod +x /usr/local/bin/mdm-notifier

echo "▸ Installing LaunchAgent..."
cp "$DIR/com.dzarlax.mdm-watch.plist" ~/Library/LaunchAgents/
launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/com.dzarlax.mdm-watch.plist 2>/dev/null || \
launchctl kickstart -k gui/$(id -u)/com.dzarlax.mdm-watch

echo ""
echo "✓ Done. Grant notification permission:"
echo "  mdm-notifier -title 'MDM Watch' -message 'Installed' -open 'http://localhost:8765'"
echo ""
echo "✓ Open UI:"
echo "  open http://localhost:8765"
INSTALL
chmod +x "$DIST/install.sh"

rm -f "$ZIP"
ditto -c -k --keepParent "$DIST" "$ZIP"
echo "▸ Release archive: $ZIP"

# ── 5. GitHub Release ─────────────────────────────────────────────────────────
echo "▸ Creating GitHub Release v$VERSION..."
gh release create "v$VERSION" "$ZIP" \
    --title "v$VERSION" \
    --notes "### Install

Download and unzip, then run:

\`\`\`bash
unzip mdm-watch-${VERSION}.zip
cd mdm-watch-dist
./install.sh
\`\`\`

On first run, grant notification permission:
\`\`\`bash
mdm-notifier -title 'MDM Watch' -message 'Hello' -open 'http://localhost:8765'
\`\`\`

Then open the UI: http://localhost:8765

---
Built for macOS $(sw_vers -productVersion), Apple Silicon + Intel."

echo ""
echo "✓ Release published: https://github.com/dzarlax/mdm-watch/releases/tag/v$VERSION"
