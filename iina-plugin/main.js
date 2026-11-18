/**
 * mpv-gotorrent - IINA Player Instance Script
 * Author: biohazard786
 * License: MIT
 */

const { core, mpv, event, utils, file, http, sidebar, preferences, console: log } = iina;

const PLUGIN_ID = "io.github.biohazard786.gotorrent";
const PLUGIN_DIRS = [
  `~/Library/Application Support/com.colliderli.iina/plugins/${PLUGIN_ID}.iinaplugin`,
  `~/Library/Application Support/com.colliderli.iina/plugins/${PLUGIN_ID}.iinaplugin-dev`,
];

let currentServer = null;   // { port, baseUrl, token, infohash, startedAt, peersSeen }
let activeTorrent = null;   // { name, infohash, videos, totalSize, ... }
let statsTimer = null;
let pendingRestore = null;  // { idx, videos } after resuming one episode of a pack from history

const LOOPBACK_BASE = /^https?:\/\/(?:127\.0\.0\.1|localhost):\d+/;
const NO_PEERS_WARN_MS = 30000;
const PREFETCH_NEXT_AT = 80; // % of the current file at which the next entry is warmed up

// Companions started ahead of time for torrents queued next in the playlist, by source.
// Each is { promise, cancelled }; on_load takes it out of the map when its entry plays.
const preloaded = new Map();

// Split a companion stream URL (http://127.0.0.1:<port>/torrent/<infohash>/<file index>/<name>)
function streamLocation(url) {
  const m = typeof url === "string" && url.match(/^https?:\/\/(?:127\.0\.0\.1|localhost):\d+\/torrent\/([0-9a-fA-F]+)\/(\d+)\//);
  return m ? { hash: m[1].toLowerCase(), idx: parseInt(m[2], 10) } : null;
}

// Torrent file index of the playing stream, if it belongs to the active companion
function currentStreamIndex() {
  const loc = streamLocation(mpv.getString("path"));
  if (!loc || !currentServer || loc.hash !== currentServer.infohash) return null;
  return loc.idx;
}

// Helper: percent-decode URL
function urlDecode(str) {
  try {
    return decodeURIComponent(str);
  } catch (e) {
    return str || "";
  }
}

// Helper: extract clean torrent source
function extractTorrentSource(url) {
  if (!url || typeof url !== "string") return null;

  // Ignore internal loopback streams
  if (url.startsWith("http://127.0.0.1:") || url.startsWith("http://localhost:")) {
    return null;
  }

  const cleanUrl = url.includes("%") ? urlDecode(url) : url;

  // 1. Magnet Link
  const magnetMatch = cleanUrl.match(/(magnet:[?\/].+)/);
  if (magnetMatch) {
    const qIndex = magnetMatch[1].indexOf("?");
    if (qIndex !== -1) {
      return "magnet:" + magnetMatch[1].substring(qIndex);
    }
  }

  // Handle explicit iina://weblink?url= scheme
  const iinaMatch = cleanUrl.match(/^iina:\/\/weblink\?url=(.+)/i);
  if (iinaMatch) {
    return extractTorrentSource(urlDecode(iinaMatch[1]));
  }

  // 2. HTTP(S) .torrent URL
  if (cleanUrl.match(/https?:\/\/.*\.torrent(\?.*)?$/i)) {
    return cleanUrl;
  }

  // 3. Local .torrent file
  if (cleanUrl.match(/\.torrent(\?.*)?$/i)) {
    return cleanUrl;
  }

  // 4. 40-character hex infohash or 32-character base32
  const name = cleanUrl.split(/[/\\]/).pop();
  if (name && (name.match(/^[0-9a-fA-F]{40}$/) || name.match(/^[2-7a-zA-Z]{32}$/))) {
    return "magnet:?xt=urn:btih:" + name;
  }

  return null;
}

// Read options from IINA Preferences with fallbacks
function getPluginOpts() {
  return {
    port: preferences.get("port") || 8989,
    maxRamMb: preferences.get("max_ram_mb") || 0,
    keepDir: preferences.get("keep_dir") || "",
    keepScope: preferences.get("keep_scope") || "current",
    timeout: preferences.get("timeout") || 60,
    autoPlaylist: preferences.get("auto_playlist") !== false,
    // A cleared field must mean the default, not 0 (which turns deletion off)
    maxCachedFiles: Number.isNaN(parseInt(preferences.get("max_cached_files"), 10)) ? 3 : parseInt(preferences.get("max_cached_files"), 10),
    maxUploadKbps: Number(preferences.get("max_upload_kbps")) || 0,
    maxDownloadKbps: Number(preferences.get("max_download_kbps")) || 0,
    seedRatio: Number(preferences.get("seed_ratio")) || 0,
  };
}

// Quote a string for use inside a POSIX shell command
function shQuote(s) {
  return "'" + String(s).replace(/'/g, "'\\''") + "'";
}

async function detectArch() {
  try {
    const res = await utils.exec("/usr/bin/uname", ["-m"]);
    return res.stdout.trim() === "arm64" ? "arm64" : "amd64";
  } catch (e) {
    return "arm64";
  }
}

// Locate the companion binary: bundled in the plugin, previously downloaded, or fetched from GitHub
async function locateBinary() {
  const arch = await detectArch();
  const binName = `mpv-gotorrent-darwin-${arch}`;

  const candidates = [
    ...PLUGIN_DIRS.map((dir) => `${dir}/bin/${binName}`),
    `@data/bin/${binName}`,
    "/opt/homebrew/bin/mpv-gotorrent",
    "/usr/local/bin/mpv-gotorrent",
  ];
  for (const c of candidates) {
    if (file.exists(c)) return utils.resolvePath(c);
  }

  const dest = utils.resolvePath(`@data/bin/${binName}`);
  await utils.exec("/bin/mkdir", ["-p", utils.resolvePath("@data/bin")]);
  core.osd("Downloading mpv-gotorrent companion (one-time setup)…");
  await http.download(`https://github.com/biohazard786/mpv-gotorrent/releases/latest/download/${binName}`, "@data/bin/" + binName);
  await utils.exec("/bin/chmod", ["+x", dest]);
  await utils.exec("/usr/bin/xattr", ["-d", "com.apple.quarantine", dest]).catch(() => {});
  return dest;
}

// IINA exposes no PID API; a child shell's parent is the IINA process
async function iinaPid() {
  try {
    const res = await utils.exec("/bin/sh", ["-c", "echo $PPID"]);
    return parseInt(res.stdout.trim(), 10) || 0;
  } catch (e) {
    return 0;
  }
}

// POST to a companion ({ baseUrl, token }; the active one by default) with its session token
function companionPost(endpoint, server = currentServer) {
  if (!server || !server.baseUrl) {
    return Promise.reject(new Error("No active companion"));
  }
  const headers = server.token ? { "X-Gotorrent-Token": server.token } : {};
  return http.post(`${server.baseUrl}${endpoint}`, { headers });
}

// The companion address in a ready banner, in the shape companionPost takes
function bannerServer(data) {
  return { baseUrl: data.base_url, token: data.token };
}

// Make a ready companion (the banner it wrote) the one the rest of the plugin talks to
function adoptCompanion(parsed) {
  currentServer = {
    port: parsed.port,
    baseUrl: parsed.base_url,
    token: parsed.token,
    infohash: parsed.infohash?.toLowerCase(),
    startedAt: Date.now(),
    peersSeen: false,
  };
  activeTorrent = {
    name: parsed.name,
    infohash: currentServer.infohash,
    totalSize: parsed.total_size,
    videos: parsed.videos || [],
  };
  if (parsed.warning) {
    log.warn(`[mpv-gotorrent] ${parsed.warning}`);
    core.osd(parsed.warning);
  }
}

function stopCompanion() {
  if (statsTimer) {
    clearInterval(statsTimer);
    statsTimer = null;
  }
  if (currentServer) {
    companionPost("/shutdown").catch(() => {});
    currentServer = null;
    activeTorrent = null;
  }
}

// Start companion process and poll state file asynchronously. Resolves with its ready banner.
// quiet: no on-screen progress (background preloads).
async function launchCompanion(source, { quiet = false } = {}) {
  const binaryPath = await locateBinary();
  const opts = getPluginOpts();
  const pid = await iinaPid();
  const stamp = `${pid}_${Date.now()}`;
  const stateRef = `@tmp/gotorrent_${stamp}.json`;
  const stateFilePath = utils.resolvePath(stateRef);
  const logFilePath = utils.resolvePath(`@tmp/gotorrent_${stamp}.log`);

  const args = [
    `--source=${source}`,
    `--parent-pid=${pid}`,
    `--port=${opts.port}`,
    `--timeout=${opts.timeout}`,
    `--state-file=${stateFilePath}`,
    `--json-output=false`,
  ];
  if (opts.maxRamMb > 0) args.push(`--max-ram-mb=${opts.maxRamMb}`);
  if (opts.keepDir) args.push(`--keep-dir=${opts.keepDir}`);
  if (opts.keepScope) args.push(`--keep-scope=${opts.keepScope}`);
  // Only passed when changed, so an older companion binary without these flags still starts
  if (opts.maxCachedFiles !== 3) args.push(`--max-cached-files=${opts.maxCachedFiles}`);
  if (opts.maxUploadKbps > 0) args.push(`--max-upload-kbps=${Math.floor(opts.maxUploadKbps)}`);
  if (opts.maxDownloadKbps > 0) args.push(`--max-download-kbps=${Math.floor(opts.maxDownloadKbps)}`);
  if (opts.seedRatio > 0) args.push(`--seed-ratio=${opts.seedRatio}`);

  log.log(`[mpv-gotorrent] Spawning companion: ${binaryPath} (source: ${source})`);

  // utils.exec waits for the process to exit, so background it from the shell to return immediately
  const cmd = `nohup ${[binaryPath, ...args].map(shQuote).join(" ")} > ${shQuote(logFilePath)} 2>&1 &`;
  await utils.exec("/bin/sh", ["-c", cmd]);

  const startTime = Date.now();
  const timeoutMs = (opts.timeout || 60) * 1000;
  let lastConnectingText = "";

  return new Promise((resolve, reject) => {
    const poller = setInterval(() => {
      try {
        if (file.exists(stateRef)) {
          const content = file.read(stateRef);
          if (content && content.length > 5) {
            const parsed = JSON.parse(content);
            if (parsed.status === "ready") {
              clearInterval(poller);
              resolve(parsed);
              return;
            } else if (parsed.status === "connecting") {
              // Swarm size while metadata is fetched; 0 peers for long means a dead torrent
              const text = `Connecting to swarm… ${parsed.peers || 0} peers (${parsed.seeders || 0} seeders)`;
              if (!quiet && text !== lastConnectingText) core.osd(text);
              lastConnectingText = text;
            } else if (parsed.status === "error") {
              clearInterval(poller);
              reject(new Error(parsed.error || "Unknown companion error"));
              return;
            }
          }
        }
      } catch (err) {
        // File may be partially written, retry next tick
      }

      if (Date.now() - startTime > timeoutMs) {
        clearInterval(poller);
        reject(new Error("Timed out waiting for torrent swarm metadata."));
      }
    }, 200);
  });
}

// Intercept file/stream opening via on_load hook
mpv.addHook("on_load", 10, async (next) => {
  const rawUrl = mpv.getString("stream-open-filename");

  // A companion stream URL from history or a restored playlist: the companion that served it is
  // gone (or on another port), so reconnect by infohash and point the URL at the live one.
  const loc = streamLocation(rawUrl);
  if (loc) {
    if (currentServer && currentServer.infohash === loc.hash) {
      mpv.set("stream-open-filename", rawUrl.replace(LOOPBACK_BASE, currentServer.baseUrl));
      next();
      return;
    }
    log.log(`[mpv-gotorrent] Resuming torrent stream for infohash: ${loc.hash}`);
    core.osd("Reconnecting torrent stream…");
    try {
      stopCompanion();
      const data = await launchCompanion(loc.hash);
      adoptCompanion(data);
      mpv.set("stream-open-filename", rawUrl.replace(LOOPBACK_BASE, data.base_url));
      // History hands us one episode; put the rest of the pack around it once it loads
      if (getPluginOpts().autoPlaylist && data.videos && data.videos.length > 1) {
        pendingRestore = { idx: loc.idx, videos: data.videos };
      }
      startStatsPoller();
    } catch (err) {
      log.error(`[mpv-gotorrent] ${err.message}`);
      core.osd(`mpv-gotorrent error: ${err.message}`);
    }
    next();
    return;
  }

  const source = extractTorrentSource(rawUrl);

  if (!source) {
    next();
    return;
  }

  log.log(`[mpv-gotorrent] Intercepted torrent source: ${source}`);
  core.osd("Connecting to BitTorrent swarm…");

  try {
    stopCompanion();
    // Reuse the companion preloaded for this entry, or start one
    const pre = preloaded.get(source);
    preloaded.delete(source);
    const data = await (pre ? pre.promise : launchCompanion(source));
    adoptCompanion(data);

    if (data.m3u_playlist && getPluginOpts().autoPlaylist) {
      mpv.set("stream-open-filename", "memory://" + data.m3u_playlist);
      if (data.videos && data.videos.length > 1 && !data.warning) {
        core.osd(`Loaded ${data.videos.length} episodes into playlist`);
      }
    } else if (data.first_stream_url) {
      mpv.set("stream-open-filename", data.first_stream_url);
    }

    startStatsPoller();
  } catch (err) {
    log.error(`[mpv-gotorrent] ${err.message}`);
    core.osd(`mpv-gotorrent error: ${err.message}`);
  }

  next();
});

// Auto-inject clean title and subtitles on playback load.
// IINA's docs and source disagree on the event name, so listen to both and dedupe.
let lastLoad = { path: null, at: 0 };

// Rebuild a resumed episode's pack around it. videos is in playlist order, which can differ from
// torrent file order. append + playlist-move works on every mpv; loadfile insert-at needs 0.38+.
function restorePlaylist(resumedIdx, videos) {
  let before = 0;
  let seen = false;
  for (const v of videos) {
    if (v.index === resumedIdx) {
      seen = true;
      continue;
    }
    mpv.command("loadfile", [v.stream_url, "append"]);
    if (!seen) {
      mpv.command("playlist-move", [String(mpv.getNumber("playlist-count") - 1), String(before)]);
      before++;
    }
  }
}

function onFileLoaded() {
  if (!activeTorrent || !activeTorrent.videos || activeTorrent.videos.length === 0) return;

  const currentPath = mpv.getString("path") || "";
  if (currentPath === lastLoad.path && Date.now() - lastLoad.at < 500) return;
  lastLoad = { path: currentPath, at: Date.now() };

  if (pendingRestore) {
    const { idx, videos } = pendingRestore;
    pendingRestore = null;
    // Skip if the resumed episode failed to load and this is some later file
    if (currentStreamIndex() === idx && mpv.getNumber("playlist-count") === 1) {
      restorePlaylist(idx, videos);
    }
  }

  let matchedVideo = null;
  for (const v of activeTorrent.videos) {
    if (currentPath.includes(v.stream_url) || (v.name && currentPath.includes(encodeURIComponent(v.name)))) {
      matchedVideo = v;
      break;
    }
  }

  if (!matchedVideo) {
    const pos = mpv.getNumber("playlist-pos") || 0;
    matchedVideo = activeTorrent.videos[pos] || activeTorrent.videos[0];
  }

  if (matchedVideo) {
    mpv.set("file-local-options/force-media-title", matchedVideo.name.replace(/\.[^/.]+$/, ""));

    if (matchedVideo.subtitles && Array.isArray(matchedVideo.subtitles)) {
      for (const sub of matchedVideo.subtitles) {
        mpv.command("sub-add", [sub.download_url, "auto", sub.name]);
      }
    }
  }
}
event.on("iina.file-loaded", onFileLoaded);
event.on("mpv.file-loaded", onFileLoaded);

// Report duration to companion for dynamic buffer calculation
event.on("mpv.duration.changed", () => {
  const duration = mpv.getNumber("duration");
  const idx = currentStreamIndex();
  if (!duration || duration <= 0 || idx === null) return;
  companionPost(`/media?file_idx=${idx}&duration=${duration}`).catch(() => {});
});

// Stop preloaded companions whose torrent is no longer the next playlist entry (removed, skipped
// past, or the playlist was replaced), so they don't keep using peers and bandwidth all session.
let handledNext = null;
function dropPreloads(keepSource) {
  for (const [src, pre] of preloaded) {
    if (src === keepSource) continue;
    log.log(`[mpv-gotorrent] Stopping preloaded companion: ${src}`);
    preloaded.delete(src);
    pre.cancelled = true;
    pre.promise.then((data) => companionPost("/shutdown", bannerServer(data))).catch(() => {});
    handledNext = null; // so re-adding the same entry preloads it again
  }
}

// Start the companion for a torrent queued next and prefetch its first file
function preloadTorrent(source) {
  log.log(`[mpv-gotorrent] Preloading next torrent: ${source}`);
  const pre = { promise: launchCompanion(source, { quiet: true }), cancelled: false };
  preloaded.set(source, pre);
  pre.promise
    .then((data) => {
      const first = data.videos && data.videos[0];
      if (!pre.cancelled && first) {
        return companionPost(`/prefetch?file_idx=${first.index}`, bannerServer(data));
      }
    })
    .catch((err) => {
      log.warn(`[mpv-gotorrent] Preload failed: ${err.message}`);
      if (preloaded.get(source) === pre) preloaded.delete(source); // retried when its entry plays
    });
}

// Warm up the next playlist entry once the current one is far enough along, so it starts without
// waiting for the swarm. Next file of the same torrent: prefetch its opening. A different torrent
// (magnet/.torrent added to the playlist): start its companion now and prefetch its first file.
function prefetchNext() {
  const pct = mpv.getNumber("percent-pos");
  if (!pct) return; // unavailable between files; the entry being loaded may still need its preload

  const pos = mpv.getNumber("playlist-pos");
  if (pos === undefined || pos === null || pos < 0) return;
  const nextPath = mpv.getString(`playlist/${pos + 1}/filename`) || "";
  const source = nextPath ? extractTorrentSource(nextPath) : null;
  dropPreloads(source);

  if (pct < PREFETCH_NEXT_AT || !nextPath) return;
  const key = `${mpv.getString("path")}\n${nextPath}`;
  if (key === handledNext) return;
  handledNext = key;

  // The server wants the next file's index inside the torrent, not its playlist position
  const nextLoc = streamLocation(nextPath);
  if (nextLoc) {
    if (currentServer && nextLoc.hash === currentServer.infohash) {
      companionPost(`/prefetch?file_idx=${nextLoc.idx}`).catch(() => {});
    }
    return;
  }
  if (source && !preloaded.has(source)) preloadTorrent(source);
}
event.on("mpv.percent-pos.changed", prefetchNext);
// Entries added after the threshold was passed
event.on("mpv.playlist-count.changed", prefetchNext);

// Poll status endpoint and send updates to sidebar
async function fetchStats() {
  const server = currentServer;
  if (!server || !server.baseUrl) return;
  try {
    const res = await http.get(`${server.baseUrl}/status`);
    if (res && res.data) {
      sidebar.postMessage("stats", res.data);
      warnIfNoPeers(server, res.data);
    }
  } catch (e) {
    // Companion temporarily unavailable
  }
}

// A torrent with no peers just buffers forever once its metadata is known (a .torrent file or
// cached metainfo resolves instantly), so say so instead of leaving the user guessing.
function warnIfNoPeers(server, stats) {
  if (server !== currentServer || server.peersSeen) return;
  if (stats.peers > 0 || (stats.progress || 0) >= 100) {
    server.peersSeen = true;
    return;
  }
  if (Date.now() - server.startedAt > NO_PEERS_WARN_MS) {
    server.peersSeen = true; // warn once
    log.warn("[mpv-gotorrent] No peers connected after 30s");
    core.osd("No peers found after 30s. This torrent may be dead.");
  }
}

function startStatsPoller() {
  if (statsTimer) clearInterval(statsTimer);
  statsTimer = setInterval(fetchStats, 1000);
  fetchStats();
}

// Sidebar integration; the main window must exist before loadFile
event.on("iina.window-loaded", () => {
  sidebar.loadFile("ui/sidebar.html");

  sidebar.onMessage("keep", async () => {
    if (!currentServer) {
      core.osd("No active torrent stream");
      return;
    }
    const opts = getPluginOpts();
    // Without file_idx the companion falls back to the largest file, not the playing episode
    const idx = currentStreamIndex();
    const fileParam = idx === null ? "" : `&file_idx=${idx}`;
    try {
      const res = await companionPost(`/keep?scope=${encodeURIComponent(opts.keepScope)}&dir=${encodeURIComponent(opts.keepDir)}${fileParam}`);
      const d = (res && res.data) || {};
      const pct = (d.progress || 0).toFixed(1);
      const messages = {
        keeping: `💾 Keeping ${d.name} → ${d.dest_path}`,
        already_keeping: `Already saving ${d.name} (${pct}%)`,
        completed: `Already saved: ${d.dest_path}`,
      };
      core.osd(messages[d.status] || d.message || "Saving video permanently to disk…");
    } catch (e) {
      core.osd("Failed to request keep");
    }
  });

  sidebar.onMessage("poll", fetchStats);
});

// Cleanup on player close
event.on("iina.window-will-close", stopCompanion);
