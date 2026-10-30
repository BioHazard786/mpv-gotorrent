# mpv-gotorrent for IINA

Stream BitTorrent magnet links and `.torrent` files directly inside **IINA** (the modern macOS media player based on `libmpv`) with **zero setup** and **zero SSD wear**.

---

## ✨ Features

- ⚡ **Zero SSD Wear (Apple Silicon Ready)**: Stream 40GB+ 4K remuxes entirely in RAM with zero disk writes using an intelligent ring buffer. Protects soldered, non-replaceable Mac SSDs from premature write endurance (TBW) wear.
- 🍏 **Native macOS Sidebar**: A real-time IINA sidebar tab showing download/upload speed gauges, active peers, pieces completed, and RAM usage.
- 📥 **"Keep File" Button**: Save what you are watching permanently to `~/Videos` with a single click.
- 🚀 **Instant Playback**: Persists DHT routing nodes across sessions (`dht.nodes`), reducing swarm discovery to under 2 seconds.
- 🌐 **1-Click Browser Streaming**: Stream directly from Nyaa.si and 1337x via native `iina://` URL schemes.
- 📑 **Smart Playlists**: TV series and multi-file anime torrents are automatically ingested as clean episode playlists.
- 💬 **Automatic Subtitles**: Automatically detects and loads subtitle files bundled inside the torrent.

---

## 📦 Installation

### Method 1: Install from GitHub (IINA 1.3.0+)

1. Open **IINA**.
2. Open Settings (`⌘ ,`) → **Plugins**.
3. Click **"Install from GitHub…"** on the right.
4. Enter:
   ```text
   biohazard786/mpv-gotorrent
   ```
5. Click **Install**. Done!

---

### Method 2: Download `.iinaplgz` (1-Click Package)

1. Download the latest **`mpv-gotorrent.iinaplgz`** package from the **[Releases](https://github.com/biohazard786/mpv-gotorrent/releases)** page.
2. Double-click the `.iinaplgz` file, or drag & drop it directly onto any open IINA window.
3. Click **Install** when prompted by IINA.

---

### Method 3: Manual Installation (Developer)

Clone or copy the plugin into IINA's plugin folder:

```bash
mkdir -p "$HOME/Library/Application Support/com.colliderli.iina/plugins"
cp -r iina-plugin "$HOME/Library/Application Support/com.colliderli.iina/plugins/io.github.biohazard786.gotorrent.iinaplugin"
```

---

## 🎬 How to Use in IINA

1. **Drag & Drop**: Simply drag any `.torrent` file or magnet link directly into the IINA window!
2. **Menu Bar**: Go to macOS menu bar **Plugins → mpv-gotorrent → Open Magnet Link / Torrent…**.
3. **Browser**: Click the stream button on Nyaa or 1337x using our [Browser Userscripts](../userscripts/README.md).
4. **Sidebar**: Open IINA's sidebar (`⌘ ⇧ S`) and click the **BitTorrent** tab to monitor live swarm speeds, peer connections, and buffer status.

