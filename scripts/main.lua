--[[
    mpv-gotorrent: Stream torrents and magnet links directly in mpv using Go.
    Author: biohazard786
    License: MIT
--]]

local mp = require("mp")
local msg = require("mp.msg")
local options = require("mp.options")
local utils = require("mp.utils")

-- Default Configuration
local opts = {
    binary_path = "",           -- Custom path to mpv-gotorrent executable (optional)
    port = 8989,                -- Preferred deterministic HTTP port (default: 8989)
    cache_dir = "",             -- Custom cache directory (blank = system default)
    keep_files = false,         -- Keep downloaded files on exit (default false = auto cleanup)
    max_ram_mb = 0,             -- Max RAM in MB for memory-only ring buffer (0 = use disk cache)
    max_cached_episodes = 3,    -- Disk mode: keep only the last N played files on disk, deleting older ones (0 = keep all until exit)
    max_upload_kbps = 0,        -- Upload rate limit in KiB/s (0 = unlimited)
    max_download_kbps = 0,      -- Download rate limit in KiB/s (0 = unlimited; below the video bitrate playback stalls)
    seed_ratio = 0,             -- Stop uploading at this multiple of what was downloaded (e.g. 1.0; 0 = no cap)
    keep_dir = "",              -- Target folder for kept files (blank = ~/Videos or ~/Downloads)
    keep_scope = "current",     -- 'current' (active video only) or 'all' (entire torrent)
    detach_on_keep = true,      -- Finish downloading kept files in background when mpv exits
    timeout = 60,               -- Metadata resolution timeout in seconds
    auto_playlist = true,       -- Auto-playlist mode for multi-file torrents (default: true)
    show_osd = true,            -- Show OSD messages on loading
    osd_duration = 3,           -- Seconds to display loading notices
    prefetch_next_at = 80,      -- Start downloading the next episode at this % of the current one (0 = off)

    -- Key bindings (blank = no default key; input.conf can still bind the script-binding names)
    key_stats = "p",            -- Toggle stats overlay (script-binding gotorrent-stats)
    key_keep = "ctrl+k",        -- Keep current file (script-binding gotorrent-keep)

    -- Advanced ASS Text Styling (uses mpv default font and clean sizing)
    font = "",                  -- Blank = use mpv default font
    font_size = 25,             -- OSD font size
    font_color = "FFFFFF",      -- White main text (BGR hex)
    border_size = 1.2,
    border_color = "151515",    -- Dark border
    shadow_color = "000000",
    accent_color = "C7A314",    -- Cyan accent
    progress_color = "91C714",  -- Green accent
}

options.read_options(opts, "gotorrent")

local current_server = nil      -- active companion metadata { port, base_url, cmd_id, infohash }
local active_torrent = nil      -- active torrent metadata { name, infohash, videos, ... }
local is_windows = package.config:sub(1,1) == "\\"

local osd_overlay = nil
local osd_timer = nil
local osd_active = false

-- Strip common file extensions (.mkv, .mp4, etc.)
local function strip_extension(filename)
    if not filename or filename == "" then return "" end
    return (filename:gsub("%.%w+$", ""))
end

-- Append companion streams to the playlist through an M3U, so the entries get clean titles like
-- the playlist built on first load, instead of showing raw stream URLs
local function append_titled(videos)
    if #videos == 0 then return end
    local lines = {"#EXTM3U"}
    for _, v in ipairs(videos) do
        table.insert(lines, "#EXTINF:-1," .. strip_extension(v.name))
        table.insert(lines, v.stream_url)
    end
    mp.commandv("loadlist", "memory://" .. table.concat(lines, "\n") .. "\n", "append")
end

-- Detect if a URL is an internal gotorrent stream endpoint
local function extract_resume_infohash(url)
    if not url or url == "" then return nil end
    local hash = url:match("https?://127%.0%.0%.1:%d+/torrent/([0-9a-fA-F]+)")
    if hash then return hash:lower() end
    return nil
end

-- Split a gotorrent stream URL into its infohash and file index
local function stream_location(url)
    if not url then return nil end
    local hash, idx = url:match("^https?://127%.0%.0%.1:%d+/torrent/([0-9a-fA-F]+)/([0-9]+)/")
    if not hash then return nil end
    return hash:lower(), tonumber(idx)
end

-- Return the file index of the current stream if it belongs to the active companion
local function current_stream_index()
    if not current_server or not current_server.base_url then return nil end
    local hash, idx = stream_location(mp.get_property("path"))
    if hash ~= current_server.infohash then return nil end
    return idx
end

-- Percent-decode a URL string
local function url_decode(str)
    if not str then return "" end
    return (str:gsub("%%(%x%x)", function(hex)
        return string.char(tonumber(hex, 16))
    end))
end

-- Extract clean torrent source (stripping any cwd prepended by mpv)
local function extract_torrent_source(url)
    if not url or url == "" then return nil end

    -- Ignore our own internal loopback stream URLs
    if url:match("^https?://127%.0%.0%.1:%d+/") then
        return nil
    end

    -- Normalize percent-encoding if present
    local clean_url = url:find("%%") and url_decode(url) or url

    -- 1. Magnet Link (handles magnet:?, magnet/? or URI-normalized magnet? from desktop/browser handlers)
    local raw_magnet = clean_url:match("(magnet[:/%?].+)")
    if raw_magnet then
        local query_start = raw_magnet:find("%?")
        if query_start then
            return "magnet:" .. raw_magnet:sub(query_start)
        end
    end

    -- 2. HTTP/HTTPS URL pointing to a .torrent file
    local remote_torrent = clean_url:match("(https?://.*%.torrent.*)")
    if remote_torrent then
        return remote_torrent
    end

    -- 3. Local or relative .torrent file
    if clean_url:match("%.torrent$") or clean_url:match("%.torrent%?") then
        return clean_url
    end

    -- 4. 40-character hex or 32-character base32 infohash (mpv may prepend cwd, so check the
    -- last path component; Lua patterns have no {n} quantifier). Real files are left alone.
    if not utils.file_info(clean_url) then
        local name = clean_url:match("([^/\\]+)$") or clean_url
        local hash = name:match("^(" .. string.rep("%x", 40) .. ")$")
            or name:match("^(" .. string.rep("[2-7A-Za-z]", 32) .. ")$")
        if hash then
            return "magnet:?xt=urn:btih:" .. hash
        end
    end

    return nil
end

-- Percent-encode a string for use in a URL query parameter
local function url_encode(str)
    return (tostring(str):gsub("[^%w%-%._~]", function(c)
        return string.format("%%%02X", string.byte(c))
    end))
end

-- POST to a companion endpoint asynchronously, authenticating with the session token
local function companion_post(url, callback, server)
    server = server or current_server
    local args = {"curl", "-s", "-X", "POST"}
    if server and server.token then
        table.insert(args, "-H")
        table.insert(args, "X-Gotorrent-Token: " .. server.token)
    end
    table.insert(args, url)
    mp.command_native_async({
        name = "subprocess",
        playback_only = false,
        capture_stdout = callback ~= nil,
        args = args,
    }, callback)
end

-- Locate companion binary in script directory or fallback paths
local function locate_binary()
    if opts.binary_path ~= "" then
        return opts.binary_path
    end

    local bin_name = is_windows and "mpv-gotorrent.exe" or "mpv-gotorrent"

    -- 1. Check directly alongside main.lua in mpv-gotorrent/ folder
    local script_dir = mp.get_script_directory()
    if script_dir then
        local direct_path = utils.join_path(script_dir, bin_name)
        local stat = utils.file_info(direct_path)
        if stat and stat.is_file then
            return direct_path
        end
    end

    -- 2. Check standard mpv script directory
    local conf_path = mp.find_config_file("scripts/mpv-gotorrent/" .. bin_name)
    if conf_path then return conf_path end

    -- 3. Common macOS / IINA and Unix fallback paths
    local home = os.getenv("HOME")
    if home and home ~= "" then
        local fallback_paths = {
            utils.join_path(home, ".config/mpv/scripts/mpv-gotorrent/" .. bin_name),
            utils.join_path(home, "Library/Application Support/mpv/scripts/mpv-gotorrent/" .. bin_name),
        }
        for _, p in ipairs(fallback_paths) do
            local stat = utils.file_info(p)
            if stat and stat.is_file then
                return p
            end
        end
    end

    -- 4. Fall back to system PATH
    return bin_name
end

-- Format bytes into human-readable string (e.g. 5.2 MB)
local function format_bytes(bytes)
    if not bytes or bytes <= 0 then return "0 B" end
    local units = {"B", "KB", "MB", "GB", "TB"}
    local idx = 1
    local val = bytes
    while val >= 1024 and idx < #units do
        val = val / 1024
        idx = idx + 1
    end
    return string.format("%.1f %s", val, units[idx])
end

-- Format speed (bytes/sec)
local function format_speed(bytes_per_sec)
    return format_bytes(bytes_per_sec) .. "/s"
end

-- Generate ASCII/Unicode progress bar
local function format_progress_bar(pct, length)
    length = length or 20
    pct = pct or 0
    local filled = math.floor((pct / 100.0) * length)
    if filled > length then filled = length end
    if filled < 0 then filled = 0 end
    local bar = string.rep("=", filled)
    if filled < length then
        bar = bar .. ">" .. string.rep("-", length - filled - 1)
    end
    return "[" .. bar .. "]"
end

-- Hide and remove live OSD overlay
local function hide_overlay()
    osd_active = false
    if osd_timer then
        osd_timer:kill()
        osd_timer = nil
    end
    if osd_overlay then
        osd_overlay:remove()
        osd_overlay = nil
    end
end

-- Ask the previous companion server to stop. It is never killed: it cleans up its cache itself,
-- or finishes a kept download in the background first. If the request is lost, the companion
-- still notices when mpv exits.
local function stop_active_companion()
    hide_overlay()
    if current_server then
        if current_server.base_url then
            msg.info("Requesting shutdown for companion on " .. current_server.base_url)
            companion_post(current_server.base_url .. "/shutdown")
        end
        current_server = nil
        active_torrent = nil
    end
end

-- A torrent with no peers just buffers forever once its metadata is known (a .torrent file or
-- cached metainfo resolves instantly), so say so instead of leaving the user guessing.
local NO_PEERS_WARN_AFTER = 30
local function warn_if_no_peers(server)
    mp.add_timeout(NO_PEERS_WARN_AFTER, function()
        if current_server ~= server then return end
        mp.command_native_async({
            name = "subprocess",
            playback_only = false,
            capture_stdout = true,
            args = {"curl", "-s", server.base_url .. "/status"},
        }, function(success, res)
            if not success or not res or res.status ~= 0 or current_server ~= server then return end
            local stats = utils.parse_json(res.stdout or "")
            if stats and (stats.peers or 0) == 0 and (stats.progress or 0) < 100 then
                msg.warn("No peers connected after " .. NO_PEERS_WARN_AFTER .. "s")
                mp.osd_message(string.format(
                    "mpv-gotorrent: No peers found after %ds.\nThis torrent may be dead.", NO_PEERS_WARN_AFTER), 6)
            end
        end)
    end)
end

-- Make a ready companion (the banner it wrote) the one the rest of the script talks to
local function adopt_companion(parsed)
    current_server = {
        port = parsed.port,
        base_url = parsed.base_url,
        token = parsed.token,
        infohash = parsed.infohash and parsed.infohash:lower(),
    }
    active_torrent = {
        name = parsed.name,
        infohash = current_server.infohash,
        total_size = parsed.total_size,
        videos = parsed.videos or {},
        videos_count = parsed.videos_count or 0,
    }
    warn_if_no_peers(current_server)
    if parsed.warning and parsed.warning ~= "" then
        msg.warn(parsed.warning)
        mp.osd_message("mpv-gotorrent: " .. parsed.warning, 8)
    end
end

-- Companions started ahead of time for torrents queued behind the current file, by source.
-- An entry is {} while connecting, then {data = banner}; `waiting` is set if its entry loads first.
local preloaded = {}

-- Launch companion process and poll state file asynchronously (non-blocking).
-- quiet: log failures without an on-screen message (background preloads).
local function launch_companion(source, callback, quiet)
    local bin_path = locate_binary()
    local my_pid = mp.get_property_number("pid") or 0

    local temp_dir = os.getenv("TEMP") or os.getenv("TMP") or "/tmp"
    local state_filename = string.format("mpv_gotorrent_%d_%d.json", my_pid, math.floor(mp.get_time() * 1000))
    local state_file_path = utils.join_path(temp_dir, state_filename)
    pcall(os.remove, state_file_path)

    local args = {
        bin_path,
        "--source=" .. source,
        "--parent-pid=" .. tostring(my_pid),
        "--port=" .. tostring(opts.port or 8989),
        "--timeout=" .. tostring(opts.timeout or 60),
        "--state-file=" .. state_file_path,
        "--json-output=false",
    }

    if opts.cache_dir ~= "" then
        table.insert(args, "--cache-dir=" .. opts.cache_dir)
    end
    if opts.keep_files then
        table.insert(args, "--keep-files=true")
    end
    if opts.max_ram_mb and opts.max_ram_mb > 0 then
        table.insert(args, "--max-ram-mb=" .. tostring(opts.max_ram_mb))
    end
    -- Only when changed from the companion's default (3), so an older binary without the flag still starts
    if opts.max_cached_episodes ~= 3 then
        table.insert(args, "--max-cached-files=" .. tostring(opts.max_cached_episodes))
    end
    -- Only passed when set, so an older companion binary without these flags still starts
    if opts.max_upload_kbps > 0 then
        table.insert(args, "--max-upload-kbps=" .. tostring(math.floor(opts.max_upload_kbps)))
    end
    if opts.max_download_kbps > 0 then
        table.insert(args, "--max-download-kbps=" .. tostring(math.floor(opts.max_download_kbps)))
    end
    if opts.seed_ratio > 0 then
        table.insert(args, "--seed-ratio=" .. tostring(opts.seed_ratio))
    end
    if opts.keep_dir and opts.keep_dir ~= "" then
        table.insert(args, "--keep-dir=" .. opts.keep_dir)
    end
    if opts.keep_scope and opts.keep_scope ~= "" then
        table.insert(args, "--keep-scope=" .. opts.keep_scope)
    end
    if opts.detach_on_keep ~= nil then
        table.insert(args, "--detach-on-keep=" .. tostring(opts.detach_on_keep))
    end

    msg.info("Spawning companion binary: " .. bin_path .. " (source: " .. source .. ")")

    -- Detached so mpv can't SIGKILL it on quit; the companion watches mpv's PID instead
    mp.command_native_async({
        name = "subprocess",
        playback_only = false,
        detach = true,
        args = args,
    }, function(success, res, err)
        if not success then
            msg.warn("Failed to start companion: " .. tostring(err))
        end
    end)

    local start_time = mp.get_time()
    local poll_timer = nil

    local function cleanup_poll()
        if poll_timer then
            poll_timer:kill()
            poll_timer = nil
        end
        pcall(os.remove, state_file_path)
    end

    local function check_state()
        local f = io.open(state_file_path, "r")
        if f then
            local content = f:read("*all")
            f:close()
            if content and #content > 5 then
                local parsed = utils.parse_json(content)
                if parsed and parsed.status == "ready" then
                    cleanup_poll()
                    callback(true, parsed)
                    return
                elseif parsed and parsed.status == "connecting" then
                    -- Swarm size while metadata is fetched; 0 peers for long means a dead torrent
                    if not quiet and opts.show_osd then
                        mp.osd_message(string.format("mpv-gotorrent: Connecting to swarm... %d peers (%d seeders)",
                            parsed.peers or 0, parsed.seeders or 0), 2)
                    end
                elseif parsed and parsed.status == "error" then
                    cleanup_poll()
                    if not quiet then
                        mp.osd_message("mpv-gotorrent error: " .. (parsed.error or "unknown"), 5)
                    end
                    msg.error("Companion error: " .. (parsed.error or "unknown"))
                    callback(false, parsed.error)
                    return
                end
            end
        end

        if mp.get_time() - start_time > (opts.timeout or 60) then
            cleanup_poll()
            if not quiet then
                mp.osd_message("mpv-gotorrent: Metadata fetch timed out.", 5)
            end
            msg.error("Timed out waiting for torrent metadata.")
            callback(false, "timeout")
            return
        end
    end

    poll_timer = mp.add_periodic_timer(0.08, check_state)
end

-- Hook: on_load (priority 10 so we intercept before standard file opens)
mp.add_hook("on_load", 10, function(hook)
    local raw_url = mp.get_property("stream-open-filename", "")

    -- 1. Check if this is an existing stream URL being resumed (e.g. from history, bookmark, or watch-later)
    local resume_hash = extract_resume_infohash(raw_url)
    if resume_hash then
        -- If companion is already active for this infohash, proceed immediately. The entry may
        -- still carry the port of an earlier session (a resumed history entry), so point it here.
        if current_server and current_server.infohash == resume_hash then
            mp.set_property("stream-open-filename",
                (raw_url:gsub("^https?://127%.0%.0%.1:%d+", function() return current_server.base_url end)))
            return
        end

        msg.info("Resuming torrent stream for infohash: " .. resume_hash)
        if opts.show_osd then
            mp.osd_message("mpv-gotorrent: Reconnecting stream...", opts.osd_duration)
        end

        hook:defer()
        stop_active_companion()

        launch_companion(resume_hash, function(success, data)
            if success then
                adopt_companion(data)
                msg.info("Companion reconnected for infohash: " .. resume_hash)
                -- The new companion may have bound a different port than the saved URL
                local new_url = raw_url:gsub("^https?://127%.0%.0%.1:%d+", function() return data.base_url end)
                mp.set_property("stream-open-filename", new_url)

                -- History/watch-later hands us just one episode; restore the rest of the pack
                -- around it once it is loaded, so prev/next keep working.
                local _, resumed_idx = stream_location(new_url)
                local videos = data.videos or {}
                if opts.auto_playlist and resumed_idx and #videos > 1 then
                    local function restore_playlist()
                        mp.unregister_event(restore_playlist)
                        -- Skip if the episode failed to load and this is some later file
                        if current_stream_index() ~= resumed_idx then return end
                        if mp.get_property_number("playlist-count", 0) ~= 1 then return end
                        -- videos is in playlist order, which can differ from torrent file order
                        local before, after, seen = {}, {}, false
                        for _, v in ipairs(videos) do
                            if v.index == resumed_idx then
                                seen = true
                            else
                                table.insert(seen and after or before, v)
                            end
                        end
                        -- append + playlist-move instead of loadfile insert-at, which needs mpv 0.38+
                        append_titled(before)
                        for i = 0, #before - 1 do
                            mp.commandv("playlist-move", tostring(i + 1), tostring(i))
                        end
                        append_titled(after)
                    end
                    mp.register_event("file-loaded", restore_playlist)
                end
            end
            hook:cont()
        end)
        return
    end

    -- 2. Check if this is a new torrent source (magnet link, .torrent file, or infohash)
    local source = extract_torrent_source(raw_url)
    if not source then
        return
    end

    msg.info("Intercepted torrent source: " .. source)
    if opts.show_osd then
        mp.osd_message("mpv-gotorrent: Connecting to BitTorrent swarm...", opts.osd_duration)
    end

    hook:defer()
    stop_active_companion()

    -- Reuse the companion preloaded for this entry, or start one
    local function get_companion(callback)
        local pre = preloaded[source]
        if not pre then return launch_companion(source, callback) end
        if pre.data then
            preloaded[source] = nil
            callback(true, pre.data)
        else
            pre.waiting = callback
        end
    end

    get_companion(function(success, data)
        if not success or not data then
            hook:cont()
            return
        end
        adopt_companion(data)

        local videos = data.videos or {}
        if #videos == 0 and not data.first_stream_url then
            mp.osd_message("mpv-gotorrent: No playable files found in torrent.", 5)
            hook:cont()
            return
        end

        -- Ingest M3U playlist via mpv's native memory:// demuxer
        -- This seamlessly replaces the raw magnet link in the playlist with clean HTTP stream URLs!
        if opts.auto_playlist and data.m3u_playlist and data.m3u_playlist ~= "" then
            msg.info("Ingesting torrent playlist via memory:// M3U demuxer")
            mp.set_property("stream-open-filename", "memory://" .. data.m3u_playlist)
            if opts.show_osd and #videos > 1 and not data.warning then
                mp.osd_message(string.format("Torrent loaded: %d episodes added to playlist", #videos), 4)
            end
        else
            -- Single file or direct stream fallback
            local first_url = data.first_stream_url or (videos[1] and videos[1].stream_url)
            if first_url then
                local first_title = (videos[1] and videos[1].name) or data.name or "Torrent Stream"
                local clean_first = strip_extension(first_title)
                local single_m3u = string.format("#EXTM3U\n#EXTINF:-1,%s\n%s\n", clean_first, first_url)
                mp.set_property("stream-open-filename", "memory://" .. single_m3u)
            end
        end

        hook:cont()
    end)
end)

-- Auto-load matching external subtitles when an episode or file is loaded
mp.register_event("file-loaded", function()
    if not active_torrent or not active_torrent.videos then return end

    local target_idx = current_stream_index()

    local matched_video = nil
    if target_idx ~= nil then
        for _, v in ipairs(active_torrent.videos) do
            if v.index == target_idx then
                matched_video = v
                break
            end
        end
    end

    if not matched_video and #active_torrent.videos > 0 then
        local pos = mp.get_property_number("playlist-pos", 0)
        matched_video = active_torrent.videos[pos + 1]
    end

    if matched_video then
        local clean_title = strip_extension(matched_video.name)
        mp.set_property("file-local-options/force-media-title", clean_title)

        if matched_video.subtitles then
            for _, sub in ipairs(matched_video.subtitles) do
                msg.info("Adding subtitle: " .. sub.name)
                mp.commandv("sub-add", sub.download_url, "auto", sub.name)
            end
        end
    end
end)

-- Report each file's duration so the companion can size its readahead from the bitrate.
-- Observed rather than read on file-loaded because some containers only know it later.
local duration_reported_path = nil
mp.observe_property("duration", "number", function(_, duration)
    if not duration or duration <= 0 then return end
    local path = mp.get_property("path")
    local idx = current_stream_index()
    if not idx or path == duration_reported_path then return end
    duration_reported_path = path
    companion_post(string.format("%s/media?file_idx=%d&duration=%.3f", current_server.base_url, idx, duration))
end)

-- Warm up the next playlist entry once the current one is far enough along, so it starts without
-- waiting for the swarm. Next file of the same torrent: prefetch its opening. A different torrent
-- (magnet/.torrent added to the playlist): start its companion now and prefetch its first file.
local handled_next = nil

-- Stop preloaded companions whose torrent is no longer the next playlist entry (removed, skipped
-- past, or the playlist was replaced), so they don't keep using peers and bandwidth all session.
-- A preload whose entry is already loading and waiting on it is left alone.
local function drop_preloads(keep_source)
    for src, pre in pairs(preloaded) do
        if src ~= keep_source and not pre.waiting then
            msg.info("Stopping preloaded companion: " .. src)
            preloaded[src] = nil
            pre.cancelled = true
            handled_next = nil -- so re-adding the same entry preloads it again
            if pre.data then
                companion_post(pre.data.base_url .. "/shutdown", nil, pre.data)
            end
        end
    end
end

local function prefetch_next()
    -- Unavailable between files; the entry being loaded may still need its preload then
    local pct = mp.get_property_number("percent-pos")
    if not pct then return end

    local pos = mp.get_property_number("playlist-pos", -1)
    if pos < 0 then return end
    local next_path = mp.get_property("playlist/" .. (pos + 1) .. "/filename")
    local source = next_path and extract_torrent_source(next_path)
    drop_preloads(source)

    if opts.prefetch_next_at <= 0 or pct < opts.prefetch_next_at then return end
    local key = mp.get_property("path", "") .. "\0" .. tostring(next_path)
    if not next_path or key == handled_next then return end
    handled_next = key

    local next_hash, next_idx = stream_location(next_path)
    if next_hash then
        if current_stream_index() and next_hash == current_server.infohash then
            msg.info(string.format("Prefetching next episode (file %d)", next_idx))
            companion_post(string.format("%s/prefetch?file_idx=%d", current_server.base_url, next_idx))
        end
        return
    end

    if not source or preloaded[source] then return end
    msg.info("Preloading next torrent: " .. source)
    local pre = {}
    preloaded[source] = pre
    launch_companion(source, function(success, data)
        if pre.waiting then
            preloaded[source] = nil
            -- The preload was quiet, but now the user is waiting on it
            if not success then mp.osd_message("mpv-gotorrent error: " .. tostring(data), 5) end
            return pre.waiting(success, data)
        end
        if pre.cancelled then
            if success then companion_post(data.base_url .. "/shutdown", nil, data) end
            return
        end
        if not success then
            preloaded[source] = nil
            return
        end
        pre.data = data
        local first = data.videos and data.videos[1]
        if first then
            companion_post(string.format("%s/prefetch?file_idx=%d", data.base_url, first.index), nil, data)
        end
    end, true)
end
mp.observe_property("percent-pos", "number", prefetch_next)
-- Entries added after the threshold was passed
mp.observe_property("playlist-count", "number", prefetch_next)

-- Refresh and render ASS styled OSD overlay (key_stats)
local function refresh_osd_overlay()
    if not osd_active or not current_server or not current_server.base_url then
        return
    end

    mp.command_native_async({
        name = "subprocess",
        playback_only = false,
        capture_stdout = true,
        args = {"curl", "-s", current_server.base_url .. "/status"}
    }, function(success, res)
        if not success or not res or res.status ~= 0 or not res.stdout or not osd_active then
            return
        end

        local stats = utils.parse_json(res.stdout)
        if not stats then return end

        if not osd_overlay then
            osd_overlay = mp.create_osd_overlay("ass-events")
        end

        local font_clause = (opts.font and opts.font ~= "") and string.format("\\fn%s", opts.font) or ""
        local style_header = string.format(
            "{\\r\\an7\\fs%d%s\\bord%.1f\\3c&H%s&\\1c&H%s&\\4c&H%s&}",
            opts.font_size,
            font_clause,
            opts.border_size,
            opts.border_color,
            opts.font_color,
            opts.shadow_color
        )

        local c_cyan = string.format("{\\1c&H%s&}", opts.accent_color)
        local c_green = string.format("{\\1c&H%s&}", opts.progress_color)
        local c_white = "{\\1c&HFFFFFF&}"

        local bar = format_progress_bar(stats.progress, 20)

        local storage_line = ""
        if stats.storage_mode == "ram" then
            storage_line = string.format("Storage:   " .. c_cyan .. "RAM Ring Buffer [%s / %s]" .. c_white .. "\\N",
                format_bytes(stats.ram_used_bytes or 0),
                format_bytes(stats.ram_max_bytes or 0)
            )
        else
            storage_line = "Storage:   Disk Cache\\N"
        end

        local keep_line = ""
        if stats.keep_active then
            local keep_dest = "to " .. (stats.keep_path or "~/Videos")
            if (stats.keep_count or 1) > 1 then
                keep_dest = string.format("%d files, latest %s", stats.keep_count, keep_dest)
            end
            keep_line = string.format("Keep:      " .. c_green .. "Saving %s (%.1f%%)" .. c_white .. "\\N",
                keep_dest,
                stats.keep_progress or 0
            )
        end

        local ass_content = string.format(
            "%s" ..
            c_cyan .. "[● BitTorrent Stream]" .. c_white .. "\\N" ..
            "Torrent:   %s\\N" ..
            "%s" ..
            "%s" ..
            "Speed:     " .. c_cyan .. "↓ %s" .. c_white .. "   " .. c_green .. "↑ %s" .. c_white .. "\\N" ..
            "Swarm:     Peers: %d (Active: %d, Seeders: %d)%s\\N" ..
            "Progress:  " .. c_green .. "%s %.1f%%" .. c_white .. "\\N" ..
            "Pieces:    %d / %d (%s / %s)",
            style_header,
            stats.name or "Streaming",
            storage_line,
            keep_line,
            format_speed(stats.download_speed or 0),
            format_speed(stats.upload_speed or 0),
            stats.peers or 0,
            stats.active_peers or 0,
            stats.seeders or 0,
            stats.upload_paused and "  [upload paused: seed ratio]" or "",
            bar,
            stats.progress or 0,
            stats.pieces_done or 0,
            stats.piece_count or 0,
            format_bytes(stats.completed_bytes or 0),
            format_bytes(stats.total_bytes or 0)
        )

        osd_overlay.data = ass_content
        osd_overlay:update()
    end)
end

-- Toggle OSD Overlay on/off (key_stats)
local function toggle_stats_overlay()
    if not current_server or not current_server.base_url then
        mp.osd_message("mpv-gotorrent: Not currently streaming a torrent.", 2)
        return
    end

    osd_active = not osd_active
    if osd_active then
        refresh_osd_overlay()
        if not osd_timer then
            osd_timer = mp.add_periodic_timer(1, refresh_osd_overlay)
        end
    else
        hide_overlay()
    end
end

-- Trigger "Keep File" (key_keep)
local function trigger_keep_file()
    if not current_server or not current_server.base_url then
        mp.osd_message("mpv-gotorrent: Not currently streaming a torrent.", 2)
        return
    end

    local file_idx = current_stream_index() or ""

    local url = string.format("%s/keep?scope=%s&dir=%s&file_idx=%s",
        current_server.base_url,
        url_encode(opts.keep_scope or "current"),
        url_encode(opts.keep_dir or ""),
        file_idx
    )

    companion_post(url, function(success, res)
        if not success or not res or res.status ~= 0 or not res.stdout then
            mp.osd_message("mpv-gotorrent: Failed to request keep.", 3)
            return
        end

        local data = utils.parse_json(res.stdout)
        if not data then return end

        if data.status == "keeping" then
            local pct = data.progress or 0
            local name = data.name or "file"
            local dest = data.dest_path or "~/Videos"
            mp.osd_message(string.format("💾 Keeping file: %s (%.1f%%)\nSaving to: %s", name, pct, dest), 4)
            if osd_active then refresh_osd_overlay() end
        elseif data.status == "already_keeping" then
            mp.osd_message(string.format("ℹ️ Already saving %s (%.1f%%)\nTarget: %s", data.name or "file", data.progress or 0, data.dest_path or "~/Videos"), 3)
        elseif data.status == "completed" then
            mp.osd_message(string.format(" File already saved: %s", data.dest_path or ""), 3)
        else
            mp.osd_message("mpv-gotorrent: " .. (data.message or "Keep requested"), 3)
        end
    end)
end

-- A blank key registers the binding without a default so input.conf can still map it
local function key_or_nil(key)
    if not key or key == "" then return nil end
    return key
end

mp.add_key_binding(key_or_nil(opts.key_stats), "gotorrent-stats", toggle_stats_overlay)
mp.add_key_binding(key_or_nil(opts.key_keep), "gotorrent-keep", trigger_keep_file)

-- Cleanup on mpv shutdown
mp.register_event("shutdown", stop_active_companion)
