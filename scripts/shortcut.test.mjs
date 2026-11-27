import assert from "node:assert/strict";
import test from "node:test";
import zlib from "node:zlib";
import { readFileSync } from "node:fs";
import { FAVICON, iconIco, iconPng, parseIconSvg, shortcutDefinition } from "./shortcut.mjs";

const SQUARE = '<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 120 120"><title>t</title><rect x="20" y="20" width="80" height="80" rx="10" fill="#2f5bd8"/></svg>';

function decode(png) {
  const size = png.readUInt32BE(16);
  const idat = png.indexOf("IDAT");
  const length = png.readUInt32BE(idat - 4);
  const raw = zlib.inflateSync(png.subarray(idat + 4, idat + 4 + length));
  assert.equal(raw.length, size * (size * 4 + 1));
  return { size, pixel: (x, y) => [...raw.subarray(y * (size * 4 + 1) + 1 + x * 4, y * (size * 4 + 1) + 1 + x * 4 + 4)] };
}

test("the icon is a valid RGBA PNG of the requested size, painted from the svg and transparent outside it", () => {
  const png = iconPng(64, SQUARE);
  assert.deepEqual([...png.subarray(0, 8)], [0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]);
  assert.equal(png.readUInt32BE(16), 64);
  assert.equal(png.readUInt32BE(20), 64);
  const { pixel } = decode(png);
  assert.deepEqual(pixel(32, 32), [47, 91, 216, 255]);
  assert.equal(pixel(2, 2)[3], 0);
});

test("a rectangular path, its stroke and its dashes are drawn", () => {
  const svg = '<svg viewBox="0 0 100 100"><path d="M10 10L90 10L90 90L10 90Z" fill="none" stroke="#000000" stroke-width="4" stroke-dasharray="10 10"/></svg>';
  const { pixel } = decode(iconPng(100, svg));
  assert.equal(pixel(50, 50)[3], 0);
  assert.equal(pixel(15, 10)[3], 255);
  assert.equal(pixel(25, 10)[3], 0);
});

test("the favicon of the launcher is the icon, and it parses", () => {
  const { shapes } = parseIconSvg(readFileSync(FAVICON, "utf8"));
  assert.ok(shapes.length > 0);
  assert.equal(decode(iconPng(32)).size, 32);
});

test("an unsupported svg element is refused instead of being silently dropped", () => {
  assert.throws(() => iconPng(16, '<svg viewBox="0 0 10 10"><circle cx="5" cy="5" r="4"/></svg>'), /not supported/);
  assert.throws(() => iconPng(16, '<svg viewBox="0 0 10 10"><path d="M0 0L10 0L5 10Z"/></svg>'), /rectangular/);
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
