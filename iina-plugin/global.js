/**
 * mpv-gotorrent - IINA Global Entry Script
 * Author: biohazard786
 * License: MIT
 */

const { core, menu, utils, preferences } = iina;

// State management for active companion daemon
let activeCompanion = null;

// Determine system CPU architecture ('arm64' for Apple Silicon, 'x86_64' for Intel)
async function detectArch() {
  try {
    const res = await utils.exec("uname", ["-m"]);
    if (res && res.stdout) {
      return res.stdout.trim().toLowerCase().includes("arm64") ? "arm64" : "amd64";
    }
  } catch (e) {
    // Fallback to arm64 on modern macOS
  }
  return "arm64";
}

// Locate companion executable across bundled plugin dir, mpv script dir, PATH, or auto-download
async function locateBinary() {
  const arch = await detectArch();
  const binName = `mpv-gotorrent-darwin-${arch}`;

  // 1. Check bundled binary in plugin directory
  const candidateBundled = [
    `bin/${binName}`,
    "bin/mpv-gotorrent",
    `@data/bin/${binName}`,
    "@data/bin/mpv-gotorrent"
  ];

  for (const rel of candidateBundled) {
    try {
      const resolved = utils.resolvePath(rel);
      if (resolved && utils.fileInPath(resolved)) {
        return resolved;
      }
    } catch (e) {
      // Continue searching
    }
  }

  // 2. Check standard mpv script installation directories
  const home = utils.resolvePath("~");
  if (home) {
    const standardPaths = [
      `${home}/.config/mpv/scripts/mpv-gotorrent/mpv-gotorrent`,
      `${home}/Library/Application Support/mpv/scripts/mpv-gotorrent/mpv-gotorrent`,
      "/usr/local/bin/mpv-gotorrent",
      "/opt/homebrew/bin/mpv-gotorrent"
    ];
    for (const p of standardPaths) {
      if (utils.fileInPath(p)) {
        return p;
      }
    }
  }

  // 3. Fallback to system PATH if available
  if (utils.fileInPath("mpv-gotorrent")) {
    return "mpv-gotorrent";
  }

  // 4. Auto-download companion binary from GitHub Releases (for Git / "Install from GitHub" installs)
  try {
    const downloadDest = utils.resolvePath(`@data/bin/${binName}`);
    const downloadDir = utils.resolvePath("@data/bin");

    await utils.exec("mkdir", ["-p", downloadDir]);

    const downloadUrl = `https://github.com/biohazard786/mpv-gotorrent/releases/latest/download/${binName}`;
    if (iina.hud && typeof iina.hud.show === "function") {
      iina.hud.show("Downloading mpv-gotorrent companion binary (one-time setup)…");
    }

    let downloadSuccess = false;
    if (iina.http && typeof iina.http.download === "function") {
      try {
        await iina.http.download(downloadUrl, downloadDest);
        downloadSuccess = true;
      } catch (err) {
        // Fall back to curl below
      }
    }

    if (!downloadSuccess) {
      await utils.exec("curl", ["-fsSL", downloadUrl, "-o", downloadDest]);
    }

    await utils.exec("chmod", ["+x", downloadDest]);
    try {
      await utils.exec("xattr", ["-d", "com.apple.quarantine", downloadDest]);
    } catch (e) {
      // Ignore if quarantine attribute not present
    }

    if (utils.fileInPath(downloadDest)) {
      if (iina.hud && typeof iina.hud.show === "function") {
        iina.hud.show("mpv-gotorrent companion ready!");
      }
      return downloadDest;
    }
  } catch (err) {
    // Log error
  }

  return "mpv-gotorrent";
}

// Create Global Menu Items under macOS "Plugins" menu
function registerMenu() {
  const openItem = menu.item("Open Magnet Link / Torrent…", async () => {
    const input = utils.prompt("Enter magnet link, .torrent URL, or 40-char infohash:");
    if (input && input.trim()) {
      core.open(input.trim());
    }
  });

  menu.addItem(openItem);
}

// Register global menu on launch
registerMenu();

// Graceful cleanup on exit
async function shutdownActiveCompanion() {
  if (activeCompanion && activeCompanion.baseUrl) {
    try {
      const headers = {};
      if (activeCompanion.token) {
        headers["X-Gotorrent-Token"] = activeCompanion.token;
      }
      await fetch(`${activeCompanion.baseUrl}/shutdown`, { method: "POST", headers });
    } catch (e) {
      // Ignored
    }
    activeCompanion = null;
  }
}

// Exported helpers for main.js instances
globalThis.__gotorrent = {
  locateBinary,
  shutdownActiveCompanion,
  setActiveCompanion: (c) => { activeCompanion = c; },
  getActiveCompanion: () => activeCompanion
};

