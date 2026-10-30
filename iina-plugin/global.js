/**
 * mpv-gotorrent - IINA Global Entry Script
 * Author: biohazard786
 * License: MIT
 *
 * The global entry has no `core`/`mpv`, so it only registers the menu and opens
 * player instances. Torrent handling lives in main.js, which runs per player.
 */

const { menu, utils, global, console: log } = iina;

menu.addItem(
  menu.item("Open Magnet Link / Torrent…", () => {
    const input = utils.prompt("Enter magnet link, .torrent URL, or 40-char infohash:");
    if (!input || !input.trim()) return;

    const opened = global.createPlayerInstance({ url: input.trim(), enablePlugins: true });
    if (opened === false) {
      log.error(`[mpv-gotorrent] IINA refused to open: ${input.trim()}`);
    }
  })
);
