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

package reflexive

import (
	"testing"

	"brique_engine/configuration"
)

func TestParseReflexiveCfg_N0_RCFG_01_NilConfig(t *testing.T) {
	cfg := ParseReflexiveCfg(nil)
	if cfg.RootRepo || cfg.RepoName != "" {
		t.Fatalf("nil config should return zero-value cfg, got %#v", cfg)
	}
}

func TestParseReflexiveCfg_N0_RCFG_02_ValidConfig(t *testing.T) {
	cfg := ParseReflexiveCfg(map[string]any{
		configuration.KeyRootRepo: true,
		configuration.KeyRepoName: "brique-repo",
	})
	if !cfg.RootRepo || cfg.RepoName != "brique-repo" {
		t.Fatalf("valid config parse mismatch, got %#v", cfg)
	}
}

func TestParseReflexiveCfg_N0_RCFG_03_InvalidTypesAndTrim(t *testing.T) {
	cfg := ParseReflexiveCfg(map[string]any{
		configuration.KeyRootRepo: "true",
		configuration.KeyRepoName: "  my-repo  ",
	})
	if cfg.RootRepo {
		t.Fatalf("invalid bool type should be ignored, got %#v", cfg)
	}
	if cfg.RepoName != "my-repo" {
		t.Fatalf("repo name should be trimmed, got %q", cfg.RepoName)
	}
}

func TestParseReflexiveCfg_Limits(t *testing.T) {
	cfg := ParseReflexiveCfg(map[string]any{"max_handlers": 256.0, "inbox_capacity": 128.0, "projection_max_conns": 4.0})
	if cfg.MaxHandlers != 256 || cfg.InboxCapacity != 128 || cfg.ProjectionConns != 4 {
		t.Fatalf("limits not parsed: %#v", cfg)
	}
	for _, bad := range []map[string]any{nil, {"max_handlers": 0.0, "inbox_capacity": -4.0}, {"max_handlers": "256"}} {
		cfg := ParseReflexiveCfg(bad)
		if cfg.MaxHandlers != 0 || cfg.InboxCapacity != 0 {
			t.Fatalf("invalid limits must be ignored, got %#v for %#v", cfg, bad)
		}
	}
}

func TestNewReflexiveLoop_LimitsApply(t *testing.T) {
	set := NewReflexiveLoop(nil, map[string]any{"max_handlers": 256.0, "inbox_capacity": 128.0})
	if got := cap(set.handlerSlots); got != 256 {
		t.Fatalf("handler slots = %d, want 256", got)
	}
	if got := cap(set.deliveryIn); got != 128 {
		t.Fatalf("inbox = %d, want 128", got)
	}
	def := NewReflexiveLoop(nil, nil)
	if cap(def.handlerSlots) != defaultMaxHandlers || cap(def.deliveryIn) != defaultInboxCapacity {
		t.Fatalf("defaults changed: slots=%d inbox=%d", cap(def.handlerSlots), cap(def.deliveryIn))
	}
}
