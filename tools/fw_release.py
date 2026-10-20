#!/usr/bin/env python3
"""Firmware release archive for the flasher (flash.<domain>).

Every release keeps its own folder so any board can be moved to any version,
older ones included (rollback):

    flasher/firmware/releases/<version>/{bootloader,partition-table,
        ota_data_initial,esp32_nat_router}.bin + manifest.json (ESP Web Tools)
    flasher/firmware/releases.json   index read by the flasher page and the
                                     admin dashboard ("Update FW" picker)
    flasher/firmware/*.bin           the "latest" release (old OTA URL keeps working)

Usage:
    tools/fw_release.py add <build_dir> [--title ".."] [--notes "..."] [--status testing] [--latest]
        Copy a build (e.g. firmware/build_s3n16r8 or firmware/prebuilt_s3n16r8)
        into releases/<version>/. The version is read from the app image itself
        (PROJECT_VER), so a release can never be filed under the wrong name.
    tools/fw_release.py latest <version>
        Point the top-level bins + flasher/manifest.json at an archived release
        (rolls back what newly flashed / "latest" OTA boards get).
    tools/fw_release.py index
        Rebuild releases.json from the folders (keeps existing notes).
    tools/fw_release.py list

Only the Python standard library is used.
"""
import argparse
import datetime
import hashlib
import json
import os
import re
import shutil
import struct
import sys

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
FLASHER = os.path.join(ROOT, "flasher")
FW_DIR = os.path.join(FLASHER, "firmware")
REL_DIR = os.path.join(FW_DIR, "releases")
INDEX = os.path.join(FW_DIR, "releases.json")
TOP_MANIFEST = os.path.join(FLASHER, "manifest.json")
FLASHER_HTML = os.path.join(FLASHER, "index.html")

APP = "esp32_nat_router.bin"
# file name -> flash offset (16 MB S3 layout, see firmware/partitions_16mb.csv)
PARTS = [("bootloader.bin", 0), ("partition-table.bin", 0x8000),
         ("ota_data_initial.bin", 0xF000), (APP, 0x20000)]
# where each part lives inside an ESP-IDF build directory
BUILD_PATHS = {"bootloader.bin": ["bootloader/bootloader.bin", "bootloader.bin"],
               "partition-table.bin": ["partition_table/partition-table.bin", "partition-table.bin"],
               "ota_data_initial.bin": ["ota_data_initial.bin"],
               APP: [APP]}
VERSION_RE = re.compile(r"^[0-9A-Za-z][0-9A-Za-z._-]{0,31}$")


def app_desc(path):
    """Read esp_app_desc_t from an app image: version, build date/time."""
    with open(path, "rb") as f:
        d = f.read(256)
    # 24 B image header + 8 B first segment header, then esp_app_desc_t
    magic = struct.unpack("<I", d[32:36])[0]
    if magic != 0xABCD5432:
        sys.exit(f"{path}: not an ESP-IDF app image (no app descriptor)")
    s = lambda a, b: d[a:b].split(b"\0")[0].decode(errors="replace")
    return {"version": s(48, 80), "project": s(80, 112), "time": s(112, 128), "date": s(128, 144)}


def sha256(path):
    h = hashlib.sha256()
    with open(path, "rb") as f:
        for chunk in iter(lambda: f.read(1 << 16), b""):
            h.update(chunk)
    return h.hexdigest()


def load_index():
    if os.path.exists(INDEX):
        with open(INDEX, encoding="utf-8") as f:
            return json.load(f)
    return {"latest": "", "releases": []}


def save_json(path, obj):
    tmp = path + ".tmp"
    with open(tmp, "w", encoding="utf-8") as f:
        json.dump(obj, f, indent=2, ensure_ascii=False)
        f.write("\n")
    os.replace(tmp, path)


def release_manifest(version, prefix):
    return {
        "name": "ESP32-S3 Exit Node (NAT Router + rproxy)",
        "version": version,
        "new_install_prompt_erase": True,
        "builds": [{"chipFamily": "ESP32-S3",
                    "parts": [{"path": prefix + name, "offset": off} for name, off in PARTS]}],
    }


def entry_for(version, old=None):
    old = old or {}
    app = os.path.join(REL_DIR, version, APP)
    desc = app_desc(app)
    e = {
        "version": version,
        # what the board reports in HELLO / the dashboard FW column
        "reports": desc["version"],
        "protocol": old.get("protocol") or ("mux1" if "mux" in version else "pool"),
        "built": old.get("built") or f"{desc['date']} {desc['time']}",
        "added": old.get("added") or datetime.date.today().isoformat(),
        "size": os.path.getsize(app),
        "sha256": sha256(app),
        "url": f"firmware/releases/{version}/{APP}",
        "manifest": f"firmware/releases/{version}/manifest.json",
        "title": old.get("title", ""),     # one-line headline (Thai) on the flasher page
        "notes": old.get("notes", ""),     # what changed, shown in the changelog + dashboard
        "status": old.get("status", ""),   # stable | testing | legacy ("" = stable)
    }
    if old.get("commit"):
        e["commit"] = old["commit"]
    if old.get("deprecated"):
        e["deprecated"] = old["deprecated"]
    return e


def sort_key(e):
    try:
        built = datetime.datetime.strptime(e.get("built", ""), "%b %d %Y %H:%M:%S").isoformat()
    except ValueError:
        built = ""
    return built, e.get("added", ""), e["version"]


def write_index(idx):
    idx["releases"].sort(key=sort_key, reverse=True)
    save_json(INDEX, idx)


def cmd_add(a):
    src = {}
    for name, _ in PARTS:
        for rel in BUILD_PATHS[name]:
            p = os.path.join(a.build_dir, rel)
            if os.path.exists(p):
                src[name] = p
                break
        else:
            sys.exit(f"missing {name} in {a.build_dir}")
    version = app_desc(src[APP])["version"]
    if not VERSION_RE.match(version):
        sys.exit(f"bad version string in image: {version!r}")
    dst = os.path.join(REL_DIR, version)
    if os.path.exists(os.path.join(dst, APP)) and not a.force:
        if sha256(os.path.join(dst, APP)) != sha256(src[APP]):
            sys.exit(f"release {version} already exists with a different image; bump PROJECT_VER "
                     f"in firmware/CMakeLists.txt (or pass --force to overwrite)")
    os.makedirs(dst, exist_ok=True)
    for name, _ in PARTS:
        shutil.copyfile(src[name], os.path.join(dst, name))
    save_json(os.path.join(dst, "manifest.json"), release_manifest(version, ""))

    idx = load_index()
    old = next((e for e in idx["releases"] if e["version"] == version), {})
    old = dict(old)
    for k in ("title", "notes", "status", "protocol", "commit"):
        v = getattr(a, k)
        if v:
            old[k] = v
    e = entry_for(version, old)
    idx["releases"] = [x for x in idx["releases"] if x["version"] != version] + [e]
    write_index(idx)
    print(f"added {version} ({e['size']} B, sha256 {e['sha256'][:12]}…)")
    if a.latest:
        set_latest(version)


def set_latest(version):
    rel = os.path.join(REL_DIR, version)
    if not os.path.exists(os.path.join(rel, APP)):
        sys.exit(f"no archived release {version}; run `add` first")
    for name, _ in PARTS:
        shutil.copyfile(os.path.join(rel, name), os.path.join(FW_DIR, name))
    save_json(TOP_MANIFEST, release_manifest(version, "firmware/"))
    if os.path.exists(FLASHER_HTML):
        with open(FLASHER_HTML, encoding="utf-8") as f:
            html = f.read()
        html2 = re.sub(r"NAT Router v[0-9A-Za-z._-]+", f"NAT Router v{version}", html, count=1)
        if html2 != html:
            with open(FLASHER_HTML, "w", encoding="utf-8") as f:
                f.write(html2)
    idx = load_index()
    idx["latest"] = version
    for e in idx["releases"]:
        if e["version"] == version and e.get("status") == "testing":
            e["status"] = "stable"
    write_index(idx)
    print(f"latest -> {version} (flasher/firmware/*.bin, flasher/manifest.json)")


def cmd_latest(a):
    set_latest(a.version)


def cmd_index(_a):
    idx = load_index()
    known = {e["version"]: e for e in idx["releases"]}
    rels = []
    for v in sorted(os.listdir(REL_DIR)):
        if os.path.exists(os.path.join(REL_DIR, v, APP)):
            rels.append(entry_for(v, known.get(v)))
            mf = os.path.join(REL_DIR, v, "manifest.json")
            if not os.path.exists(mf):
                save_json(mf, release_manifest(v, ""))
    idx["releases"] = rels
    write_index(idx)
    print(f"indexed {len(rels)} releases, latest = {idx.get('latest') or '(unset)'}")


def cmd_list(_a):
    idx = load_index()
    for e in sorted(idx["releases"], key=sort_key, reverse=True):
        tag = " (latest)" if e["version"] == idx.get("latest") else ""
        print(f"{e['version']:<20} {e['protocol']:<5} {e['built']:<22} {e['size']:>8}  {e.get('status') or 'stable':<7} {e.get('title') or e.get('notes', '')}{tag}")


def main():
    p = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    sub = p.add_subparsers(dest="cmd", required=True)
    s = sub.add_parser("add")
    s.add_argument("build_dir")
    s.add_argument("--title", default="", help="one-line headline for the flasher changelog")
    s.add_argument("--notes", default="")
    s.add_argument("--status", default="", choices=["", "stable", "testing", "legacy"])
    s.add_argument("--protocol", default="")
    s.add_argument("--commit", default="")
    s.add_argument("--latest", action="store_true", help="also make it the default firmware")
    s.add_argument("--force", action="store_true")
    s.set_defaults(fn=cmd_add)
    s = sub.add_parser("latest")
    s.add_argument("version")
    s.set_defaults(fn=cmd_latest)
    sub.add_parser("index").set_defaults(fn=cmd_index)
    sub.add_parser("list").set_defaults(fn=cmd_list)
    a = p.parse_args()
    a.fn(a)


if __name__ == "__main__":
    main()
