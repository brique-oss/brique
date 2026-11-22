import assert from "node:assert/strict";
import test from "node:test";
import zlib from "node:zlib";
import { iconIco, iconPng, shortcutDefinition } from "./shortcut.mjs";

test("the icon is a valid RGBA PNG of the requested size, opaque in the middle and transparent at the corner", () => {
  const png = iconPng(64);
  assert.deepEqual([...png.subarray(0, 8)], [0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]);
  assert.equal(png.readUInt32BE(16), 64);
  assert.equal(png.readUInt32BE(20), 64);
  const idat = png.indexOf("IDAT");
  const length = png.readUInt32BE(idat - 4);
  const raw = zlib.inflateSync(png.subarray(idat + 4, idat + 4 + length));
  assert.equal(raw.length, 64 * (64 * 4 + 1));
  const alphaAt = (x, y) => raw[y * (64 * 4 + 1) + 1 + x * 4 + 3];
  assert.equal(alphaAt(32, 32), 255);
  assert.equal(alphaAt(0, 0), 0);
});

test("the ico wraps the png", () => {
  const png = iconPng(32);
  const ico = iconIco(png);
  assert.equal(ico.readUInt16LE(2), 1);
  assert.equal(ico.readUInt32BE(22), png.readUInt32BE(0));
  assert.equal(ico.length, 22 + png.length);
});

test("macOS gets an app bundle that runs `ui` without a terminal", () => {
  const definition = shortcutDefinition({ platform: "darwin", binary: "/r/bin/brique-launcher", home: "/Users/a" });
  assert.equal(definition.path, "/Users/a/Applications/Brique Launcher.app");
  assert.equal(definition.files["Contents/MacOS/brique-launcher-app"], '#!/bin/sh\nexec "/r/bin/brique-launcher" ui\n');
  assert.match(definition.files["Contents/Info.plist"], /<key>CFBundleExecutable<\/key><string>brique-launcher-app<\/string>/);
  assert.deepEqual(definition.executable, ["Contents/MacOS/brique-launcher-app"]);
});

test("Linux gets a desktop entry without a terminal", () => {
  const definition = shortcutDefinition({ platform: "linux", binary: "/r/bin/brique-launcher", home: "/home/a" });
  assert.equal(definition.path, "/home/a/.local/share/applications/brique-launcher.desktop");
  assert.match(definition.files[""], /Exec="\/r\/bin\/brique-launcher" ui/);
  assert.match(definition.files[""], /Terminal=false/);
});

test("Windows gets a shortcut on the desktop", () => {
  const definition = shortcutDefinition({ platform: "win32", binary: "C:\\r\\bin\\brique-launcher.exe", home: "C:\\Users\\a", desktop: "C:\\Users\\a\\Desktop" });
  assert.equal(definition.kind, "windows-shortcut");
  assert.equal(definition.arguments, "ui");
});
