"""Tests for tools/fw_release.py (stdlib unittest).

Run from the repo root:  python -I -m unittest tools.test_fw_release
or                       python -I tools/test_fw_release.py
"""
import importlib.util
import json
import os
import struct
import sys
import tempfile
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
spec = importlib.util.spec_from_file_location("fw_release", os.path.join(HERE, "fw_release.py"))
fw = importlib.util.module_from_spec(spec)
spec.loader.exec_module(fw)

APP = "esp32_nat_router.bin"


def fake_app(version, project="esp32_nat_router", time="01:02:03", date="Oct 21 2026", filler=b"\xaa"):
    """An image with a valid esp_app_desc_t header and some payload."""
    d = bytearray(filler * 4096)
    struct.pack_into("<I", d, 32, 0xABCD5432)

    def put(off, size, s):
        b = s.encode()[: size - 1]
        d[off:off + size] = b + b"\0" * (size - len(b))

    put(48, 32, version)
    put(80, 32, project)
    put(112, 16, time)
    put(128, 16, date)
    return bytes(d)


def make_build(root, target, version, filler=b"\xaa"):
    """A fake ESP-IDF build directory for target with the four flashable parts."""
    d = os.path.join(root, f"build_{target}_{version}")
    os.makedirs(os.path.join(d, "bootloader"))
    os.makedirs(os.path.join(d, "partition_table"))
    with open(os.path.join(d, "project_description.json"), "w") as f:
        json.dump({"target": target, "project_name": "esp32_nat_router"}, f)
    with open(os.path.join(d, "bootloader", "bootloader.bin"), "wb") as f:
        f.write(b"BOOT" + target.encode())
    with open(os.path.join(d, "partition_table", "partition-table.bin"), "wb") as f:
        f.write(b"PART" + target.encode())
    with open(os.path.join(d, "ota_data_initial.bin"), "wb") as f:
        f.write(b"\xff" * 64)
    with open(os.path.join(d, APP), "wb") as f:
        f.write(fake_app(version, filler=filler))
    return d


class Args:
    """What argparse would hand cmd_add."""

    def __init__(self, build_dir, **kw):
        self.build_dir = build_dir
        self.title = kw.get("title", "")
        self.notes = kw.get("notes", "")
        self.status = kw.get("status", "")
        self.protocol = kw.get("protocol", "")
        self.commit = kw.get("commit", "")
        self.latest = kw.get("latest", False)
        self.force = kw.get("force", False)


class FwReleaseTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        root = self.tmp.name
        self.flasher = os.path.join(root, "flasher")
        self.fw_dir = os.path.join(self.flasher, "firmware")
        self.rel_dir = os.path.join(self.fw_dir, "releases")
        os.makedirs(self.rel_dir)
        self.saved = {k: getattr(fw, k) for k in ("FLASHER", "FW_DIR", "REL_DIR", "INDEX", "TOP_MANIFEST", "FLASHER_HTML")}
        fw.FLASHER = self.flasher
        fw.FW_DIR = self.fw_dir
        fw.REL_DIR = self.rel_dir
        fw.INDEX = os.path.join(self.fw_dir, "releases.json")
        fw.TOP_MANIFEST = os.path.join(self.flasher, "manifest.json")
        fw.FLASHER_HTML = os.path.join(self.flasher, "index.html")  # absent: set_latest skips it

    def tearDown(self):
        for k, v in self.saved.items():
            setattr(fw, k, v)
        self.tmp.cleanup()

    def index(self):
        with open(fw.INDEX, encoding="utf-8") as f:
            return json.load(f)

    def entry(self, version):
        return next(e for e in self.index()["releases"] if e["version"] == version)

    def test_detect_target_reads_project_description(self):
        b = make_build(self.tmp.name, "esp32c3", "2.4.22")
        self.assertEqual(fw.detect_target(b), "esp32c3")

    def test_add_s3_then_c3_same_version(self):
        fw.cmd_add(Args(make_build(self.tmp.name, "esp32s3", "2.4.22"), status="testing", title="two boards"))
        fw.cmd_add(Args(make_build(self.tmp.name, "esp32c3", "2.4.22", filler=b"\xbb")))

        rel = os.path.join(self.rel_dir, "2.4.22")
        for tgt in ("esp32s3", "esp32c3"):
            for name in ("bootloader.bin", "partition-table.bin", "ota_data_initial.bin", APP):
                self.assertTrue(os.path.exists(os.path.join(rel, tgt, name)), f"{tgt}/{name} missing")
        self.assertFalse(os.path.exists(os.path.join(rel, APP)), "no bins at the version root in the new layout")

        with open(os.path.join(rel, "manifest.json"), encoding="utf-8") as f:
            mf = json.load(f)
        fams = sorted(b["chipFamily"] for b in mf["builds"])
        self.assertEqual(fams, ["ESP32-C3", "ESP32-S3"])
        paths = {b["chipFamily"]: [p["path"] for p in b["parts"]] for b in mf["builds"]}
        self.assertEqual(paths["ESP32-C3"][-1], f"esp32c3/{APP}")
        self.assertEqual(paths["ESP32-S3"][-1], f"esp32s3/{APP}")
        offsets = [p["offset"] for p in mf["builds"][0]["parts"]]
        self.assertEqual(offsets, [0, 0x8000, 0xF000, 0x20000])

        e = self.entry("2.4.22")
        self.assertEqual(sorted(e["targets"]), ["esp32c3", "esp32s3"])
        s3, c3 = e["targets"]["esp32s3"], e["targets"]["esp32c3"]
        self.assertEqual(s3["url"], f"firmware/releases/2.4.22/esp32s3/{APP}")
        self.assertEqual(c3["url"], f"firmware/releases/2.4.22/esp32c3/{APP}")
        self.assertNotEqual(s3["sha256"], c3["sha256"])
        # legacy consumers (old dashboard, flasher list) still see the S3 image
        self.assertEqual(e["url"], s3["url"])
        self.assertEqual(e["sha256"], s3["sha256"])
        self.assertEqual(e["size"], s3["size"])
        # the second add kept the notes of the first
        self.assertEqual(e["title"], "two boards")
        self.assertEqual(e["status"], "testing")
        self.assertEqual(e["reports"], "2.4.22")

    def test_legacy_folder_indexed_as_s3(self):
        # pre-2.4.22 layout: the four bins sit at the version root
        legacy = os.path.join(self.rel_dir, "2.4.21-mux1.2")
        os.makedirs(legacy)
        for name in ("bootloader.bin", "partition-table.bin", "ota_data_initial.bin"):
            with open(os.path.join(legacy, name), "wb") as f:
                f.write(b"x")
        with open(os.path.join(legacy, APP), "wb") as f:
            f.write(fake_app("2.4.21-mux1.2"))
        fw.cmd_index(None)

        e = self.entry("2.4.21-mux1.2")
        self.assertEqual(list(e["targets"]), ["esp32s3"])
        self.assertEqual(e["targets"]["esp32s3"]["url"], f"firmware/releases/2.4.21-mux1.2/{APP}")
        self.assertEqual(e["url"], f"firmware/releases/2.4.21-mux1.2/{APP}")
        self.assertTrue(os.path.exists(os.path.join(legacy, APP)), "legacy files are never moved")
        with open(os.path.join(legacy, "manifest.json"), encoding="utf-8") as f:
            mf = json.load(f)
        self.assertEqual([b["chipFamily"] for b in mf["builds"]], ["ESP32-S3"])
        self.assertEqual(mf["builds"][0]["parts"][-1]["path"], APP)

    def test_latest_copies_both_targets(self):
        fw.cmd_add(Args(make_build(self.tmp.name, "esp32s3", "2.4.22")))
        fw.cmd_add(Args(make_build(self.tmp.name, "esp32c3", "2.4.22", filler=b"\xbb")))
        fw.set_latest("2.4.22")

        with open(os.path.join(self.fw_dir, APP), "rb") as f:
            self.assertEqual(f.read()[-1:], b"\xaa", "root bins are the S3 image (old OTA URL)")
        with open(os.path.join(self.fw_dir, "esp32c3", APP), "rb") as f:
            self.assertEqual(f.read()[-1:], b"\xbb")
        with open(fw.TOP_MANIFEST, encoding="utf-8") as f:
            mf = json.load(f)
        paths = {b["chipFamily"]: b["parts"][-1]["path"] for b in mf["builds"]}
        self.assertEqual(paths, {"ESP32-S3": f"firmware/{APP}", "ESP32-C3": f"firmware/esp32c3/{APP}"})
        self.assertEqual(mf["version"], "2.4.22")
        self.assertEqual(self.index()["latest"], "2.4.22")

    def test_latest_refuses_without_s3(self):
        fw.cmd_add(Args(make_build(self.tmp.name, "esp32c3", "2.4.22")))
        with self.assertRaises(SystemExit):
            fw.set_latest("2.4.22")

    def test_add_refuses_different_image_same_target(self):
        fw.cmd_add(Args(make_build(self.tmp.name, "esp32c3", "2.4.22")))
        other = make_build(os.path.join(self.tmp.name, "again"), "esp32c3", "2.4.22", filler=b"\xcc")
        with self.assertRaises(SystemExit):
            fw.cmd_add(Args(other))
        fw.cmd_add(Args(other, force=True))  # --force overwrites
        with open(os.path.join(self.rel_dir, "2.4.22", "esp32c3", APP), "rb") as f:
            self.assertEqual(f.read()[-1:], b"\xcc")

    def test_force_readd_refreshes_built_and_commit(self):
        fw.cmd_add(Args(make_build(self.tmp.name, "esp32c3", "2.4.22"), title="first", commit="aaaaaaa"))
        again = os.path.join(self.tmp.name, "again")
        b = make_build(again, "esp32c3", "2.4.22", filler=b"\xdd")
        # the rebuilt image carries a later build time
        with open(os.path.join(b, APP), "wb") as f:
            f.write(fake_app("2.4.22", time="09:08:07", date="Oct 22 2026", filler=b"\xdd"))
        fw.cmd_add(Args(b, force=True, commit="bbbbbbb"))
        e = self.entry("2.4.22")
        self.assertEqual(e["built"], "Oct 22 2026 09:08:07", "built must describe the archived image, not the first add")
        self.assertEqual(e["targets"]["esp32c3"]["built"], "Oct 22 2026 09:08:07")
        self.assertEqual(e["commit"], "bbbbbbb")
        self.assertEqual(e["title"], "first", "notes survive a re-add")

    def test_chip_suffixed_version_files_under_the_release(self):
        # firmware 2.4.22 reports 2.4.22-s3 / 2.4.22-c3 so the dashboard FW column
        # and the flasher say which chip an image is for; the release key is 2.4.22
        fw.cmd_add(Args(make_build(self.tmp.name, "esp32s3", "2.4.22-s3")))
        fw.cmd_add(Args(make_build(self.tmp.name, "esp32c3", "2.4.22-c3", filler=b"\xbb")))
        self.assertTrue(os.path.exists(os.path.join(self.rel_dir, "2.4.22", "esp32c3", APP)))
        self.assertFalse(os.path.exists(os.path.join(self.rel_dir, "2.4.22-c3")))
        e = self.entry("2.4.22")
        self.assertEqual(e["reports"], "2.4.22-s3", "legacy readers see the S3 string")
        self.assertEqual(e["targets"]["esp32s3"]["reports"], "2.4.22-s3")
        self.assertEqual(e["targets"]["esp32c3"]["reports"], "2.4.22-c3")
        self.assertEqual(fw.split_version("2.4.22-c6"), ("2.4.22", "esp32c6"))
        self.assertEqual(fw.split_version("2.4.21-mux1.4"), ("2.4.21-mux1.4", None))

    def test_chip_suffix_must_match_the_build_target(self):
        # an esp32s3 build directory holding an image that says -c3 is a mix-up
        b = make_build(self.tmp.name, "esp32s3", "2.4.22-c3")
        with self.assertRaises(SystemExit):
            fw.cmd_add(Args(b))
        self.assertFalse(os.path.exists(os.path.join(self.rel_dir, "2.4.22")))

    def test_add_unknown_target_refused(self):
        b = make_build(self.tmp.name, "esp32p4", "2.4.22")
        with self.assertRaises(SystemExit):
            fw.cmd_add(Args(b))


if __name__ == "__main__":
    unittest.main()
