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

package execution

import (
	"testing"

	"brique_engine/configuration"
)

func TestExecutionConfig_N1_EXCFG_01_ParseNilDefaults(t *testing.T) {
	cfg := ParseExecutionCfg(nil)
	if cfg.Wrappers != nil {
		t.Fatalf("wrappers should be nil by default")
	}
	if cfg.CtxRegistry.Caps == nil {
		t.Fatalf("CtxRegistry.Caps should be initialized")
	}
	if len(cfg.CtxRegistry.Caps) != 0 {
		t.Fatalf("CtxRegistry.Caps should be empty by default")
	}
}

func TestExecutionConfig_N1_EXCFG_02_ParseWithoutWrappersKey(t *testing.T) {
	cfg := ParseExecutionCfg(map[string]any{"x": 1})
	if cfg.Wrappers != nil {
		t.Fatalf("wrappers should remain nil when key missing")
	}
	if cfg.CtxRegistry.Caps == nil {
		t.Fatalf("CtxRegistry.Caps should be initialized")
	}
}

func TestExecutionConfig_N1_EXCFG_03_ParseValidWrapperBuildRun(t *testing.T) {
	in := map[string]any{
		configuration.KeyWrappers: []any{
			map[string]any{
				configuration.KeyWrpName:    "py",
				configuration.KeyWrpMode:    "interpreted",
				configuration.KeyWrpReadyTO: float64(1500),
				configuration.KeyWrpStopTO:  int64(900),
				configuration.KeyWrpBuild: map[string]any{
					configuration.KeyWrpShell:    true,
					configuration.KeyWrpTO:       float64(2200),
					configuration.KeyWrpArti:     "bin/app",
					configuration.KeyWrpArtiKind: "file",
					configuration.KeyWrpCmd:      []any{"go", "build"},
				},
				configuration.KeyWrpRun: map[string]any{
					configuration.KeyWrpShell: false,
					configuration.KeyWrpCmd:   []any{"python", "main.py"},
					configuration.KeyWrpEnv: map[string]any{
						"A": "1",
						"B": "2",
					},
				},
			},
		},
	}

	cfg := ParseExecutionCfg(in)
	if len(cfg.Wrappers) != 1 {
		t.Fatalf("wrappers len=%d want 1", len(cfg.Wrappers))
	}
	w := cfg.Wrappers[0]
	if w.Name != "py" || w.Mode != "interpreted" {
		t.Fatalf("unexpected wrapper identity: %#v", w)
	}
	if w.ReadyTimeoutMs != 1500 || w.StopTimeoutMs != 900 {
		t.Fatalf("unexpected timeout values: ready=%d stop=%d", w.ReadyTimeoutMs, w.StopTimeoutMs)
	}
	if w.Build == nil {
		t.Fatalf("build cfg should be set")
	}
	if !w.Build.Shell || w.Build.TimeoutMs != 2200 || w.Build.Artifact != "bin/app" || w.Build.ArtifactKind != "file" {
		t.Fatalf("unexpected build cfg: %#v", w.Build)
	}
	if len(w.Build.Cmd) != 2 || w.Build.Cmd[0] != "go" || w.Build.Cmd[1] != "build" {
		t.Fatalf("unexpected build cmd: %#v", w.Build.Cmd)
	}
	if w.Run.Shell {
		t.Fatalf("unexpected run cfg basics: %#v", w.Run)
	}
	if len(w.Run.Cmd) != 2 || w.Run.Cmd[0] != "python" || w.Run.Cmd[1] != "main.py" {
		t.Fatalf("unexpected run cmd: %#v", w.Run.Cmd)
	}
	if len(w.Run.Env) != 2 || w.Run.Env["A"] != "1" || w.Run.Env["B"] != "2" {
		t.Fatalf("unexpected run env: %#v", w.Run.Env)
	}
}

func TestExecutionConfig_N1_EXCFG_04_ParseSkipsMalformedWrappers(t *testing.T) {
	in := map[string]any{
		configuration.KeyWrappers: []any{
			"bad",
			map[string]any{configuration.KeyWrpName: "ok"},
			nil,
		},
	}
	cfg := ParseExecutionCfg(in)
	if len(cfg.Wrappers) != 1 {
		t.Fatalf("wrappers len=%d want 1", len(cfg.Wrappers))
	}
	if cfg.Wrappers[0].Name != "ok" {
		t.Fatalf("expected kept valid wrapper entry, got %#v", cfg.Wrappers[0])
	}
}

func TestExecutionConfig_N1_EXCFG_05_ParseCommandFiltersNonStrings(t *testing.T) {
	in := map[string]any{
		configuration.KeyWrappers: []any{
			map[string]any{
				configuration.KeyWrpName: "w1",
				configuration.KeyWrpBuild: map[string]any{
					configuration.KeyWrpCmd: []any{"go", 12, "test", true},
				},
				configuration.KeyWrpRun: map[string]any{
					configuration.KeyWrpCmd: []any{"python", nil, "app.py"},
				},
			},
		},
	}
	cfg := ParseExecutionCfg(in)
	w := cfg.Wrappers[0]
	if len(w.Build.Cmd) != 2 || w.Build.Cmd[0] != "go" || w.Build.Cmd[1] != "test" {
		t.Fatalf("unexpected filtered build cmd: %#v", w.Build.Cmd)
	}
	if len(w.Run.Cmd) != 2 || w.Run.Cmd[0] != "python" || w.Run.Cmd[1] != "app.py" {
		t.Fatalf("unexpected filtered run cmd: %#v", w.Run.Cmd)
	}
}

func TestExecutionConfig_N1_EXCFG_06_ParseEnvKeepsOnlyStringValues(t *testing.T) {
	in := map[string]any{
		configuration.KeyWrappers: []any{
			map[string]any{
				configuration.KeyWrpName: "w1",
				configuration.KeyWrpRun: map[string]any{
					configuration.KeyWrpEnv: map[string]any{
						"A": "x",
						"B": 1,
						"C": true,
						"D": "y",
					},
				},
			},
		},
	}
	cfg := ParseExecutionCfg(in)
	w := cfg.Wrappers[0]
	if len(w.Run.Env) != 2 {
		t.Fatalf("expected 2 string env values, got %#v", w.Run.Env)
	}
	if w.Run.Env["A"] != "x" || w.Run.Env["D"] != "y" {
		t.Fatalf("unexpected env map: %#v", w.Run.Env)
	}
	if _, ok := w.Run.Env["B"]; ok {
		t.Fatalf("non-string env value should be dropped")
	}
}
