# mpv-gotorrent

Stream BitTorrent files and magnet links directly inside **mpv** with **zero setup** and **zero external dependencies**. No Node.js, Python, Docker, or TorrServer background service required.

---

## ✨ Features

- ⚡ **Zero Setup & Self-Contained**: Single static companion binary + Lua script. Starts when mpv opens and closes automatically when you exit.
- 💾 **RAM-Only Ring Buffer (Zero SSD Wear)**: Stream 40GB+ remuxes completely in RAM with zero disk writes using an intelligent LRU piece cache and header/tail cue pinning. Protects SSD write endurance (TBW).
- 📥 **"Keep File" Hotkey (<kbd>Ctrl</kbd> + <kbd>k</kbd>)**: Save what you're watching permanently on the fly without restarting. Downloads the full file into your library (`~/Videos`) and finishes in the background if mpv closes. Available in disk mode (RAM mode never writes to disk).
- 🚀 **Instant Playback & DHT Persistence**: Automatically persists DHT routing nodes across sessions (`dht.nodes`), reducing swarm discovery and metadata resolution from 15 seconds to under 2 seconds.
- 🌐 **Built-in Public Tracker Boost**: Injects top high-uptime public UDP/IP trackers (auto-cached from `ngosang/trackerslist`) into every magnet to revive dead or slow torrents without DNS bottlenecks.
- 🔄 **Full Resume & History**: Remembers your playback position (`watch_later`) and seamlessly integrates with mpv history scripts (`SimpleHistory`, `uosc`, `memo`).
- 📑 **Smart Playlists**: Multi-file torrents (like TV shows and anime) are automatically ingested as clean, sorted episode playlists.
- 💬 **Automatic Subtitles**: Automatically detects and injects subtitle files (`.srt`, `.vtt`, `.ass`) included in the torrent.
- 📊 **Live Stats OSD**: Press <kbd>p</kbd> at any time to check real-time download/upload speed, peer count, RAM/disk storage mode, and keep buffer progress.
- 🧹 **Auto-Cleanup**: Temporary torrent files are automatically cleaned up on exit (or kept if you prefer).

---

## 🚀 Quick Install

Install or update with a single command in your terminal:

### Linux & macOS

```bash
curl -fsSL https://raw.githubusercontent.com/biohazard786/mpv-gotorrent/main/install.sh | bash
```

_(or via `wget`: `wget -qO- https://raw.githubusercontent.com/biohazard786/mpv-gotorrent/main/install.sh | bash`)_

### Windows (PowerShell)

```powershell
irm https://raw.githubusercontent.com/biohazard786/mpv-gotorrent/main/install.ps1 | iex
```

> **Tip:** Re-running the command anytime will automatically check for and install updates.

---

## 📦 Other Installation Methods

<details>
<summary><b>Manual Installation</b></summary>

1. Download the latest release archive (`.tar.gz` or `.zip`) for your operating system from the **[Releases](https://github.com/biohazard786/mpv-gotorrent/releases)** page.
2. Extract the archive into your mpv scripts directory:
   - **Linux / macOS**: `~/.config/mpv/scripts/mpv-gotorrent/`
   - **Windows**: `%APPDATA%\mpv\scripts\mpv-gotorrent\`
3. Verify that `main.lua` and `mpv-gotorrent` (or `mpv-gotorrent.exe`) are inside that folder.
</details>

<details>
<summary><b>Build from Source</b></summary>

Requires **Go 1.26+** and **make** (or PowerShell on Windows):

**Linux / macOS:**

```bash
git clone https://github.com/biohazard786/mpv-gotorrent.git
cd mpv-gotorrent
make install
```

**Windows:**

```powershell
git clone https://github.com/biohazard786/mpv-gotorrent.git
cd mpv-gotorrent
.\install.ps1 -Local
```

</details>

---

## 🎬 How to Use

Simply pass any magnet link, `.torrent` file, torrent URL, or 40-character infohash to mpv:

```bash
# Magnet link
mpv "magnet:?xt=urn:btih:..."

# Local .torrent file
mpv path/to/video.torrent

# Remote torrent URL
mpv "https://example.com/movie.torrent"
```

> **Desktop Tip:** You can also simply **drag & drop** any `.torrent` file or magnet link directly into an open mpv window!

> 🌐 **1-Click Browser Streaming:** Stream directly from **Nyaa.si** and **1337x** without copying links! Check out our [Browser Userscripts](userscripts/README.md).

### Controls

|                Key                | Action                                                                                               |
| :-------------------------------: | :--------------------------------------------------------------------------------------------------- |
|           <kbd>p</kbd>            | Toggle live torrent stats overlay (Speed, Peers, Seeds, Buffer, RAM/Disk, Keep status)               |
|  <kbd>Ctrl</kbd> + <kbd>k</kbd>   | **Keep File**: Save current video permanently to `~/Videos` (or background download). Disk mode only |
| <kbd>&lt;</kbd> / <kbd>&gt;</kbd> | Previous / Next episode in multi-file torrents                                                       |

To use other keys, set `key_stats` / `key_keep` in `gotorrent.conf` (see Configuration below), or add e.g. `P script-binding gotorrent-stats` or `K script-binding gotorrent-keep` to your `input.conf`.

---

## ⚙️ Configuration (Optional)

You can customize settings by creating `gotorrent.conf` in your mpv `script-opts` folder:

- **Linux / macOS**: `~/.config/mpv/script-opts/gotorrent.conf`
- **Windows**: `%APPDATA%\mpv\script-opts\gotorrent.conf`

```ini
# Max RAM in MB for zero-wear memory ring buffer (e.g. 512, 1024). 0 = use disk cache
# Ctrl+k (Keep) is unavailable in RAM mode
max_ram_mb=0

# Save location when pressing Ctrl+k (blank = ~/Videos or ~/Downloads)
keep_dir=

# Scope for Ctrl+k: 'current' (active video only) or 'all' (entire torrent)
keep_scope=current

# Finish download in background when mpv closes after pressing Ctrl+k (default: true)
detach_on_keep=true

# Keep downloaded files after quitting mpv (default: false = auto-delete disk cache)
# Kept files go to cache_dir, or ~/.cache/mpv-gotorrent/downloads if it is blank
keep_files=false

# Custom download/cache directory (blank = ~/.cache/mpv-gotorrent/sessions)
# Each session uses its own subfolder, which is deleted on exit unless keep_files=true
cache_dir=

# Seconds to wait for torrent metadata before timing out (default: 60)
timeout=60

# Automatically queue multi-file torrents as a playlist (default: true)
auto_playlist=true

# Start downloading the next episode's opening at this % of the current one (default: 80, 0 = off)
prefetch_next_at=80

# Show on-screen messages when connecting and resolving metadata (default: true)
show_osd=true

# Preferred streaming HTTP port (default: 8989)
port=8989

# Keys (mpv key names, e.g. p, ctrl+k, alt+s). Blank = no default key
key_stats=p
key_keep=ctrl+k
```

---

## 📄 License

MIT License. See [LICENSE](LICENSE) for details.
