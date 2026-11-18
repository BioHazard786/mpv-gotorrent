-- Drives mpv for the smoke test, selected by PROBE_MODE:
--   dump:  print the playlist a few seconds after start, then quit
--   keep2: Ctrl+K on the first episode, play the next, Ctrl+K again, then quit
local mode = os.getenv("PROBE_MODE") or "dump"

if mode == "dump" then
    mp.add_timeout(tonumber(os.getenv("DUMP_AFTER") or "4"), function()
        for i = 0, mp.get_property_number("playlist-count", 0) - 1 do
            local name = mp.get_property("playlist/" .. i .. "/filename"):match("([^/]+)$")
            print("PL " .. name .. (mp.get_property_bool("playlist/" .. i .. "/current") and " *" or ""))
        end
        mp.command("quit")
    end)
elseif mode == "keep2" then
    local loaded = 0
    mp.register_event("file-loaded", function()
        loaded = loaded + 1
        mp.add_timeout(0.7, function() mp.command("script-binding main/gotorrent-keep") end)
        if loaded == 1 then
            mp.add_timeout(1.5, function() mp.command("playlist-next") end)
        else
            mp.add_timeout(4, function() mp.command("quit") end)
        end
    end)
end
