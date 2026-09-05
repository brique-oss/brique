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

import { access } from "node:fs/promises";
import { dirname, resolve as resolvePath } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

export async function resolve(specifier, context, defaultResolve) {
  // Redirect .js imports to .ts for TypeScript source files
  if (isRelative(specifier) && specifier.endsWith(".js")) {
    const tsSpecifier = specifier.slice(0, -3) + ".ts";
    const parentPath = fileURLToPath(context.parentURL);
    const candidate = resolvePath(dirname(parentPath), tsSpecifier);
    if (await exists(candidate)) {
      return { shortCircuit: true, url: pathToFileURL(candidate).href };
    }
  }

  try {
    return await defaultResolve(specifier, context, defaultResolve);
  } catch (err) {
    if (
      !["ERR_MODULE_NOT_FOUND", "ERR_UNSUPPORTED_DIR_IMPORT"].includes(err?.code) ||
      !isRelative(specifier)
    ) {
      throw err;
    }

    const parentPath = fileURLToPath(context.parentURL);
    const basePath = resolvePath(dirname(parentPath), specifier);

    for (const candidate of [
      `${basePath}.ts`,
      `${basePath}.tsx`,
      `${basePath}/index.ts`,
      `${basePath}/index.tsx`,
    ]) {
      if (await exists(candidate)) {
        return { shortCircuit: true, url: pathToFileURL(candidate).href };
      }
    }

    throw err;
  }
}

function isRelative(specifier) {
  return specifier.startsWith("./") || specifier.startsWith("../");
}

async function exists(path) {
  try {
    await access(path);
    return true;
  } catch {
    return false;
  }
}
