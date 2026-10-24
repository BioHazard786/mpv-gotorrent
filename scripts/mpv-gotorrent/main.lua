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
    cache_dir = "",             -- Custom cache directory (blank = system temp)
    keep_files = false,         -- Keep downloaded files on exit (default false = auto cleanup)
    timeout = 60,               -- Metadata resolution timeout in seconds
    auto_playlist = true,       -- Auto-playlist mode for multi-file torrents (default: true)
    show_osd = true,            -- Show OSD messages on loading
    osd_duration = 3,           -- Seconds to display loading notices

    -- Advanced ASS Text Styling (uses mpv default font and larger size as requested)
    font = "",                  -- Blank = use mpv default font (no monospace)
    font_size = 22,             -- Increased font size
    font_color = "FFFFFF",      -- White main text (BGR hex)
    border_size = 1.2,
    border_color = "151515",    -- Dark border
    shadow_color = "000000",
    accent_color = "00F0FF",    -- Cyan accent (BGR hex: 00F0FF)
    progress_color = "33FF66",  -- Green accent (BGR hex: 33FF66)
}

options.read_options(opts, "gotorrent")

local current_server = nil      -- active companion metadata { port, base_url, pid, cmd_id }
local active_torrent = nil      -- active torrent metadata { name, infohash, videos, source }
local is_windows = package.config:sub(1,1) == "\\"

local osd_overlay = nil
local osd_timer = nil
local osd_active = false

local last_torrent_title = nil  -- remembered clean title for history logging
local last_torrent_source = nil -- remembered clean magnet/source for history logging

-- Decode URL percent-encoding (e.g. %20 -> space)
local function urldecode(s)
    if not s then return "" end
    return s:gsub("+", " "):gsub("%%(%x%x)", function(h)
        return string.char(tonumber(h, 16))
    end)
end

-- Strip common file extensions (.mkv, .mp4, etc.)
local function strip_extension(filename)
    if not filename or filename == "" then return "" end
    return (filename:gsub("%.%w+$", ""))
end

-- Extract clean torrent source (stripping any cwd prepended by mpv)
local function extract_torrent_source(url)
    if not url or url == "" then return nil end

    -- Ignore our own internal loopback stream URLs
    if url:match("^https?://127%.0%.0%.1:%d+/stream") then
        return nil
    end

    -- 1. Magnet Link (mpv often prepends cwd e.g. /path/to/cwd/magnet:?...)
    local magnet = url:match("(magnet:%?.+)")
    if magnet then
        return magnet
    end

    -- 2. HTTP/HTTPS URL pointing to a .torrent file
    local remote_torrent = url:match("(https?://.*%.torrent.*)")
    if remote_torrent then
        return remote_torrent
    end

    -- 3. Local or relative .torrent file
    if url:match("%.torrent$") or url:match("%.torrent%?") then
        return url
    end

    -- 4. 40-character hex or 32-character base32 infohash
    local hash = url:match("([0-9a-fA-F]{40})$") or url:match("([2-7a-zA-Z]{32})$")
    if hash then
        return "magnet:?xt=urn:btih:" .. hash
    end

    return nil
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

    -- 2. Check standard mpv script config directory
    local conf_path = mp.find_config_file("scripts/mpv-gotorrent/" .. bin_name)
    if conf_path then return conf_path end

    -- 3. Check legacy flat script path
    local flat_path = mp.find_config_file("scripts/" .. bin_name)
    if flat_path then return flat_path end

    -- 4. Fall back to system PATH
    return bin_name
end

-- Temporary sleep utility for hook polling
local function sleep_sec(sec)
    local start = mp.get_time()
    while mp.get_time() - start < sec do
        -- spin wait during synchronous hook phase
    end
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
    length = length or 22
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
    end
end

-- Update track titles and subtitles on track changes
local function update_track_metadata()
    if not active_torrent or not active_torrent.videos then return end
    local current_pos = mp.get_property_number("playlist-pos", 0)
    local v = active_torrent.videos[current_pos + 1]
    if not v then return end

    local clean_title = strip_extension(v.name)
    mp.set_property("force-media-title", clean_title)
    mp.set_property("title", clean_title)
    pcall(function()
        mp.set_property(string.format("playlist/%d/title", current_pos), clean_title)
    end)

    if v.subtitles then
        for _, sub in ipairs(v.subtitles) do
            mp.commandv("sub-add", sub.download_url, "auto", sub.name)
        end
    end
end

-- Ensure title bar never reverts to magnet link during video playback (e.g. from modernx or playlistmanager)
local function reaffirm_title()
    if not active_torrent or not active_torrent.videos then return end
    local current_pos = mp.get_property_number("playlist-pos", 0)
    local v = active_torrent.videos[current_pos + 1]
    if v then
        local clean_title = strip_extension(v.name)
        mp.set_property("force-media-title", clean_title)
        mp.set_property("title", clean_title)
    end
end

mp.observe_property("media-title", "string", function(name, val)
    if not val or val == "" or val:match("^magnet:") or val:match("/magnet:") or val:match("^https?://127%.0%.0%.1") then
        reaffirm_title()
    end
end)

mp.observe_property("title", "string", function(name, val)
    if not val or val == "" or val:match("^magnet:") or val:match("/magnet:") or val:match("^https?://127%.0%.0%.1") then
        reaffirm_title()
    end
end)

-- Sanitize SimpleHistory log file (mpvHistory.log)
-- Ensures lines contain clean titles without extension and clean magnet URIs for instant reopening.
local function sanitize_history_log()
    local log_paths = {}

    local conf = mp.find_config_file("mpvHistory.log")
    if conf then table.insert(log_paths, conf) end

    local conf_dir = mp.find_config_file(".")
    if conf_dir then
        table.insert(log_paths, utils.join_path(conf_dir, "mpvHistory.log"))
    end

    local home = os.getenv("HOME") or os.getenv("USERPROFILE") or ""
    if home ~= "" then
        table.insert(log_paths, utils.join_path(home, ".config/mpv/mpvHistory.log"))
        local appdata = os.getenv("APPDATA")
        if appdata and appdata ~= "" then
            table.insert(log_paths, utils.join_path(appdata, "mpv/mpvHistory.log"))
        end
    end

    local target_log = nil
    for _, p in ipairs(log_paths) do
        local stat = utils.file_info(p)
        if stat and stat.is_file then
            target_log = p
            break
        end
    end

    if not target_log then return end

    local f = io.open(target_log, "r")
    if not f then return end
    local lines = {}
    local modified = false

    for line in f:lines() do
        local new_line = line
        local has_magnet = line:match("(magnet:%?[^%s|]+)")
        local has_stream = line:match("(https?://127%.0%.0%.1:%d+/stream/[^%s|]+)") or line:match("(https?://127%.0%.0%.1:%d+/stream%?[^%s|]+)")

        if has_magnet or has_stream then
            local date_part = line:match("^(%[[^%]]+%])")
            local title_part = line:match('^%[[^%]]+%]%s*"(.-)"')
            local length_part = line:match("(length=[%d%.]+)")
            local time_part = line:match("(time=[%d%.]+)")

            local clean_magnet = has_magnet
            if not clean_magnet then
                clean_magnet = last_torrent_source or (active_torrent and active_torrent.source)
                if not clean_magnet and active_torrent and active_torrent.infohash then
                    clean_magnet = "magnet:?xt=urn:btih:" .. active_torrent.infohash
                end
            end

            local clean_title = title_part
            if not clean_title or clean_title == "" or clean_title == "Torrent Stream" or clean_title:match("^magnet:") then
                if last_torrent_title and last_torrent_title ~= "" then
                    clean_title = last_torrent_title
                elseif active_torrent and active_torrent.name then
                    clean_title = strip_extension(active_torrent.name)
                elseif clean_magnet and clean_magnet:match("[?&]dn=([^&]+)") then
                    local dn = clean_magnet:match("[?&]dn=([^&]+)")
                    clean_title = strip_extension(urldecode(dn))
                elseif has_stream then
                    local stem = has_stream:match(".+/(.+)") or ""
                    clean_title = strip_extension(urldecode(stem))
                end
            else
                clean_title = strip_extension(clean_title)
            end

            if date_part and clean_magnet and length_part and time_part then
                new_line = string.format('%s "%s" | %s | %s | %s', date_part, clean_title or "Torrent Stream", clean_magnet, length_part, time_part)
                if new_line ~= line then
                    modified = true
                end
            end
        end
        table.insert(lines, new_line)
    end
    f:close()

    if modified then
        local wf = io.open(target_log, "w")
        if wf then
            for _, l in ipairs(lines) do
                wf:write(l .. "\n")
            end
            wf:close()
            msg.info("Sanitized SimpleHistory log (" .. target_log .. ") for clean torrent stream reopening")
        end
    end
end

-- Perform initial history log hygiene on load
sanitize_history_log()

-- Terminate previous companion server if running
local function stop_active_companion()
    hide_overlay()
    if current_server then
        if current_server.base_url then
            msg.info("Requesting shutdown for server on " .. current_server.base_url)
            mp.command_native_async({
                name = "subprocess",
                playback_only = false,
                args = {"curl", "-s", "-X", "POST", current_server.base_url .. "/shutdown"}
            })
        end
        if current_server.cmd_id then
            mp.abort_async_command(current_server.cmd_id)
        end
        current_server = nil
        active_torrent = nil
    end
end

-- Hook: on_load (priority 10 so we intercept before standard file opens)
mp.add_hook("on_load", 10, function()
    local raw_url = mp.get_property("stream-open-filename", "")
    local source = extract_torrent_source(raw_url)
    if not source then
        return
    end

    msg.info("Intercepted torrent source: " .. source)
    if opts.show_osd then
        mp.osd_message("mpv-gotorrent: Connecting to BitTorrent swarm...", opts.osd_duration)
    end

    stop_active_companion()

    local bin_path = locate_binary()
    local my_pid = mp.get_property_number("pid") or 0

    -- Prepare temporary state file for zero-latency IPC
    local temp_dir = os.getenv("TEMP") or os.getenv("TMP") or "/tmp"
    local state_filename = string.format("mpv_gotorrent_%d_%d.json", my_pid, math.floor(mp.get_time() * 1000))
    local state_file_path = utils.join_path(temp_dir, state_filename)
    os.remove(state_file_path)

    local args = {
        bin_path,
        "--source=" .. source,
        "--parent-pid=" .. tostring(my_pid),
        "--port=0",
        "--timeout=" .. tostring(opts.timeout),
        "--state-file=" .. state_file_path,
        "--json-output=true",
    }

    if opts.cache_dir ~= "" then
        table.insert(args, "--cache-dir=" .. opts.cache_dir)
    end
    if opts.keep_files then
        table.insert(args, "--keep-files=true")
    end

    msg.info("Spawning companion binary: " .. bin_path)

    -- Spawn companion in the background
    local cmd_id = mp.command_native_async({
        name = "subprocess",
        playback_only = false,
        args = args,
    }, function(success, res, err)
        if not success or (res and res.status ~= 0) then
            msg.warn("Companion process exited or terminated.")
        end
    end)

    -- Poll state file for readiness
    local deadline = mp.get_time() + opts.timeout
    local ready_data = nil

    while mp.get_time() < deadline do
        local f = io.open(state_file_path, "r")
        if f then
            local content = f:read("*all")
            f:close()
            if content and #content > 5 then
                local parsed = utils.parse_json(content)
                if parsed and parsed.status == "ready" then
                    ready_data = parsed
                    os.remove(state_file_path)
                    break
                elseif parsed and parsed.status == "error" then
                    os.remove(state_file_path)
                    mp.osd_message("mpv-gotorrent error: " .. (parsed.error or "unknown"), 5)
                    msg.error("Companion error: " .. (parsed.error or "unknown"))
                    return
                end
            end
        end
        sleep_sec(0.08)
    end

    if not ready_data then
        os.remove(state_file_path)
        mp.osd_message("mpv-gotorrent: Metadata fetch timed out.", 5)
        msg.error("Timed out waiting for torrent metadata.")
        return
    end

    current_server = {
        port = ready_data.port,
        base_url = ready_data.base_url,
        cmd_id = cmd_id,
    }

    active_torrent = {
        name = ready_data.name,
        infohash = ready_data.infohash,
        total_size = ready_data.total_size,
        videos = ready_data.videos or {},
        videos_count = ready_data.videos_count or 0,
        source = source,
    }

    local videos = active_torrent.videos
    local first_stream = ready_data.first_stream_url

    if #videos == 0 and not first_stream then
        mp.osd_message("mpv-gotorrent: No playable files found in torrent.", 5)
        return
    end

    -- Redirect current stream to local HTTP endpoint
    mp.set_property("stream-open-filename", first_stream)

    -- Explicitly set clean media title and playlist title without extension
    local raw_title = (videos[1] and videos[1].name) or ready_data.name or "Torrent Stream"
    local clean_title = strip_extension(raw_title)
    last_torrent_title = clean_title
    last_torrent_source = source
    mp.set_property("force-media-title", clean_title)
    mp.set_property("title", clean_title)
    pcall(function()
        mp.set_property("playlist/0/title", clean_title)
    end)

    -- Auto-Playlist Mode (Default as requested)
    -- If multi-file torrent, automatically load all files sequentially into playlist
    if opts.auto_playlist and #videos > 1 then
        msg.info(string.format("Multi-file torrent detected (%d videos). Loading sequentially into playlist.", #videos))
        for i = 2, #videos do
            local v = videos[i]
            local item_title = strip_extension(v.name)
            mp.commandv("loadfile", v.stream_url, "append")
            pcall(function()
                mp.set_property(string.format("playlist/%d/title", i - 1), item_title)
            end)
        end
        if opts.show_osd then
            mp.osd_message(string.format("Torrent loaded: %d episodes added to playlist", #videos), 4)
        end
    else
        if opts.show_osd then
            mp.osd_message("Streaming: " .. clean_title, 3)
        end
    end

    -- Auto-load subtitles for first video
    if #videos > 0 and videos[1].subtitles then
        for _, sub in ipairs(videos[1].subtitles) do
            msg.info("Adding subtitle: " .. sub.name)
            mp.commandv("sub-add", sub.download_url, "auto", sub.name)
        end
    end
end)

-- Event hooks to ensure title bar and playlist stay clean during playback & track transitions
mp.register_event("start-file", update_track_metadata)
mp.register_event("file-loaded", function()
    update_track_metadata()
    -- Reaffirm clean title after modernx / playlistmanager file-loaded handlers finish
    mp.add_timeout(0.1, reaffirm_title)
    mp.add_timeout(0.5, reaffirm_title)
    -- Allow SimpleHistory to log, then sanitize entry
    mp.add_timeout(0.3, sanitize_history_log)
end)

mp.register_event("end-file", function()
    sanitize_history_log()
end)

-- Refresh and render ASS styled OSD overlay (webtorrent / stats.lua style)
local function refresh_osd_overlay()
    if not osd_active or not current_server or not current_server.base_url then
        return
    end

    local res = mp.command_native({
        name = "subprocess",
        playback_only = false,
        capture_stdout = true,
        args = {"curl", "-s", current_server.base_url .. "/status"}
    })

    if not res or res.status ~= 0 or not res.stdout then
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
    local ass_content = string.format(
        "%s" ..
        c_cyan .. "[● BitTorrent Stream]" .. c_white .. "\\N" ..
        "Torrent:   %s\\N" ..
        "Speed:     " .. c_cyan .. "↓ %s" .. c_white .. "   " .. c_green .. "↑ %s" .. c_white .. "\\N" ..
        "Swarm:     Peers: %d (Active: %d)\\N" ..
        "Progress:  " .. c_green .. "%s %.1f%%" .. c_white .. "\\N" ..
        "Pieces:    %d / %d (%s / %s)",
        style_header,
        stats.name or "Streaming",
        format_speed(stats.download_speed or 0),
        format_speed(stats.upload_speed or 0),
        stats.peers or 0,
        stats.active_peers or 0,
        bar,
        stats.progress or 0,
        stats.pieces_done or 0,
        stats.piece_count or 0,
        format_bytes(stats.completed_bytes or 0),
        format_bytes(stats.total_bytes or 0)
    )

    osd_overlay.data = ass_content
    osd_overlay:update()
end

-- Toggle OSD Overlay on/off (Key: 'P')
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

mp.add_key_binding("P", "gotorrent-stats", toggle_stats_overlay)

-- Cleanup on mpv shutdown
mp.register_event("shutdown", function()
    sanitize_history_log()
    stop_active_companion()
end)
