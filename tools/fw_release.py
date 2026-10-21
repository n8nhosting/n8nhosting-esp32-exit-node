#!/usr/bin/env python3
"""Firmware release archive for the flasher (flash.<domain>).

Every release keeps its own folder so any board can be moved to any version,
older ones included (rollback). Since 2.4.22 one version holds one image per
board chip (target):

    flasher/firmware/releases/<version>/<target>/{bootloader,partition-table,
        ota_data_initial,esp32_nat_router}.bin      target = esp32s3 | esp32c3
    flasher/firmware/releases/<version>/manifest.json   ESP Web Tools, one build
                                                        per target present
    flasher/firmware/releases.json   index read by the flasher page and the
                                     admin dashboard ("Update FW" picker):
                                     "targets": {<target>: {url,size,sha256,built}}
                                     plus url/size/sha256 of the esp32s3 image
                                     for consumers that predate targets
    flasher/firmware/*.bin           the "latest" esp32s3 release (old OTA URL
                                     keeps working)
    flasher/firmware/esp32c3/*.bin   the "latest" esp32c3 release

Releases made before 2.4.22 keep their four bins at the version root; they are
read as esp32s3 and never moved.

Usage:
    tools/fw_release.py add <build_dir> [--title ".."] [--notes "..."] [--status testing] [--latest]
        Copy a build (e.g. firmware/build_s3n16r8 or firmware/build_c3mini)
        into releases/<version>/<target>/. The version is read from the app
        image itself (PROJECT_VER) and the target from the build directory's
        project_description.json, so a release can never be filed under the
        wrong name or chip. Run it once per build directory of a version.
    tools/fw_release.py latest <version>
        Point the top-level bins + flasher/manifest.json at an archived release
        (rolls back what newly flashed / "latest" OTA boards get). Needs the
        version's esp32s3 image.
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
# file name -> flash offset; the same for the S3 (16 MB) and C3 (4 MB) layouts,
# see firmware/partitions_16mb.csv and firmware/partitions_c3_4mb.csv
PARTS = [("bootloader.bin", 0), ("partition-table.bin", 0x8000),
         ("ota_data_initial.bin", 0xF000), (APP, 0x20000)]
# where each part lives inside an ESP-IDF build directory
BUILD_PATHS = {"bootloader.bin": ["bootloader/bootloader.bin", "bootloader.bin"],
               "partition-table.bin": ["partition_table/partition-table.bin", "partition-table.bin"],
               "ota_data_initial.bin": ["ota_data_initial.bin"],
               APP: [APP]}
VERSION_RE = re.compile(r"^[0-9A-Za-z][0-9A-Za-z._-]{0,31}$")

# IDF target -> ESP Web Tools chipFamily. Adding a board means adding a row.
CHIP_FAMILY = {"esp32s3": "ESP32-S3", "esp32c3": "ESP32-C3"}
LEGACY_TARGET = "esp32s3"   # releases before 2.4.22 (bins at the version root)
# where `latest` puts each target's bins under flasher/firmware/
LATEST_SUBDIR = {"esp32s3": "", "esp32c3": "esp32c3"}


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


def detect_target(build_dir):
    """The IDF target a build directory was configured for (project_description.json)."""
    p = os.path.join(build_dir, "project_description.json")
    try:
        with open(p, encoding="utf-8") as f:
            target = json.load(f).get("target", "")
    except (OSError, ValueError):
        sys.exit(f"{build_dir}: no readable project_description.json (is it an ESP-IDF build directory?)")
    if target not in CHIP_FAMILY:
        sys.exit(f"{build_dir}: target {target!r} is not a known board chip ({', '.join(CHIP_FAMILY)})")
    return target


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


def targets_in(version):
    """target -> relative dir of its bins inside releases/<version> ("" = legacy root)."""
    rel = os.path.join(REL_DIR, version)
    found = {}
    for t in CHIP_FAMILY:
        if os.path.exists(os.path.join(rel, t, APP)):
            found[t] = t
    if not found and os.path.exists(os.path.join(rel, APP)):
        found[LEGACY_TARGET] = ""
    return found


def release_manifest(version, prefix, targets):
    """ESP Web Tools manifest: one build per target, parts at prefix + <subdir>/.
    targets maps target -> subdir ("" for files right under prefix)."""
    builds = []
    for t in sorted(targets):
        sub = targets[t]
        base = prefix + (sub + "/" if sub else "")
        builds.append({"chipFamily": CHIP_FAMILY[t],
                       "parts": [{"path": base + name, "offset": off} for name, off in PARTS]})
    return {
        "name": "ESP32 Exit Node (NAT Router + rproxy)",
        "version": version,
        "new_install_prompt_erase": True,
        "builds": builds,
    }


def entry_for(version, old=None):
    old = old or {}
    targets = targets_in(version)
    if not targets:
        sys.exit(f"release {version} has no app image")
    tinfo = {}
    for t, sub in targets.items():
        app = os.path.join(REL_DIR, version, sub, APP) if sub else os.path.join(REL_DIR, version, APP)
        desc = app_desc(app)
        tinfo[t] = {
            "url": f"firmware/releases/{version}/{sub + '/' if sub else ''}{APP}",
            "size": os.path.getsize(app),
            "sha256": sha256(app),
            "built": f"{desc['date']} {desc['time']}",
            "reports": desc["version"],
        }
    # the entry's own url/size/sha256 describe the esp32s3 image when there is
    # one (every consumer before 2.4.22 assumed S3), else the first target
    main_t = LEGACY_TARGET if LEGACY_TARGET in tinfo else sorted(tinfo)[0]
    main = tinfo[main_t]
    e = {
        "version": version,
        # what the board reports in HELLO / the dashboard FW column
        "reports": main["reports"],
        "protocol": old.get("protocol") or ("mux1" if "mux" in version or version >= "2.4.22" else "pool"),
        "built": old.get("built") or main["built"],
        "added": old.get("added") or datetime.date.today().isoformat(),
        "size": main["size"],
        "sha256": main["sha256"],
        "url": main["url"],
        "manifest": f"firmware/releases/{version}/manifest.json",
        "targets": {t: {k: v for k, v in info.items() if k != "reports"} for t, info in sorted(tinfo.items())},
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


def write_release_manifest(version):
    save_json(os.path.join(REL_DIR, version, "manifest.json"),
              release_manifest(version, "", targets_in(version)))


def cmd_add(a):
    target = detect_target(a.build_dir)
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
    if targets_in(version).get(LEGACY_TARGET) == "" and target == LEGACY_TARGET and not a.force:
        sys.exit(f"release {version} exists in the pre-2.4.22 layout; bump PROJECT_VER (or --force)")
    dst = os.path.join(REL_DIR, version, target)
    if os.path.exists(os.path.join(dst, APP)) and not a.force:
        if sha256(os.path.join(dst, APP)) != sha256(src[APP]):
            sys.exit(f"release {version} already has a different {target} image; bump PROJECT_VER "
                     f"in firmware/CMakeLists.txt (or pass --force to overwrite)")
    os.makedirs(dst, exist_ok=True)
    for name, _ in PARTS:
        shutil.copyfile(src[name], os.path.join(dst, name))
    write_release_manifest(version)

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
    t = e["targets"][target]
    print(f"added {version} {target} ({t['size']} B, sha256 {t['sha256'][:12]}...)"
          f" targets: {', '.join(e['targets'])}")
    if a.latest:
        set_latest(version)


def set_latest(version):
    targets = targets_in(version)
    if LEGACY_TARGET not in targets:
        sys.exit(f"no archived {LEGACY_TARGET} image for {version}; run `add` with its build first")
    top = {}
    for t, sub in targets.items():
        rel = os.path.join(REL_DIR, version, sub) if sub else os.path.join(REL_DIR, version)
        out = os.path.join(FW_DIR, LATEST_SUBDIR[t]) if LATEST_SUBDIR[t] else FW_DIR
        os.makedirs(out, exist_ok=True)
        for name, _ in PARTS:
            shutil.copyfile(os.path.join(rel, name), os.path.join(out, name))
        top[t] = LATEST_SUBDIR[t]
    save_json(TOP_MANIFEST, release_manifest(version, "firmware/", top))
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
    print(f"latest -> {version} ({', '.join(sorted(targets))}; flasher/firmware/*.bin, flasher/manifest.json)")


def cmd_latest(a):
    set_latest(a.version)


def cmd_index(_a):
    idx = load_index()
    known = {e["version"]: e for e in idx["releases"]}
    rels = []
    for v in sorted(os.listdir(REL_DIR)):
        if targets_in(v):
            rels.append(entry_for(v, known.get(v)))
            write_release_manifest(v)
    idx["releases"] = rels
    write_index(idx)
    print(f"indexed {len(rels)} releases, latest = {idx.get('latest') or '(unset)'}")


def cmd_list(_a):
    idx = load_index()
    for e in sorted(idx["releases"], key=sort_key, reverse=True):
        tag = " (latest)" if e["version"] == idx.get("latest") else ""
        targets = "/".join(e.get("targets", {LEGACY_TARGET: None}))
        print(f"{e['version']:<20} {e['protocol']:<5} {targets:<15} {e['built']:<22} {e['size']:>8}  "
              f"{e.get('status') or 'stable':<7} {e.get('title') or e.get('notes', '')}{tag}")


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
