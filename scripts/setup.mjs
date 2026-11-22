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

// One command after a clone: install the dependencies, build every binary (the launcher
// included, with its management page embedded) and the VS Code extension for this OS, install
// and print how to start the launcher. The setup is for a clone or a rebuild; day to day, only
// `npm run launcher` is needed. With --service the launcher is also installed as a login service
// (launchd, systemd user unit or scheduled task), so it is already running when the session opens.
//
//   node scripts/setup.mjs [--no-extension] [--no-runtime] [--no-shortcut] [--shortcut-only] [--service] [--no-open] [--dry-run] [--uninstall]
//
// --dry-run prints what would be done and the service definition, and changes nothing.

import { spawnSync } from "node:child_process";
import { existsSync, mkdirSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import os from "node:os";
import path from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";
import { installShortcut, removeShortcut } from "./shortcut.mjs";

export const SERVICE_NAME = "brique-launcher";
export const MAC_LABEL = "io.brique.launcher";
export const WINDOWS_TASK = "BriqueLauncher";

export function launcherBinary(rootDir, platform = process.platform) {
  return path.join(rootDir, "bin", platform === "win32" ? "brique-launcher.exe" : "brique-launcher");
}

function xml(value) {
  return value.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;");
}

// serviceDefinition describes how this OS starts the launcher supervisor at login: the file to
// write (if any) and the commands that install, restart and remove it. Pure: nothing is run.
export function serviceDefinition({ platform, binary, home, uid = 0 }) {
  if (platform === "darwin") {
    const file = path.join(home, "Library", "LaunchAgents", `${MAC_LABEL}.plist`);
    const log = path.join(home, "Library", "Logs", "brique-launcher.log");
    const content = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>${MAC_LABEL}</string>
  <key>ProgramArguments</key><array><string>${xml(binary)}</string><string>serve</string></array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><dict><key>SuccessfulExit</key><false/></dict>
  <key>StandardOutPath</key><string>${xml(log)}</string>
  <key>StandardErrorPath</key><string>${xml(log)}</string>
</dict>
</plist>
`;
    const domain = `gui/${uid}`;
    return {
      kind: "launchd",
      file,
      content,
      stop: [["launchctl", ["bootout", `${domain}/${MAC_LABEL}`]]],
      start: [["launchctl", ["bootstrap", domain, file]]],
      remove: [["launchctl", ["bootout", `${domain}/${MAC_LABEL}`]]],
    };
  }
  if (platform === "win32") {
    return {
      kind: "task-scheduler",
      file: null,
      content: `schtasks task ${WINDOWS_TASK}: "${binary}" serve, at logon`,
      stop: [["schtasks", ["/End", "/TN", WINDOWS_TASK]]],
      start: [
        ["schtasks", ["/Create", "/F", "/TN", WINDOWS_TASK, "/SC", "ONLOGON", "/TR", `"${binary}" serve`]],
        ["schtasks", ["/Run", "/TN", WINDOWS_TASK]],
      ],
      remove: [
        ["schtasks", ["/End", "/TN", WINDOWS_TASK]],
        ["schtasks", ["/Delete", "/F", "/TN", WINDOWS_TASK]],
      ],
    };
  }
  const file = path.join(home, ".config", "systemd", "user", `${SERVICE_NAME}.service`);
  const content = `[Unit]
Description=Brique launcher supervisor

[Service]
ExecStart="${binary}" serve
Restart=on-failure

[Install]
WantedBy=default.target
`;
  return {
    kind: "systemd-user",
    file,
    content,
    stop: [["systemctl", ["--user", "stop", SERVICE_NAME]]],
    start: [
      ["systemctl", ["--user", "daemon-reload"]],
      ["systemctl", ["--user", "enable", "--now", SERVICE_NAME]],
    ],
    remove: [
      ["systemctl", ["--user", "disable", "--now", SERVICE_NAME]],
    ],
  };
}

export const RUNTIME_DIRECTORY = "runtime";
export const RUNTIME_POINTER = "CURRENT";

// runtimeName names a new runtime package directory after the build time: a package is never
// overwritten (build:runtime refuses an existing directory), and instances keep the one they use.
export function runtimeName(date = new Date()) {
  return date.toISOString().replace(/[-:]/g, "").replace(/\..*$/, "Z").toLowerCase();
}

export function runtimePaths(rootDir, name) {
  const parent = path.join(rootDir, RUNTIME_DIRECTORY);
  return { parent, directory: path.join(parent, name), pointer: path.join(parent, RUNTIME_POINTER) };
}

export function currentRuntime(rootDir) {
  const { parent, pointer } = runtimePaths(rootDir, "");
  try {
    const name = readFileSync(pointer, "utf8").trim();
    return /^[0-9a-z]+$/.test(name) ? path.join(parent, name) : null;
  } catch {
    return null;
  }
}

export function parseArgs(args) {
  const options = { extension: true, runtime: true, shortcut: true, shortcutOnly: false, service: false, open: true, dryRun: false, uninstall: false };
  for (const arg of args) {
    if (arg === "--no-extension") options.extension = false;
    else if (arg === "--service") options.service = true;
    else if (arg === "--no-shortcut") options.shortcut = false;
    else if (arg === "--no-runtime") options.runtime = false;
    else if (arg === "--shortcut-only") options.shortcutOnly = true;
    else if (arg === "--no-open") options.open = false;
    else if (arg === "--dry-run") options.dryRun = true;
    else if (arg === "--uninstall") options.uninstall = true;
    else throw new Error(`Unknown option: ${arg}`);
  }
  return options;
}

function main() {
  const rootDir = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
  const options = parseArgs(process.argv.slice(2));
  const binary = launcherBinary(rootDir);
  const uid = typeof process.getuid === "function" ? process.getuid() : 0;
  const service = serviceDefinition({ platform: process.platform, binary, home: os.homedir(), uid });

  const run = (command, args, { allowFailure = false, cwd = rootDir } = {}) => {
    console.log(`$ ${command} ${args.join(" ")}`);
    if (options.dryRun) return 0;
    const result = spawnSync(command, args, { cwd, stdio: "inherit", shell: process.platform === "win32" });
    if (result.status !== 0 && !allowFailure) {
      console.error(`Failed: ${command} ${args.join(" ")}`);
      process.exit(result.status ?? 1);
    }
    return result.status ?? 1;
  };

  if (options.uninstall) {
    console.log("Removing the double-clickable shortcut.");
    if (!options.dryRun) removeShortcut({ binary });
    console.log(`Removing the ${service.kind} service of the launcher.`);
    for (const [command, args] of service.remove) run(command, args, { allowFailure: true });
    if (service.file && !options.dryRun) rmSync(service.file, { force: true });
    console.log("Done. Registered instances and the launcher data are untouched.");
    return;
  }

  if (options.shortcutOnly) {
    console.log("Creating the shortcut:", options.dryRun ? "(dry run)" : installShortcut({ binary }));
    return;
  }

  for (const tool of ["go", "node", "npm"]) {
    if (spawnSync(tool, ["version"], { stdio: "ignore", shell: process.platform === "win32" }).error) {
      console.error(`${tool} is required.`);
      process.exit(1);
    }
  }

  if (!existsSync(path.join(rootDir, "node_modules"))) run("npm", ["ci"]);

  // The launcher is built with the other binaries, for this OS, by build:brique (also part of the package).
  if (options.extension) {
    run("npm", ["run", "vscode:package"]);
    if (run("npm", ["run", "vscode:install"], { allowFailure: true }) !== 0) {
      console.warn("The VS Code extension could not be installed (is the 'code' command available?). Continuing.");
    }
  } else {
    run("npm", ["run", "build:brique"]);
  }

  if (options.shortcut) {
    console.log("Double-clickable launcher:", options.dryRun ? "(dry run)" : installShortcut({ binary }));
  }

  if (options.service) {
    console.log(`Installing the launcher service (${service.kind}).`);
    if (service.file) {
      console.log(`  ${service.file}`);
      if (options.dryRun) console.log(service.content);
      else {
        mkdirSync(path.dirname(service.file), { recursive: true });
        if (process.platform === "darwin") mkdirSync(path.join(os.homedir(), "Library", "Logs"), { recursive: true });
        writeFileSync(service.file, service.content);
      }
    }
    for (const [command, args] of service.stop) run(command, args, { allowFailure: true });
    for (const [command, args] of service.start) {
      if (run(command, args, { allowFailure: true }) !== 0) {
        console.warn("The service could not be started; `brique-launcher ui` starts the supervisor itself.");
      }
    }
  }

  // The runtime package: every binary and the web libraries in one verified directory. An instance that
  // serves a web page must run from such a package, so the launcher uses the current one by default.
  if (options.runtime) {
    const { parent, directory, pointer } = runtimePaths(rootDir, runtimeName());
    run("node", ["scripts/build-runtime.mjs", "--output", directory]);
    if (!options.dryRun) {
      mkdirSync(parent, { recursive: true });
      writeFileSync(pointer, `${path.basename(directory)}\n`);
    }
    console.log(`Runtime package: ${directory}`);
  }

  // A launcher that is already running keeps serving the page and the code it was built with.
  // Replace it by the new one, unless it manages running engines (stopping it would stop them).
  if (run(binary, ["shutdown", "--if-idle"], { allowFailure: true }) !== 0) {
    console.warn("\nThe running launcher was NOT restarted, so the page you see is still the previous version.\n" +
      "Stop the instances it manages (or run `brique-launcher shutdown`), then start it again.");
  }

  // The binary is ready. Start the supervisor and open the page now; later: `npm run launcher`.
  run(binary, options.open ? ["ui"] : ["ui", "--no-open"]);
  console.log("\nBookmark the page address (without the part after '#'): the browser keeps the token.");
  console.log("Next times, double-click the \"Brique Launcher\" shortcut (or run: npm run launcher).");
  if (!options.service) console.log("To have it start at login: npm run setup -- --service");
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) main();
