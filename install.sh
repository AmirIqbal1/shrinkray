#!/usr/bin/env bash
#
# shrinkray installer for Ubuntu / Linux Mint
#
# Usage (once this is on GitHub):
#   curl -fsSL https://raw.githubusercontent.com/YOUR_USERNAME/shrinkray/main/install.sh | bash
#
set -euo pipefail

REPO_RAW="https://raw.githubusercontent.com/YOUR_USERNAME/shrinkray/main"
INSTALL_DIR="${HOME}/.local/bin"

echo "==> shrinkray installer"

if ! command -v ffmpeg >/dev/null 2>&1; then
  echo "==> ffmpeg not found - installing (this needs sudo)..."
  sudo apt update -y
  sudo apt install -y ffmpeg
else
  echo "==> ffmpeg already installed"
fi

# Sanity-check that this ffmpeg can actually do the job
if ! ffmpeg -hide_banner -encoders 2>/dev/null | grep -Eq "libx265|libsvtav1"; then
  echo "!! Warning: this ffmpeg build has neither HEVC (libx265) nor AV1"
  echo "   (libsvtav1) support. shrinkray needs one of them. On Ubuntu/Mint"
  echo "   the apt 'ffmpeg' package normally includes libx265 - if this"
  echo "   warning showed up, your distro's build may be unusual."
fi

mkdir -p "$INSTALL_DIR"

if [ -f "./shrinkray" ]; then
  # Running from a cloned repo
  cp "./shrinkray" "$INSTALL_DIR/shrinkray"
else
  # Running via curl | bash - fetch the script itself
  echo "==> Downloading shrinkray..."
  curl -fsSL "$REPO_RAW/shrinkray" -o "$INSTALL_DIR/shrinkray"
fi

chmod +x "$INSTALL_DIR/shrinkray"

case ":${PATH}:" in
  *":${INSTALL_DIR}:"*) ;;
  *)
    SHELL_RC="${HOME}/.bashrc"
    [ -n "${ZSH_VERSION:-}" ] && SHELL_RC="${HOME}/.zshrc"
    echo "export PATH=\"\$PATH:${INSTALL_DIR}\"" >> "$SHELL_RC"
    echo "==> Added ${INSTALL_DIR} to your PATH in ${SHELL_RC}"
    echo "    Run: source ${SHELL_RC}   (or just open a new terminal)"
    ;;
esac

echo "==> Installed! Try:  shrinkray --help"
