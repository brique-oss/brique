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

declare module "vscode" {
  export type Disposable = {
    dispose(): void;
  };

  export type Uri = {
    fsPath: string;
    toString(): string;
  };

  export const Uri: {
    file(fsPath: string): Uri;
  };

  export enum ViewColumn {
    One = 1,
  }

  export type Webview = {
    html: string;
    cspSource: string;
    asWebviewUri(uri: Uri): Uri;
    onDidReceiveMessage(callback: (message: unknown) => void): Disposable;
  };

  export type WebviewPanel = {
    webview: Webview;
    onDidDispose(callback: () => void): Disposable;
    dispose(): void;
  };

  export type ExtensionContext = {
    extensionUri: Uri;
    subscriptions: Disposable[];
  };

  export type OutputChannel = {
    appendLine(value: string): void;
    show(preserveFocus?: boolean): void;
    dispose(): void;
  };

  export namespace commands {
    export function registerCommand(
      command: string,
      callback: (...args: unknown[]) => unknown
    ): Disposable;

    export function executeCommand<T = unknown>(
      command: string,
      ...rest: unknown[]
    ): PromiseLike<T>;
  }

  export namespace window {
    export function createWebviewPanel(
      viewType: string,
      title: string,
      showOptions: ViewColumn | { viewColumn: ViewColumn; preserveFocus?: boolean },
      options: {
        enableScripts: boolean;
        localResourceRoots: Uri[];
        retainContextWhenHidden?: boolean;
      }
    ): WebviewPanel;

    export function showOpenDialog(options: {
      canSelectFiles: boolean;
      canSelectFolders: boolean;
      canSelectMany: boolean;
      filters?: Record<string, string[]>;
      title?: string;
    }): PromiseLike<Uri[] | undefined>;

    export function showQuickPick<T>(
      items: readonly T[],
      options?: { title?: string }
    ): PromiseLike<T | undefined>;

    export function showInformationMessage(message: string): PromiseLike<string | undefined>;
    export function showErrorMessage(message: string): PromiseLike<string | undefined>;
    export function showWarningMessage(message: string): PromiseLike<string | undefined>;
    export function createOutputChannel(name: string): OutputChannel;
  }

  export namespace workspace {
    export function findFiles(
      include: string,
      exclude?: string,
      maxResults?: number
    ): PromiseLike<Uri[]>;

    export function asRelativePath(uri: Uri): string;
  }

  export namespace env {
    export function openExternal(target: Uri): PromiseLike<boolean>;
  }
}
