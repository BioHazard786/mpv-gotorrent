/**
 * mpv-gotorrent - IINA Player Instance Script
 * Author: biohazard786
 * License: MIT
 */

const { core, mpv, utils, sidebar, hud, preferences, console: log } = iina;

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

// Start companion process and poll state file asynchronously
async function launchCompanion(source) {
  const binaryPath = await (globalThis.__gotorrent?.locateBinary() || "mpv-gotorrent");
  const opts = getPluginOpts();
  const pid = mpv.getInt("pid") || 0;
  const stateFilePath = `/tmp/mpv_gotorrent_${pid}_${Date.now()}.json`;

  const args = [
    `--source=${source}`,
    `--parent-pid=${pid}`,
    `--port=${opts.port}`,
    `--timeout=${opts.timeout}`,
    `--state-file=${stateFilePath}`,
    `--json-output=false`
  ];

  if (opts.maxRamMb > 0) args.push(`--max-ram-mb=${opts.maxRamMb}`);
  if (opts.keepDir) args.push(`--keep-dir=${opts.keepDir}`);
  if (opts.keepScope) args.push(`--keep-scope=${opts.keepScope}`);

  log.log(`[mpv-gotorrent] Spawning companion: ${binaryPath} with source: ${source}`);

  // Launch detached using mpv native subprocess so companion outlives playback hooks
  mpv.commandNativeAsync({
    name: "subprocess",
    playback_only: false,
    detach: true,
    args: [binaryPath, ...args]
  });

  const startTime = Date.now();
  const timeoutMs = (opts.timeout || 60) * 1000;

  // Poll state file until companion is ready
  return new Promise((resolve, reject) => {
    const poller = setInterval(async () => {
      try {
        if (utils.fileInPath(stateFilePath)) {
          const content = utils.readFile(stateFilePath);
          if (content && content.length > 5) {
            const parsed = JSON.parse(content);
            if (parsed.status === "ready") {
              clearInterval(poller);
              currentServer = {
                port: parsed.port,
                baseUrl: parsed.base_url,
                token: parsed.token,
                infohash: parsed.infohash?.toLowerCase()
              };
              activeTorrent = {
                name: parsed.name,
                infohash: parsed.infohash?.toLowerCase(),
                totalSize: parsed.total_size,
                videos: parsed.videos || []
              };
              globalThis.__gotorrent?.setActiveCompanion(currentServer);
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
    }, 100);
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
  hud.show("Connecting to BitTorrent swarm…");

  try {
    const data = await launchCompanion(source);

    if (data.m3u_playlist && getPluginOpts().autoPlaylist) {
      mpv.set("stream-open-filename", "memory://" + data.m3u_playlist);
      if (data.videos && data.videos.length > 1) {
        hud.show(`Loaded ${data.videos.length} episodes into playlist`);
      }
    } else if (data.first_stream_url) {
      mpv.set("stream-open-filename", data.first_stream_url);
    }

    startStatsPoller();
  } catch (err) {
    log.error(`[mpv-gotorrent] ${err.message}`);
    hud.show(`mpv-gotorrent error: ${err.message}`);
  }

  next();
});

// Auto-inject internal subtitles on playback load
mpv.registerEvent("file-loaded", () => {
  if (!activeTorrent || !activeTorrent.videos || activeTorrent.videos.length === 0) return;

  const currentPath = mpv.getString("path") || "";
  let matchedVideo = null;

  for (const v of activeTorrent.videos) {
    if (currentPath.includes(v.stream_url) || (v.name && currentPath.includes(encodeURIComponent(v.name)))) {
      matchedVideo = v;
      break;
    }
  }

  if (!matchedVideo) {
    const pos = mpv.getInt("playlist-pos") || 0;
    matchedVideo = activeTorrent.videos[pos] || activeTorrent.videos[0];
  }

  if (matchedVideo) {
    // Set clean media title
    const cleanTitle = matchedVideo.name.replace(/\.[^/.]+$/, "");
    mpv.set("file-local-options/force-media-title", cleanTitle);

    // Inject subtitles
    if (matchedVideo.subtitles && Array.isArray(matchedVideo.subtitles)) {
      for (const sub of matchedVideo.subtitles) {
        mpv.commandv("sub-add", sub.download_url, "auto", sub.name);
      }
    }
  }
});

// POST to companion server with session token
function companionPost(endpoint) {
  if (!currentServer || !currentServer.baseUrl) {
    return Promise.reject(new Error("No active companion"));
  }
  const headers = {};
  if (currentServer.token) {
    headers["X-Gotorrent-Token"] = currentServer.token;
  }
  return fetch(`${currentServer.baseUrl}${endpoint}`, {
    method: "POST",
    headers
  });
}

// Report duration to companion for dynamic buffer calculation
mpv.observeProperty("duration", "number", (_, duration) => {
  if (!duration || duration <= 0 || !currentServer) return;
  companionPost(`/media?duration=${duration}`).catch(() => {});
});

// Prefetch next episode opening when current video reaches 80%
let lastPrefetched = null;
mpv.observeProperty("percent-pos", "number", (_, pct) => {
  if (!pct || pct < 80 || !currentServer) return;
  const path = mpv.getString("path");
  if (path === lastPrefetched) return;
  lastPrefetched = path;

  const pos = mpv.getInt("playlist-pos");
  if (pos !== undefined && pos >= 0) {
    const nextIdx = pos + 1;
    companionPost(`/prefetch?file_idx=${nextIdx}`).catch(() => {});
  }
});

// Sidebar integration
try {
  const sidebarPath = utils.fileInPath("ui/sidebar.html") ? "ui/sidebar.html" : "iina-plugin/ui/sidebar.html";
  sidebar.loadFile(sidebarPath);

  sidebar.onMessage("keep", async () => {
    if (!currentServer) {
      hud.show("No active torrent stream");
      return;
    }
    const opts = getPluginOpts();
    try {
      const url = `/keep?scope=${encodeURIComponent(opts.keepScope)}&dir=${encodeURIComponent(opts.keepDir)}`;
      const res = await companionPost(url);
      const data = await res.json();
      hud.show(`💾 ${data.message || "Saving video permanently to disk…"}`);
    } catch (e) {
      hud.show("Failed to request keep");
    }
  });

  sidebar.onMessage("poll", async () => {
    fetchStats();
  });
} catch (e) {
  // Sidebar tab not active
}

// Poll status endpoint and send updates to sidebar
async function fetchStats() {
  if (!currentServer || !currentServer.baseUrl) return;
  try {
    const res = await fetch(`${currentServer.baseUrl}/status`);
    if (res.ok) {
      const stats = await res.json();
      sidebar.postMessage("stats", stats);
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

// Cleanup on player shutdown
mpv.registerEvent("shutdown", () => {
  if (statsTimer) clearInterval(statsTimer);
  if (currentServer) {
    companionPost("/shutdown").catch(() => {});
    currentServer = null;
  }
});

