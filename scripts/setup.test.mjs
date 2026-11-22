import assert from "node:assert/strict";
import test from "node:test";
import { currentRuntime, launcherBinary, parseArgs, runtimeName, runtimePaths, serviceDefinition } from "./setup.mjs";
import { mkdtempSync, mkdirSync, writeFileSync } from "node:fs";
import os from "node:os";
import path from "node:path";

test("the launcher binary follows the platform", () => {
  assert.equal(launcherBinary("/r", "linux"), "/r/bin/brique-launcher");
  assert.match(launcherBinary("C:\\r", "win32"), /brique-launcher\.exe$/);
});

test("macOS uses a launchd agent that runs serve at load and keeps it alive", () => {
  const definition = serviceDefinition({ platform: "darwin", binary: "/r/bin/brique-launcher", home: "/Users/a", uid: 501 });
  assert.equal(definition.file, "/Users/a/Library/LaunchAgents/io.brique.launcher.plist");
  assert.match(definition.content, /<string>serve<\/string>/);
  assert.match(definition.content, /<key>RunAtLoad<\/key><true\/>/);
  // A deliberate `brique-launcher shutdown` exits cleanly and must not be undone by a restart.
  assert.match(definition.content, /<key>SuccessfulExit<\/key><false\/>/);
  assert.deepEqual(definition.start, [["launchctl", ["bootstrap", "gui/501", definition.file]]]);
});

test("Linux uses a systemd user unit", () => {
  const definition = serviceDefinition({ platform: "linux", binary: "/r/bin/brique-launcher", home: "/home/a" });
  assert.equal(definition.file, "/home/a/.config/systemd/user/brique-launcher.service");
  assert.match(definition.content, /ExecStart="\/r\/bin\/brique-launcher" serve/);
  assert.deepEqual(definition.start[1], ["systemctl", ["--user", "enable", "--now", "brique-launcher"]]);
});

test("Windows uses a logon task", () => {
  const definition = serviceDefinition({ platform: "win32", binary: "C:\\r\\bin\\brique-launcher.exe", home: "C:\\Users\\a" });
  assert.equal(definition.file, null);
  assert.equal(definition.start[0][1][0], "/Create");
});

test("paths with XML characters are escaped in the plist", () => {
  const definition = serviceDefinition({ platform: "darwin", binary: "/r&d/<x>/brique-launcher", home: "/h", uid: 1 });
  assert.match(definition.content, /\/r&amp;d\/&lt;x&gt;\/brique-launcher/);
});

test("options are strict", () => {
  assert.deepEqual(parseArgs(["--dry-run"]), { extension: true, runtime: true, shortcut: true, shortcutOnly: false, service: false, open: true, dryRun: true, uninstall: false });
  assert.throws(() => parseArgs(["--bogus"]));
});

test("the login service is opt-in", () => {
  assert.equal(parseArgs([]).service, false);
  assert.equal(parseArgs(["--service"]).service, true);
});

test("a runtime package is named after the build time and the pointer finds the current one", () => {
  assert.match(runtimeName(new Date("2026-11-22T10:20:30.456Z")), /^20261122t102030z$/);
  const root = mkdtempSync(path.join(os.tmpdir(), "brique-runtime-test-"));
  assert.equal(currentRuntime(root), null);
  const { parent, pointer } = runtimePaths(root, "x");
  mkdirSync(parent, { recursive: true });
  writeFileSync(pointer, "20261122t102030z\n");
  assert.equal(currentRuntime(root), path.join(parent, "20261122t102030z"));
  writeFileSync(pointer, "../escape\n");
  assert.equal(currentRuntime(root), null);
});
