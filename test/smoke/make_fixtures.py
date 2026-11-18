"""Write the .torrent fixtures for the smoke test.

Usage: make_fixtures.py <dir>, where <dir>/cache already holds moviea.mkv, movieb.mkv and
Pack/E1.mkv, Pack/E2.mkv, Pack/E10.mkv. The pack's files are listed in byte order (E1, E10, E2),
which differs from the natural playlist order (E1, E2, E10) on purpose.
"""
import hashlib
import os
import sys

PIECE = 16384


def bencode(o):
    if isinstance(o, int):
        return b"i%de" % o
    if isinstance(o, str):
        o = o.encode()
    if isinstance(o, bytes):
        return b"%d:%s" % (len(o), o)
    if isinstance(o, list):
        return b"l" + b"".join(bencode(x) for x in o) + b"e"
    return b"d" + b"".join(bencode(k) + bencode(o[k]) for k in sorted(o)) + b"e"


def pieces(blob):
    return b"".join(hashlib.sha1(blob[i:i + PIECE]).digest() for i in range(0, len(blob), PIECE))


def write(path, info):
    with open(path, "wb") as f:
        f.write(bencode({"info": info}))
    return hashlib.sha1(bencode(info)).hexdigest()


root = sys.argv[1]
cache = os.path.join(root, "cache")
for name in ("moviea.mkv", "movieb.mkv"):
    data = open(os.path.join(cache, name), "rb").read()
    write(os.path.join(root, name[5] + ".torrent"),
          {"name": name, "length": len(data), "piece length": PIECE, "pieces": pieces(data)})

names = sorted(["E1.mkv", "E2.mkv", "E10.mkv"])
blobs = [open(os.path.join(cache, "Pack", n), "rb").read() for n in names]
print(write(os.path.join(root, "pack.torrent"), {
    "name": "Pack",
    "piece length": PIECE,
    "pieces": pieces(b"".join(blobs)),
    "files": [{"path": [n], "length": len(b)} for n, b in zip(names, blobs)],
}))

# Valid metadata whose data exists nowhere: a torrent with no peers
data = os.urandom(200000)
write(os.path.join(root, "dead.torrent"),
      {"name": "dead.mkv", "length": len(data), "piece length": PIECE, "pieces": pieces(data)})
