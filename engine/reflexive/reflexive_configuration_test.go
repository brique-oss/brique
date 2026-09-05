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
