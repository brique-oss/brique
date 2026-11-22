/*
 * Copyright 2026 Nicolas Cassan
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */


// A double-clickable launcher for the Brique launcher, per OS: an application bundle on macOS, a
// .desktop entry on Linux, a shortcut on Windows. Double-clicking runs `brique-launcher ui`, which
// starts the supervisor if needed (detached) and opens the management page in the browser.

import { spawnSync } from "node:child_process";
import { chmodSync, mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import os from "node:os";
import path from "node:path";
import zlib from "node:zlib";

export const APP_NAME = "Brique Launcher";

function crc32(buffer) {
  let crc = 0xffffffff;
  for (const byte of buffer) {
    crc ^= byte;
    for (let bit = 0; bit < 8; bit += 1) crc = (crc >>> 1) ^ (0xedb88320 & -(crc & 1));
  }
  return (crc ^ 0xffffffff) >>> 0;
}

function chunk(type, data) {
  const head = Buffer.alloc(8);
  head.writeUInt32BE(data.length, 0);
  head.write(type, 4, "ascii");
  const crc = Buffer.alloc(4);
  crc.writeUInt32BE(crc32(Buffer.concat([head.subarray(4), data])), 0);
  return Buffer.concat([head, data, crc]);
}

// iconPng draws the icon: white bricks laid in a running bond on the Brique blue, in a rounded square, as a size x size RGBA PNG.
// The wall is an aggregation: every brick is a participant, and the three whites (full, soft, very soft) are different ones laid in the same masonry.
// The drawing is made in a 120 x 120 space and smoothed by sampling each pixel 4 x 4 times.
export function iconPng(size = 256) {
  const BLUE = [47, 91, 216];
  const FULL = 1, SOFT = 0.62, FAINT = 0.38;
  const rows = [
    [24, [[18, 38, FULL], [60, 42, SOFT]]],
    [44, [[18, 20, SOFT], [42, 38, FULL], [84, 18, FAINT]]],
    [64, [[18, 38, FAINT], [60, 42, FULL]]],
    [84, [[18, 20, FULL], [42, 38, SOFT], [84, 18, FULL]]],
  ];
  const bricks = rows.flatMap(([top, row]) => row.map(([left, width, opacity]) => ({ left, top, right: left + width, bottom: top + 16, opacity })));
  const inRounded = (x, y, left, top, right, bottom, radius) => {
    if (x < left || x >= right || y < top || y >= bottom) return false;
    const cx = Math.min(Math.max(x, left + radius), right - radius);
    const cy = Math.min(Math.max(y, top + radius), bottom - radius);
    return (x - cx) ** 2 + (y - cy) ** 2 <= radius ** 2;
  };
  const samples = 4;
  const pixels = Buffer.alloc(size * size * 4);
  for (let y = 0; y < size; y += 1) {
    for (let x = 0; x < size; x += 1) {
      let covered = 0, red = 0, green = 0, blue = 0;
      for (let sy = 0; sy < samples; sy += 1) {
        for (let sx = 0; sx < samples; sx += 1) {
          const u = ((x + (sx + 0.5) / samples) / size) * 120, v = ((y + (sy + 0.5) / samples) / size) * 120;
          if (!inRounded(u, v, 0, 0, 120, 120, 27)) continue;
          let color = BLUE;
          for (const brick of bricks) {
            if (inRounded(u, v, brick.left, brick.top, brick.right, brick.bottom, 4)) {
              color = BLUE.map((channel) => channel + (255 - channel) * brick.opacity);
              break;
            }
          }
          covered += 1; red += color[0]; green += color[1]; blue += color[2];
        }
      }
      const at = (y * size + x) * 4;
      if (covered === 0) continue;
      pixels[at] = Math.round(red / covered); pixels[at + 1] = Math.round(green / covered); pixels[at + 2] = Math.round(blue / covered);
      pixels[at + 3] = Math.round((covered / (samples * samples)) * 255);
    }
  }
  const raw = Buffer.alloc(size * (size * 4 + 1));
  for (let y = 0; y < size; y += 1) {
    raw[y * (size * 4 + 1)] = 0;
    pixels.copy(raw, y * (size * 4 + 1) + 1, y * size * 4, (y + 1) * size * 4);
  }
  const header = Buffer.alloc(13);
  header.writeUInt32BE(size, 0);
  header.writeUInt32BE(size, 4);
  header[8] = 8; header[9] = 6; // 8 bits, RGBA
  return Buffer.concat([Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]),
    chunk("IHDR", header), chunk("IDAT", zlib.deflateSync(raw)), chunk("IEND", Buffer.alloc(0))]);
}

// iconIco wraps a 256x256 PNG in an ICO container (accepted by Windows since Vista).
export function iconIco(png) {
  const header = Buffer.alloc(22);
  header.writeUInt16LE(0, 0); header.writeUInt16LE(1, 2); header.writeUInt16LE(1, 4);
  header[6] = 0; header[7] = 0; // 0 means 256
  header.writeUInt16LE(1, 10); header.writeUInt16LE(32, 12);
  header.writeUInt32LE(png.length, 14); header.writeUInt32LE(22, 18);
  return Buffer.concat([header, png]);
}

function xml(value) {
  return value.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;");
}

// shortcutDefinition says where the shortcut goes and what it contains. Pure: nothing is written.
export function shortcutDefinition({ platform, binary, home, desktop = path.join(home, "Desktop") }) {
  if (platform === "darwin") {
    const app = path.join(home, "Applications", `${APP_NAME}.app`);
    return {
      kind: "app-bundle",
      path: app,
      files: {
        "Contents/Info.plist": `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>CFBundleName</key><string>${APP_NAME}</string>
  <key>CFBundleDisplayName</key><string>${APP_NAME}</string>
  <key>CFBundleIdentifier</key><string>io.brique.launcher.app</string>
  <key>CFBundleExecutable</key><string>brique-launcher-app</string>
  <key>CFBundleIconFile</key><string>icon</string>
  <key>CFBundlePackageType</key><string>APPL</string>
  <key>CFBundleVersion</key><string>1</string>
  <key>LSUIElement</key><true/>
</dict>
</plist>
`,
        "Contents/MacOS/brique-launcher-app": `#!/bin/sh\nexec "${binary.replace(/"/g, '\\"')}" ui\n`,
      },
      executable: ["Contents/MacOS/brique-launcher-app"],
    };
  }
  if (platform === "win32") {
    return { kind: "windows-shortcut", path: path.join(desktop, `${APP_NAME}.lnk`), target: binary, arguments: "ui" };
  }
  const entry = path.join(home, ".local", "share", "applications", "brique-launcher.desktop");
  return {
    kind: "desktop-entry",
    path: entry,
    files: {
      "": `[Desktop Entry]
Type=Application
Name=${APP_NAME}
Comment=Manage Brique instances
Exec="${binary}" ui
Icon=${path.join(home, ".local", "share", "icons", "brique-launcher.png")}
Terminal=false
Categories=Development;
`,
    },
    executable: [""],
  };
}

function runTool(command, args) {
  return spawnSync(command, args, { stdio: "ignore" }).status === 0;
}

// installShortcut writes the shortcut. It returns a short description of what was created.
export function installShortcut({ platform = process.platform, binary, home = os.homedir() }) {
  const definition = shortcutDefinition({ platform, binary, home });
  const png = iconPng(256);
  if (definition.kind === "app-bundle") {
    rmSync(definition.path, { recursive: true, force: true });
    for (const [relative, content] of Object.entries(definition.files)) {
      const full = path.join(definition.path, relative);
      mkdirSync(path.dirname(full), { recursive: true });
      writeFileSync(full, content);
    }
    for (const relative of definition.executable) chmodSync(path.join(definition.path, relative), 0o755);
    makeIcns(png, path.join(definition.path, "Contents", "Resources", "icon.icns"));
    return `${definition.path} (drag it to the Dock if you like)`;
  }
  if (definition.kind === "desktop-entry") {
    const icon = path.join(home, ".local", "share", "icons", "brique-launcher.png");
    mkdirSync(path.dirname(icon), { recursive: true });
    writeFileSync(icon, png);
    mkdirSync(path.dirname(definition.path), { recursive: true });
    writeFileSync(definition.path, definition.files[""]);
    chmodSync(definition.path, 0o755);
    return definition.path;
  }
  const icon = path.join(path.dirname(binary), "brique-launcher.ico");
  writeFileSync(icon, iconIco(png));
  const script = `$s=(New-Object -ComObject WScript.Shell).CreateShortcut('${definition.path.replace(/'/g, "''")}');` +
    `$s.TargetPath='${definition.target.replace(/'/g, "''")}';$s.Arguments='${definition.arguments}';` +
    `$s.IconLocation='${icon.replace(/'/g, "''")}';$s.WindowStyle=7;$s.Save()`;
  if (!runTool("powershell", ["-NoProfile", "-Command", script])) throw new Error("could not create the shortcut");
  return definition.path;
}

export function removeShortcut({ platform = process.platform, binary, home = os.homedir() }) {
  const definition = shortcutDefinition({ platform, binary, home });
  rmSync(definition.path, { recursive: true, force: true });
  if (definition.kind === "desktop-entry") rmSync(path.join(home, ".local", "share", "icons", "brique-launcher.png"), { force: true });
}

// makeIcns builds a macOS icon from the PNG with the system tools; without them the app keeps the default icon.
function makeIcns(png, destination) {
  const work = mkdtempSync(path.join(os.tmpdir(), "brique-icon-"));
  try {
    const source = path.join(work, "icon.png");
    const iconset = path.join(work, "icon.iconset");
    writeFileSync(source, png);
    mkdirSync(iconset);
    for (const size of [16, 32, 128, 256]) {
      for (const scale of [1, 2]) {
        const pixels = size * scale;
        const name = scale === 1 ? `icon_${size}x${size}.png` : `icon_${size}x${size}@2x.png`;
        if (pixels === 256) writeFileSync(path.join(iconset, name), png);
        else if (!runTool("sips", ["-z", String(pixels), String(pixels), source, "--out", path.join(iconset, name)])) return false;
      }
    }
    mkdirSync(path.dirname(destination), { recursive: true });
    return runTool("iconutil", ["-c", "icns", iconset, "-o", destination]);
  } finally {
    rmSync(work, { recursive: true, force: true });
  }
}
