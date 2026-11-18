#!/usr/bin/env bash
# Headless end-to-end check of scripts/main.lua with a real mpv and companion binary, using local
# .torrent files whose data is already in the cache, so no network swarm is needed.
# Needs: go, mpv, ffmpeg, python3, curl.
set -euo pipefail

repo="$(cd "$(dirname "$0")/../.." && pwd)"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
export XDG_CACHE_HOME="$work/xdg-cache" # keep the companion's metainfo/DHT caches out of ~

echo "==> building companion"
(cd "$repo" && CGO_ENABLED=0 go build -o "$work/mpv-gotorrent" ./cmd/mpv-gotorrent)

echo "==> making fixtures"
mkdir -p "$work/cache/Pack"
clip() { ffmpeg -loglevel error -y -f lavfi -i testsrc=size=160x120:rate=10:duration="$2" \
    -f lavfi -i sine=duration="$2" -c:v libx264 -c:a aac -shortest "$1"; }
clip "$work/cache/moviea.mkv" 8
clip "$work/cache/movieb.mkv" 8
for e in E1 E2 E10; do clip "$work/cache/Pack/$e.mkv" 4; done
pack_hash="$(python3 "$repo/test/smoke/make_fixtures.py" "$work")"

failed=0
check() { # check <name> <log> <grep -E pattern>...
    local name="$1" log="$2"; shift 2
    for pattern in "$@"; do
        if ! grep -Eq -- "$pattern" "$log"; then
            echo "FAIL $name: expected /$pattern/"; echo "---- log ----"; cat "$log"; echo "-------------"
            failed=1; return
        fi
    done
    echo "ok   $name"
}
run_mpv() { # run_mpv <log> <extra script-opts> <args>...
    local log="$1" extra="$2"; shift 2
    timeout 60 mpv --no-config --load-scripts=no --vo=null --ao=null --idle=no --msg-level=all=info \
        --script="$repo/scripts/main.lua" --script="$repo/test/smoke/probe.lua" \
        --script-opts="gotorrent-binary_path=$work/mpv-gotorrent,gotorrent-keep_files=yes,gotorrent-cache_dir=$work/cache,gotorrent-port=9700,gotorrent-keep_dir=$work/kept${extra}" \
        "$@" >"$log" 2>&1 || true
}

run_mpv "$work/pack.log" "" "$work/pack.torrent"
check "pack opens as a playlist in natural order" "$work/pack.log" \
    "PL E1.mkv \*" "PL E2.mkv$" "PL E10.mkv$"
# the three lines above must also come in this order
grep -E "^\[probe\] PL" "$work/pack.log" | sed 's/.*PL //; s/ \*//' | tr '\n' ' ' | grep -q "^E1.mkv E2.mkv E10.mkv $" \
    || { echo "FAIL pack order"; failed=1; }

run_mpv "$work/resume.log" "" "http://127.0.0.1:9999/torrent/$pack_hash/2/E2.mkv"
check "resume from history restores the pack around the episode" "$work/resume.log" \
    "Companion reconnected" "PL E1.mkv$" "PL E2.mkv \*" "PL E10.mkv$"
grep -E "^\[probe\] PL" "$work/resume.log" | sed 's/.*PL //; s/ \*//' | tr '\n' ' ' | grep -q "^E1.mkv E2.mkv E10.mkv $" \
    || { echo "FAIL resume order"; failed=1; }

DUMP_AFTER=12 run_mpv "$work/preload.log" ",gotorrent-prefetch_next_at=10" "$work/a.torrent" "$work/b.torrent"
check "next torrent is preloaded and adopted" "$work/preload.log" \
    "Preloading next torrent" "Playing: http://127.0.0.1:[0-9]+/torrent/[0-9a-f]+/0/movieb.mkv"
spawns="$(grep -c "Spawning companion" "$work/preload.log" || true)"
[ "$spawns" = 2 ] || { echo "FAIL preload: expected 2 companion spawns, got $spawns"; failed=1; }

PROBE_MODE=keep2 run_mpv "$work/keep.log" "" "$work/pack.torrent"
sleep 2
if [ -f "$work/kept/E1.mkv" ] && [ -f "$work/kept/E2.mkv" ]; then echo "ok   two episodes kept in one session"
else echo "FAIL keep: kept dir has: $(ls "$work/kept" 2>/dev/null)"; cat "$work/keep.log"; failed=1; fi

DUMP_AFTER=35 run_mpv "$work/dead.log" ",gotorrent-keep_files=no" "$work/dead.torrent"
check "dead torrent is reported" "$work/dead.log" "No peers connected after 30s"

exit "$failed"
