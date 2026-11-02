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

'use strict';

const { app, BrowserWindow, dialog, ipcMain } = require('electron');
const path = require('path');
const { pathToFileURL } = require('url');

async function main() {
  await app.whenReady();

  const transportUrl = process.env.SPARK_UI_TRANSPORT_URL;
  if (!transportUrl) throw new Error('Missing SPARK_UI_TRANSPORT_URL');
  const relayModuleUrl = pathToFileURL(
    path.resolve(__dirname, '..', 'LocalWebSocketRelay.js')
  ).href;
  const { LocalWebSocketRelay } = await import(relayModuleUrl);
  const relay = new LocalWebSocketRelay(transportUrl);

  ipcMain.handle('spark:showOpenDialog', async (_event, options = {}) => {
    const result = await dialog.showOpenDialog(options);
    return {
      canceled: result.canceled,
      filePaths: result.filePaths,
    };
  });

  const win = new BrowserWindow({
    width: 1200,
    height: 800,
    webPreferences: {
      nodeIntegration: true,
      contextIsolation: false,
    },
  });

  const unsubscribeMessage = relay.subscribe((data) => {
    if (!win.isDestroyed()) win.webContents.send('spark:transport:message', data);
  });
  const unsubscribeState = relay.subscribeToConnectionState((state) => {
    if (!win.isDestroyed()) win.webContents.send('spark:transport:state', state);
  });
  ipcMain.on('spark:transport:open', () => relay.open());
  ipcMain.on('spark:transport:close', () => relay.close());
  ipcMain.handle('spark:transport:send', async (_event, data) => {
    if (typeof data !== 'string') throw new Error('Invalid Spark transport payload');
    await relay.send(data);
  });

  win.webContents.on('console-message', (_event, level, message, line, sourceId) => {
    const prefix = ['[renderer:verbose]', '[renderer:info]', '[renderer:warn]', '[renderer:error]'][level] ?? '[renderer]';
    console.log(`${prefix} ${message} (${sourceId}:${line})`);
  });

  win.webContents.on('did-fail-load', (_event, errorCode, errorDescription, validatedURL) => {
    console.error(`[renderer:load-failed] ${errorCode} ${errorDescription} url=${validatedURL}`);
  });

  const rendererPath =
    process.env.SPARK_RENDERER_ENTRYPOINT ||
    path.resolve('dist/spark/hosts/electron/renderer/index.html');

  await win.loadFile(rendererPath);

  win.on('closed', () => {
    unsubscribeMessage();
    unsubscribeState();
    relay.close();
    ipcMain.removeHandler('spark:transport:send');
    app.quit();
  });
}

main().catch((err) => {
  console.error('[electron-main:error]', err);
  app.quit();
});

app.on('window-all-closed', () => {
  app.quit();
});
