# Copyright 2026 Nicolas Cassan
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

from __future__ import annotations

import asyncio
import sys

from wrapper.runtime import WrapperRuntime


async def _amain() -> int:
    runtime = WrapperRuntime()
    return await runtime.run()


def _is_expected_boundary_connect_failure(exc: Exception) -> bool:
    if isinstance(exc, OSError) and getattr(exc, "errno", None) in {61, 111}:
        return True
    text = str(exc)
    return "Connect call failed" in text or "connection refused" in text.lower()


def main() -> int:
    try:
        return asyncio.run(_amain())
    except KeyboardInterrupt:
        return 0
    except Exception as exc:  # pragma: no cover - startup/teardown race path
        if _is_expected_boundary_connect_failure(exc):
            return 0
        sys.stderr.write(f"sandbox_py wrapper fatal error: {exc}\n")
        sys.stderr.flush()
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
