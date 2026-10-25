# mpv-gotorrent

Stream BitTorrent files and magnet links directly inside **mpv** with **zero setup** and **zero external dependencies**. No Node.js, Python, Docker, or TorrServer background service required.

---

## ✨ Features

- ⚡ **Zero Setup & Self-Contained**: Single static companion binary + Lua script. Starts when mpv opens and closes automatically when you exit.
- 🚀 **Instant Playback**: Connects to the BitTorrent swarm in milliseconds with smart readahead buffering for smooth seeking.
- 🔄 **Full Resume & History**: Remembers your playback position (`watch_later`) and seamlessly integrates with mpv history scripts (`SimpleHistory`, `uosc`, `memo`).
- 📑 **Smart Playlists**: Multi-file torrents (like TV shows and anime) are automatically ingested as clean, sorted episode playlists.
- 💬 **Automatic Subtitles**: Automatically detects and injects subtitle files (`.srt`, `.vtt`, `.ass`) included in the torrent.
- 📊 **Live Stats OSD**: Press <kbd>p</kbd> at any time to check real-time download/upload speed, peer count, and buffer progress.
- 🧹 **Auto-Cleanup**: Temporary torrent files are automatically cleaned up on exit (or kept if you prefer).

---

## 🚀 Quick Install

Install or update with a single command in your terminal:

### Linux & macOS
```bash
curl -fsSL https://raw.githubusercontent.com/biohazard786/mpv-gotorrent/main/install.sh | bash
```
*(or via `wget`: `wget -qO- https://raw.githubusercontent.com/biohazard786/mpv-gotorrent/main/install.sh | bash`)*

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

Requires **Go 1.21+** and **make** (or PowerShell on Windows):

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

### Controls

| Key | Action |
|:---:|:---|
| <kbd>p</kbd> | Toggle live torrent stats overlay (Speed, Peers, Seeds, Buffer) |
| <kbd>&lt;</kbd> / <kbd>&gt;</kbd> | Previous / Next episode in multi-file torrents |

---

## ⚙️ Configuration (Optional)

You can customize settings by creating `gotorrent.conf` in your mpv `script-opts` folder:
- **Linux / macOS**: `~/.config/mpv/script-opts/gotorrent.conf`
- **Windows**: `%APPDATA%\mpv\script-opts\gotorrent.conf`

```ini
# Keep downloaded files after quitting mpv (default: false = auto-delete cache)
keep_files=false

# Custom download/cache directory (blank = system temporary directory)
cache_dir=

# Seconds to wait for torrent metadata before timing out (default: 60)
timeout=60

# Automatically queue multi-file torrents as a playlist (default: true)
auto_playlist=true

# Show on-screen messages when connecting and resolving metadata (default: true)
show_osd=true

# Preferred streaming HTTP port (default: 8989)
port=8989
```

---

## 📄 License

MIT License. See [LICENSE](LICENSE) for details.
