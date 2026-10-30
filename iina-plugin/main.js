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

let currentServer = null;   // { port, baseUrl, token, infohash }
let activeTorrent = null;   // { name, infohash, videos, totalSize, ... }
let statsTimer = null;

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

function companionHeaders() {
  return currentServer && currentServer.token ? { "X-Gotorrent-Token": currentServer.token } : {};
}

function companionPost(endpoint) {
  if (!currentServer || !currentServer.baseUrl) {
    return Promise.reject(new Error("No active companion"));
  }
  return http.post(`${currentServer.baseUrl}${endpoint}`, { headers: companionHeaders() });
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

// Start companion process and poll state file asynchronously
async function launchCompanion(source) {
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

  log.log(`[mpv-gotorrent] Spawning companion: ${binaryPath} (source: ${source})`);

  // utils.exec waits for the process to exit, so background it from the shell to return immediately
  const cmd = `nohup ${[binaryPath, ...args].map(shQuote).join(" ")} > ${shQuote(logFilePath)} 2>&1 &`;
  await utils.exec("/bin/sh", ["-c", cmd]);

  const startTime = Date.now();
  const timeoutMs = (opts.timeout || 60) * 1000;

  return new Promise((resolve, reject) => {
    const poller = setInterval(() => {
      try {
        if (file.exists(stateRef)) {
          const content = file.read(stateRef);
          if (content && content.length > 5) {
            const parsed = JSON.parse(content);
            if (parsed.status === "ready") {
              clearInterval(poller);
              currentServer = {
                port: parsed.port,
                baseUrl: parsed.base_url,
                token: parsed.token,
                infohash: parsed.infohash?.toLowerCase(),
              };
              activeTorrent = {
                name: parsed.name,
                infohash: parsed.infohash?.toLowerCase(),
                totalSize: parsed.total_size,
                videos: parsed.videos || [],
              };
              resolve(parsed);
              return;
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
  const source = extractTorrentSource(rawUrl);

  if (!source) {
    next();
    return;
  }

  log.log(`[mpv-gotorrent] Intercepted torrent source: ${source}`);
  core.osd("Connecting to BitTorrent swarm…");

  try {
    stopCompanion();
    const data = await launchCompanion(source);

    if (data.m3u_playlist && getPluginOpts().autoPlaylist) {
      mpv.set("stream-open-filename", "memory://" + data.m3u_playlist);
      if (data.videos && data.videos.length > 1) {
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
function onFileLoaded() {
  if (!activeTorrent || !activeTorrent.videos || activeTorrent.videos.length === 0) return;

  const currentPath = mpv.getString("path") || "";
  if (currentPath === lastLoad.path && Date.now() - lastLoad.at < 500) return;
  lastLoad = { path: currentPath, at: Date.now() };

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
  if (!duration || duration <= 0 || !currentServer) return;
  companionPost(`/media?duration=${duration}`).catch(() => {});
});

// Prefetch next episode opening when current video reaches 80%
let lastPrefetched = null;
event.on("mpv.percent-pos.changed", () => {
  const pct = mpv.getNumber("percent-pos");
  if (!pct || pct < 80 || !currentServer) return;
  const path = mpv.getString("path");
  if (path === lastPrefetched) return;
  lastPrefetched = path;

  const pos = mpv.getNumber("playlist-pos");
  if (pos !== undefined && pos >= 0) {
    companionPost(`/prefetch?file_idx=${pos + 1}`).catch(() => {});
  }
});

// Poll status endpoint and send updates to sidebar
async function fetchStats() {
  if (!currentServer || !currentServer.baseUrl) return;
  try {
    const res = await http.get(`${currentServer.baseUrl}/status`);
    if (res && res.data) {
      sidebar.postMessage("stats", res.data);
    }
  } catch (e) {
    // Companion temporarily unavailable
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
    try {
      const res = await companionPost(`/keep?scope=${encodeURIComponent(opts.keepScope)}&dir=${encodeURIComponent(opts.keepDir)}`);
      core.osd(`💾 ${(res.data && res.data.message) || "Saving video permanently to disk…"}`);
    } catch (e) {
      core.osd("Failed to request keep");
    }
  });

  sidebar.onMessage("poll", fetchStats);
});

// Cleanup on player close
event.on("iina.window-will-close", stopCompanion);
