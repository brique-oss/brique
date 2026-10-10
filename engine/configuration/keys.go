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

package configuration

const (
	// context.json
	// context loop
	KeyContextName   = "ctx_name"
	KeyEngineVersion = "engine_version"
	KeyCxtExtName    = "ctx_ext_name"
	KeyEngineCfg     = "engine_config"
	KeyChildList     = "children_list"
	KeyCtxVersion    = "ctx_version"

	// comm loop
	KeyTrust                 = "trust"
	KeyScope                 = "scope"
	KeyInterfaces            = "interfaces"
	KeyIntName               = "name"
	KeyIntType               = "type"
	KeyIntDriver             = "driver"
	KeyIntConfig             = "config"
	KeyWrapBound             = "wrapper_boundary"
	KeyInstanceKeyName       = "instance_key_name"
	KeyIntHTTPMode           = "mode"
	KeyIntIngressEnabled     = "ingress_enabled"
	KeyIntEgressEnabled      = "egress_enabled"
	KeyIntAddr               = "addr"
	KeyIntPath               = "path"
	KeyIntRdLim              = "read_limit"
	KeyIntRqTO               = "request_timeout_ms"
	KeyIntWrTO               = "write_timeout_ms"
	KeyIntRdTO               = "read_timeout_ms"
	KeyIntIdleTO             = "idle_timeout_ms"
	KeyIntTargets            = "targets"
	KeyIntPingInter          = "ping_interval_ms"
	KeyIntAnyOri             = "allow_any_origin"
	KeyIntWS                 = "ws"
	KeyIntHTTP               = "http"
	KeyIntHTTPS              = "https"
	KeyTLSCertFile           = "tls_cert_file"
	KeyTLSKeyFile            = "tls_key_file"
	KeyTLSInsecureSkipVerify = "tls_insecure_skip_verify"
	KeyTLSServerName         = "tls_server_name"
	KeyScatter               = "scattered"
	KeyScatterMaxItems       = "scattered_max_items"
	KeyScatterAllowed        = "scattered_allowed"

	// matter loop
	KeyMatterInlineMaxBytes = "inline_max_bytes"
	KeyMatterMaxUploadBytes = "max_upload_bytes"
	KeyMatterLeaseTTLms     = "lease_ttl_ms"

	// execution loop
	KeyWrappers             = "wrappers"
	KeyWrpName              = "wrapper_name"
	KeyWrpMode              = "mode"
	KeyWrpReadyTO           = "ready_timeout_ms"
	KeyWrpStopTO            = "stop_timeout_ms"
	KeyWrpBuild             = "build"
	KeyWrpCWD               = "cwd"
	KeyWrpShell             = "shell"
	KeyWrpTO                = "timeout_ms"
	KeyWrpArti              = "artifact"
	KeyWrpArtiKind          = "artifact_kind"
	KeyWrpCmd               = "cmd"
	KeyWrpRun               = "run"
	KeyWrpEnv               = "env"
	KeyWrpEnvName           = "env_name"
	KeyExecDefaultTimeoutMs = "default_invoke_timeout_ms"
	KeyCtxRegistry          = "ctx_registry"
	KeyCtxCaps              = "caps"
	KeyCapKey               = "cap_key"
	KeyCapName              = "cap_name"
	KeyCapRelCtx            = "rel_ctx"
	KeyCapWrp               = "wrapper"
	KeyCapLang              = "lang"
	KeyCapKind              = "kind"

	// trace loop
	KeyTraceEnabled  = "enabled"
	KeyTraceLevel    = "level"
	KeyTraceInCap    = "inchan_capacity"
	KeyTraceRingCap  = "ring_capacity"
	KeyTraceFlushN   = "flush_every_n"
	KeyTraceFlushInt = "flush_every_interval"
	KeyTraceSegMax   = "segment_max_bytes"

	// reflexive loop
	KeyRootRepo   = "root_repo"
	KeyRepoName   = "repo_name"
	KeyDepth      = "depth"
	KeyMaxPerPath = "max_per_path"
)
