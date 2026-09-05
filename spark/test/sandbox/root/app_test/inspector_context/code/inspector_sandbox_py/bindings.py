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

from app import InspectorSandboxService


def build_bindings(runtime):
    service = InspectorSandboxService()

    return {
        "capacities": {
            ("", "echo"): service.echo,
            ("", "delayed_echo"): service.delayed_echo,
            ("", "error_test"): service.error_test,
        },
        "matters": {},
        "shutdown": [],
    }
