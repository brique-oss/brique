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
import { chmodSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";
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

// FAVICON is the single source of the launcher's icon: the browser tab, the macOS application, the Linux entry and the Windows shortcut.
export const FAVICON = fileURLToPath(new URL("../launcher/web_ui_static/favicon.svg", import.meta.url));

function svgColor(value) {
  if (value === undefined || value === "none") return null;
  const named = { white: "#ffffff", black: "#000000" };
  let hex = (named[value.toLowerCase()] || value).replace(/^#/, "");
  if (hex.length === 3) hex = [...hex].map((digit) => digit + digit).join("");
  if (!/^[0-9a-fA-F]{6}$/.test(hex)) throw new Error(`favicon: unsupported color "${value}"`);
  return [0, 2, 4].map((at) => parseInt(hex.slice(at, at + 2), 16));
}

// parseIconSvg reads the subset of SVG the icon is drawn with: a viewBox, then <rect> elements and <path> elements that trace an
// axis-aligned rectangle (M x y L x y L x y L x y Z), each with fill, fill-opacity, stroke, stroke-width, stroke-opacity and
// stroke-dasharray. Anything else is refused, so the icons never silently differ from the favicon.
export function parseIconSvg(text) {
  const attributes = (source) => Object.fromEntries([...source.matchAll(/([\w:-]+)="([^"]*)"/g)].map((match) => [match[1], match[2]]));
  const root = text.match(/<svg\b([^>]*)>/);
  if (!root) throw new Error("favicon: no <svg> element");
  const viewBox = (attributes(root[1]).viewBox || "").trim().split(/[\s,]+/).map(Number);
  if (viewBox.length !== 4 || viewBox.some(Number.isNaN) || viewBox[2] <= 0 || viewBox[3] <= 0) throw new Error("favicon: the <svg> needs a viewBox");
  const body = text.slice(root.index + root[0].length).replace(/<title>[\s\S]*?<\/title>/g, "").replace(/<\/svg>\s*$/, "");
  const shapes = [];
  for (const element of body.matchAll(/<\s*([\w:-]+)\b([^>]*?)\/?>|([^<\s][^<]*)/g)) {
    if (element[3]) throw new Error("favicon: unexpected text in the drawing");
    const [, tag, source] = element;
    const a = attributes(source);
    let left, top, width, height, radius = Number(a.rx || a.ry || 0);
    if (tag === "rect") {
      [left, top, width, height] = [a.x || 0, a.y || 0, a.width, a.height].map(Number);
    } else if (tag === "path") {
      const match = (a.d || "").trim().match(/^M\s*(\S+?)[\s,]+(\S+?)\s*L\s*(\S+?)[\s,]+(\S+?)\s*L\s*(\S+?)[\s,]+(\S+?)\s*L\s*(\S+?)[\s,]+(\S+?)\s*Z$/i);
      if (!match) throw new Error(`favicon: only rectangular paths are supported, not "${a.d}"`);
      const points = [0, 2, 4, 6].map((at) => [Number(match[at + 1]), Number(match[at + 2])]);
      const xs = new Set(points.map((point) => point[0])), ys = new Set(points.map((point) => point[1]));
      const axisAligned = points.every((point, at) => { const next = points[(at + 1) % 4]; return point[0] === next[0] || point[1] === next[1]; });
      if (xs.size !== 2 || ys.size !== 2 || !axisAligned) throw new Error(`favicon: the path "${a.d}" is not an axis-aligned rectangle`);
      left = Math.min(...xs); top = Math.min(...ys); width = Math.max(...xs) - left; height = Math.max(...ys) - top;
      radius = 0;
    } else {
      throw new Error(`favicon: the <${tag}> element is not supported (use <rect> or rectangular <path>)`);
    }
    if ([left, top, width, height, radius].some(Number.isNaN)) throw new Error(`favicon: a <${tag}> has a non-numeric geometry`);
    const dash = (a["stroke-dasharray"] || "none") === "none" ? null : a["stroke-dasharray"].trim().split(/[\s,]+/).map(Number);
    shapes.push({
      left, top, right: left + width, bottom: top + height, radius: Math.min(radius, width / 2, height / 2),
      fill: a.fill === undefined ? [0, 0, 0] : svgColor(a.fill), fillOpacity: Number(a["fill-opacity"] ?? a.opacity ?? 1),
      stroke: svgColor(a.stroke), strokeWidth: Number(a["stroke-width"] ?? 1), strokeOpacity: Number(a["stroke-opacity"] ?? a.opacity ?? 1),
      dash: dash && dash.length && dash.every((part) => part >= 0) && dash.some((part) => part > 0) ? (dash.length % 2 ? [...dash, ...dash] : dash) : null,
    });
  }
  return { viewBox, shapes };
}

// iconPng draws the favicon as a size x size RGBA PNG. Each pixel is sampled 4 x 4 times and the shapes are painted in order.
export function iconPng(size = 256, svg = readFileSync(FAVICON, "utf8")) {
  const { viewBox: [minX, minY, viewWidth, viewHeight], shapes } = parseIconSvg(svg);
  const scale = Math.max(viewWidth, viewHeight);
  const offsetX = minX - (scale - viewWidth) / 2, offsetY = minY - (scale - viewHeight) / 2;
  // distance is the signed distance from (x, y) to the outline of a rounded rectangle: negative inside.
  const distance = (x, y, shape) => {
    const halfWidth = (shape.right - shape.left) / 2, halfHeight = (shape.bottom - shape.top) / 2;
    const qx = Math.abs(x - (shape.left + halfWidth)) - (halfWidth - shape.radius);
    const qy = Math.abs(y - (shape.top + halfHeight)) - (halfHeight - shape.radius);
    return Math.hypot(Math.max(qx, 0), Math.max(qy, 0)) + Math.min(Math.max(qx, qy), 0) - shape.radius;
  };
  // dashOn tells whether the outline is drawn at the point of the perimeter nearest to (x, y), walking clockwise from the top-left corner.
  const dashOn = (x, y, shape) => {
    if (!shape.dash) return true;
    const width = shape.right - shape.left, height = shape.bottom - shape.top;
    const gaps = [y - shape.top, shape.right - x, shape.bottom - y, x - shape.left].map(Math.abs);
    const side = gaps.indexOf(Math.min(...gaps));
    let along = [x - shape.left, width + (y - shape.top), width + height + (shape.right - x), 2 * width + height + (shape.bottom - y)][side];
    along %= shape.dash.reduce((total, part) => total + part, 0);
    for (let at = 0; at < shape.dash.length; at += 1) {
      if (along < shape.dash[at]) return at % 2 === 0;
      along -= shape.dash[at];
    }
    return true;
  };
  const samples = 4;
  const pixels = Buffer.alloc(size * size * 4);
  for (let y = 0; y < size; y += 1) {
    for (let x = 0; x < size; x += 1) {
      let red = 0, green = 0, blue = 0, alpha = 0;
      for (let sy = 0; sy < samples; sy += 1) {
        for (let sx = 0; sx < samples; sx += 1) {
          const u = offsetX + ((x + (sx + 0.5) / samples) / size) * scale, v = offsetY + ((y + (sy + 0.5) / samples) / size) * scale;
          let r = 0, g = 0, b = 0, a = 0; // premultiplied
          const paint = (color, opacity) => { r = color[0] * opacity + r * (1 - opacity); g = color[1] * opacity + g * (1 - opacity); b = color[2] * opacity + b * (1 - opacity); a = opacity + a * (1 - opacity); };
          for (const shape of shapes) {
            const d = distance(u, v, shape);
            if (shape.fill && d <= 0) paint(shape.fill, shape.fillOpacity);
            if (shape.stroke && Math.abs(d) <= shape.strokeWidth / 2 && dashOn(u, v, shape)) paint(shape.stroke, shape.strokeOpacity);
          }
          red += r; green += g; blue += b; alpha += a;
        }
      }
      if (alpha === 0) continue;
      const at = (y * size + x) * 4;
      pixels[at] = Math.round(red / alpha); pixels[at + 1] = Math.round(green / alpha); pixels[at + 2] = Math.round(blue / alpha);
      pixels[at + 3] = Math.round((alpha / (samples * samples)) * 255);
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
    // macOS caches the icon of a bundle by path: re-registering it makes Finder show the new one (the Dock may need `killall Dock`).
    runTool("/System/Library/Frameworks/CoreServices.framework/Frameworks/LaunchServices.framework/Support/lsregister", ["-f", definition.path]);
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
