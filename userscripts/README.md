# Browser Userscripts (1-Click Streaming)

Enhance your browsing experience on **Nyaa.si** and **1337x** by streaming torrents directly into **mpv** or **IINA** (macOS) with a single click.

---

## 🚀 Available Scripts

| Script | Supported Sites | Features |
| :--- | :--- | :--- |
| **[Nyaa.si Stream in MPV / IINA](nyaa-stream.user.js)** | `nyaa.si` | Adds a ▶ play button to search/browse tables and a "Stream in MPV/IINA" button on torrent pages. |
| **[1337x Stream in MPV / IINA](1337x-stream.user.js)** | `1337x.to`, `1337x.st`, `1337x.ws`, `1337x.eu`, `1337x.se`, etc. | Adds a direct ▶ play button to search/popular listings (instant fetch) and torrent detail pages. |

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

When you click the stream button, the userscript triggers the appropriate desktop URL scheme:

* **MPV (Default)**:
  ```
  mpv://magnet:?xt=urn:btih:...
  ```
* **IINA (macOS)**:
  ```
  iina://weblink?url=magnet%3A%3Fxt%3Durn%3Abtih%3A...
  ```

### 🍏 Using IINA on macOS?

* **Instant toggle (Alt / ⌥ Click)**: Hold <kbd>Alt</kbd> (or <kbd>Option ⌥</kbd> on Mac) while clicking any stream button to open in the alternate player on the fly (e.g. opens in IINA instead of MPV).
* **Set IINA as Default**: Run this once in your browser console on Nyaa or 1337x:
  ```javascript
  localStorage.setItem('gotorrent_player', 'iina');
  ```
  *(To switch back to MPV: `localStorage.setItem('gotorrent_player', 'mpv');`)*

### URL Scheme Handler Setup

* **Linux / BSD**: Run `install.sh` — it automatically registers the `mpv://` protocol handler with your desktop environment (`xdg-mime`).
* **Windows**: Run `install.ps1` — it automatically registers `mpv://` in Windows Registry.
* **macOS (IINA)**: IINA automatically registers `iina://` during app installation. No setup required!
* **Third-Party Tools**: If you already use **[mpv-handler](https://github.com/akiirui/mpv-handler)**, it is 100% compatible out of the box.
