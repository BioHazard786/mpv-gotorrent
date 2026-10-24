# mpv-gotorrent

A high-performance, cross-platform **mpv** plugin and standalone Go companion that streams BitTorrent files and magnet links directly into mpv with **zero external dependencies** (no Node.js, Python, or TorrServer background service required).

---

## Features

- **Zero Runtime Dependencies**: Single compiled static Go companion binary + Lua hook script.
- **Ultra-Fast Startup**: Connects to the swarm and begins streaming in milliseconds.
- **Auto-Playlist by Default**: Multi-file torrents (season packs, multi-part series) are automatically discovered, naturally sorted (`S01E01`, `S01E02`...), and queued sequentially into mpv's playlist.
- **Smart Container Readahead**: Automatically prioritizes video headers (`moov` atoms in MP4 / EBML in MKV) and maintains a 32 MB sliding window ahead of playback for seamless seeking.
- **Automatic Subtitle Injection**: Detects and attaches `.srt`, `.vtt`, `.ass`, and `.sub` files within the torrent swarm to the active stream using `sub-add`.
- **Bulletproof Lifecycle & Zombie Protection**:
  - **Linux**: `PR_SET_PDEATHSIG` automatic kernel signal on parent death.
  - **macOS**: `kqueue` `NOTE_EXIT` monitoring on parent PID.
  - **Windows**: Win32 `OpenProcess(SYNCHRONIZE)` and `WaitForSingleObject`.
  - **Universal**: Inherited stdin pipe EOF detection that immediately cleans up if mpv crashes or receives `SIGKILL`.
- **Beautiful Colored Logs**: Uses standard Go `log/slog` styled with [`tint`](https://github.com/lmittmann/tint).
- **Interactive OSD Stats**: Press <kbd>T</kbd> inside mpv to view real-time download/upload speed, peer & seed counts, buffer completion, and progress.

---

## Directory Structure

Like modern mpv plugins (e.g. `videoclip`), both the script entrypoint and the binary live in an isolated directory:

```
~/.config/mpv/scripts/mpv-gotorrent/
├── main.lua
└── mpv-gotorrent         (or mpv-gotorrent.exe on Windows)
```

The script uses `mp.get_script_directory()` to find the companion binary right next to `main.lua`.

---

## Installation

### 1. Build & Install via Makefile (Linux / macOS)

Clone this repository and run:

```bash
make install
```

This compiles `bin/mpv-gotorrent` and installs both `main.lua` and `mpv-gotorrent` into `~/.config/mpv/scripts/mpv-gotorrent/`.

### 2. Manual Installation

1. Copy `scripts/mpv-gotorrent/main.lua` to:
   - **Linux / macOS**: `~/.config/mpv/scripts/mpv-gotorrent/main.lua`
   - **Windows**: `%APPDATA%\mpv\scripts\mpv-gotorrent\main.lua`
2. Copy the compiled binary `mpv-gotorrent` (or `mpv-gotorrent.exe`) into the exact same folder.

---

## Usage

Simply pass any magnet link, `.torrent` path, HTTP torrent URL, or 40-character infohash directly to `mpv`:

```bash
# Magnet Link
mpv "magnet:?xt=urn:btih:..."

# Local .torrent file
mpv path/to/movie.torrent

# Remote .torrent URL
mpv "https://example.com/file.torrent"

# 40-character Infohash
mpv 0123456789abcdef0123456789abcdef01234567
```

### Controls

- <kbd>T</kbd>: Toggle on-screen display (OSD) torrent statistics (Download speed, Upload speed, Active Peers, Seeds, Buffer Progress).
- <kbd>&lt;</kbd> / <kbd>&gt;</kbd>: Previous / Next episode in multi-file torrent playlists.

---

## Configuration

You can customize behavior by creating `~/.config/mpv/script-opts/gotorrent.conf`:

```ini
# Custom binary location (optional, automatically found by default)
binary_path=

# Custom cache directory (blank uses OS temp directory)
cache_dir=

# Keep downloaded files on exit (default false: auto-cleanup cache)
keep_files=false

# Seconds to wait for torrent metadata before timing out
timeout=60

# Automatically queue multi-file torrents sequentially in playlist (default: true)
auto_playlist=true

# Show OSD messages on loading and playback events
show_osd=true
```

---

## Building from Source

### Requirements
- Go 1.21+
- `make`

```bash
# Build native binary
make build

# Run unit tests
make test

# Cross-compile for Windows (amd64), Linux (amd64/arm64), and macOS (Intel & Apple Silicon)
make cross
```

---

## License

MIT License. See [LICENSE](LICENSE) for details.
