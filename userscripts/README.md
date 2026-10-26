# Browser Userscripts (1-Click Streaming)

Enhance your browsing experience on **Nyaa.si** and **1337x** by streaming torrents directly into **mpv** with a single click.

---

## 🚀 Available Scripts

| Script | Supported Sites | Features |
| :--- | :--- | :--- |
| **[Nyaa.si Stream in MPV](nyaa-stream.user.js)** | `nyaa.si` | Adds a ▶ play button to search/browse tables and a "Stream in MPV" button on torrent pages. |
| **[1337x Stream in MPV](1337x-stream.user.js)** | `1337x.to`, `1337x.st`, `1337x.ws`, `1337x.eu`, `1337x.se`, etc. | Adds a direct ▶ play button to search/popular listings (instant fetch) and torrent detail pages. |

---

## 📦 How to Install

1. Install a userscript manager in your browser if you haven't already:
   - **[Violentmonkey](https://violentmonkey.github.io/)** (Recommended: open-source & fast)
   - **[Tampermonkey](https://www.tampermonkey.net/)**

2. Click on the raw script links to install with 1 click:
   - **[Install Nyaa.si Stream Script](https://raw.githubusercontent.com/biohazard786/mpv-gotorrent/main/userscripts/nyaa-stream.user.js)**
   - **[Install 1337x Stream Script](https://raw.githubusercontent.com/biohazard786/mpv-gotorrent/main/userscripts/1337x-stream.user.js)**

---

## ⚡ How It Works

When you click the stream button, the userscript triggers the `mpv://` URL scheme:

```
mpv://magnet:?xt=urn:btih:...
```

1. Your browser delegates the `mpv://` link to the OS.
2. The OS launches `mpv`.
3. `mpv-gotorrent` hooks into the stream, discovers peers, and begins playback immediately!

### URL Scheme Handler Setup

* **Automatic**: Re-run `install.sh` (Linux/macOS) or `install.ps1` (Windows) — it automatically registers the `mpv://` protocol handler with your desktop environment.
* **Third-Party Tools**: If you already use **[mpv-handler](https://github.com/akiirui/mpv-handler)**, it is 100% compatible out of the box.
