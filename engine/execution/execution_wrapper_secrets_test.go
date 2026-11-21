//go:build !windows

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
	"crypto/ed25519"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"brique_engine/circulation"
	"brique_engine/configuration"
	"brique_engine/security/trust"
	"brique_engine/shared"
)

const secretTestValue = "sk-test-0123456789-not-a-real-key"

// secretsCommReg is the registry of the execution tests with the two things a launch asks of
// the real one: the current trust runtime and the machine's directory.
type secretsCommReg struct {
	*mockExecCommReg
	runtime *trust.Runtime
	machine string
}

func (r secretsCommReg) TrustRuntime() *trust.Runtime { return r.runtime }
func (r secretsCommReg) MachineDirectory() string     { return r.machine }

// activeSecretsRuntime builds a trust runtime with a signed policy that grants these secrets and
// nothing else, the way the engine adopts one.
func activeSecretsRuntime(t *testing.T, secrets []trust.WrapperSecret) *trust.Runtime {
	t.Helper()
	authorityPublic, authorityPrivate, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	authorityID, err := trust.SigningKeyID(authorityPublic)
	if err != nil {
		t.Fatal(err)
	}
	policy := trust.Policy{
		Format: trust.PolicyFormat, FormatVersion: trust.PolicyFormatVersionV2,
		PolicyID: "main", InstanceID: "execution-instance", Revision: "1", IssuedAt: "2026-10-24T12:00:00Z",
		Principals: []trust.Principal{}, Scopes: []trust.Scope{}, WrapperSecrets: secrets,
	}
	policyJSON, err := json.Marshal(policy)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := trust.CanonicalPolicy(policyJSON)
	if err != nil {
		t.Fatal(err)
	}
	proof, err := trust.SignPolicy(canonical, authorityPrivate)
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := json.Marshal(trust.Envelope{
		Policy: canonical,
		Proofs: []trust.Proof{{AuthorityKeyID: authorityID, Algorithm: trust.SigningAlgorithm, Signature: proof}},
	})
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := trust.VerifyAndDecode(envelope, trust.Authority{KeyID: authorityID, PublicKey: authorityPublic})
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := trust.NewRuntime("execution-instance", "main")
	if err != nil {
		t.Fatal(err)
	}
	catalog := trust.Catalog{Capabilities: map[string][]string{}, ResourceActions: map[string][]string{}}
	candidate, err := runtime.Prepare(decoded, catalog)
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.Commit(candidate, func(trust.ActivationRecord) error { return nil }); err != nil {
		t.Fatal(err)
	}
	return runtime
}

// secretWrapperCfg is a wrapper that writes the variable it was given into a file, then waits.
func secretWrapperCfg(readyMs int, env map[string]any, needs []any) map[string]any {
	return map[string]any{
		configuration.KeyWrappers: []any{
			map[string]any{
				configuration.KeyWrpName:    "w1",
				configuration.KeyWrpMode:    configuration.ValueConfigStyleInterpreted,
				configuration.KeyWrpReadyTO: readyMs,
				configuration.KeyWrpStopTO:  200,
				configuration.KeyWrpRun: map[string]any{
					configuration.KeyWrpCmd:     []any{"sh", "-c", `printf %s "${MYKEY-unset}" > "$OUT"; sleep 5`},
					configuration.KeyWrpShell:   false,
					configuration.KeyWrpEnv:     env,
					configuration.KeyWrpSecrets: needs,
				},
			},
		},
	}
}

func need(name, env string) map[string]any {
	return map[string]any{configuration.KeyWrpSecretName: name, configuration.KeyWrpSecretEnv: env}
}

func grantW1(secrets ...string) []trust.WrapperSecret {
	out := make([]trust.WrapperSecret, 0, len(secrets))
	for _, name := range secrets {
		out = append(out, trust.WrapperSecret{Name: name, Wrappers: []string{"w1"}})
	}
	return out
}

func storeSecret(t *testing.T, machine, name, value string) {
	t.Helper()
	if err := shared.WriteProtectedFile(machine, shared.SecretsDir+"/"+name, []byte(value), 0o600); err != nil {
		t.Fatal(err)
	}
}

// secretsLoop builds a loop whose wrapper writes MYKEY into a file. policy nil means no policy at all.
func secretsLoop(t *testing.T, readyMs int, needs []any, policy *trust.Runtime) (l *ExecutionLoop, commCh chan circulation.Message, machine, out string) {
	t.Helper()
	machine = t.TempDir()
	out = filepath.Join(t.TempDir(), "out")
	l, commCh, _ = newProcTestLoop(t, secretWrapperCfg(readyMs, map[string]any{"OUT": out}, needs))
	l.frame.CtxCommReg = secretsCommReg{mockExecCommReg: l.frame.CtxCommReg.(*mockExecCommReg), runtime: policy, machine: machine}
	return l, commCh, machine, out
}

func readWhenWritten(t *testing.T, path string) string {
	t.Helper()
	var got string
	waitProc(t, 5*time.Second, func() bool {
		data, err := os.ReadFile(path)
		got = string(data)
		return err == nil && got != ""
	}, "the wrapper to write what it was given")
	return got
}

func assertNoValue(t *testing.T, label string, err error) {
	t.Helper()
	if err != nil && strings.Contains(err.Error(), secretTestValue) {
		t.Fatalf("%s: the error carries the value: %v", label, err)
	}
}

func TestWrapperSecrets_AGrantedWrapperGetsTheValueInsteadOfAnInheritedOne(t *testing.T) {
	t.Setenv("MYKEY", "inherited-value")
	l, _, machine, out := secretsLoop(t, 0, []any{need("anthropic", "MYKEY")}, activeSecretsRuntime(t, grantW1("anthropic")))
	storeSecret(t, machine, "anthropic", secretTestValue)
	spawnTracked(t, l, true)
	if got := readWhenWritten(t, out); got != secretTestValue {
		t.Fatalf("the wrapper was given %q, want the secret", got)
	}
}

// A wrapper that declares no secret is launched as it always was: it inherits the environment of
// the engine. The mechanism adds to what a wrapper is given, it does not scrub what it inherits.
func TestWrapperSecrets_AWrapperWithoutDeclarationIsLaunchedAsBefore(t *testing.T) {
	t.Setenv("MYKEY", "inherited-value")
	l, _, machine, out := secretsLoop(t, 0, nil, activeSecretsRuntime(t, grantW1("anthropic")))
	storeSecret(t, machine, "anthropic", secretTestValue)
	spawnTracked(t, l, true)
	if got := readWhenWritten(t, out); got != "inherited-value" {
		t.Fatalf("an undeclared wrapper saw %q, want what it inherits", got)
	}
}

// The right alone gives nothing: a granted secret that the wrapper did not declare is not given.
func TestWrapperSecrets_ARightWithoutADeclarationGivesNothing(t *testing.T) {
	l, _, machine, out := secretsLoop(t, 0, nil, activeSecretsRuntime(t, grantW1("anthropic")))
	storeSecret(t, machine, "anthropic", secretTestValue)
	spawnTracked(t, l, true)
	if got := readWhenWritten(t, out); got != "unset" {
		t.Fatalf("the wrapper saw %q, want nothing", got)
	}
}

func TestWrapperSecrets_WhatIsNotGivenDoesNotStartTheWrapper(t *testing.T) {
	granted := activeSecretsRuntime(t, grantW1("anthropic"))
	otherWrapper := activeSecretsRuntime(t, []trust.WrapperSecret{{Name: "anthropic", Wrappers: []string{"someone_else"}}})
	otherSecret := activeSecretsRuntime(t, grantW1("mistral"))
	for _, c := range []struct {
		label     string
		policy    *trust.Runtime
		stored    bool
		mustBeSay string
	}{
		{"no policy at all", nil, true, "no security policy is active"},
		{"granted to another wrapper", otherWrapper, true, "not granted to this wrapper"},
		{"another secret granted", otherSecret, true, "not granted to this wrapper"},
		{"no value on the machine", granted, false, "brique-sec secret add anthropic"},
	} {
		t.Run(c.label, func(t *testing.T) {
			l, _, machine, out := secretsLoop(t, 0, []any{need("anthropic", "MYKEY")}, c.policy)
			if c.stored {
				storeSecret(t, machine, "anthropic", secretTestValue)
			}
			cfg, _ := l.getWrapperCfg("w1")
			pid, err := l.startWrapperProcess(cfg)
			if err == nil || pid != 0 {
				t.Fatalf("the wrapper started: pid=%d err=%v", pid, err)
			}
			if !isWrapperSecretError(err) {
				t.Fatalf("the refusal is not marked as a secret failure: %v", err)
			}
			if !strings.Contains(err.Error(), "secret anthropic") || !strings.Contains(err.Error(), "wrapper w1") || !strings.Contains(err.Error(), c.mustBeSay) {
				t.Fatalf("the refusal must name the secret, the wrapper and what to do: %v", err)
			}
			assertNoValue(t, c.label, err)
			time.Sleep(100 * time.Millisecond)
			if _, statErr := os.Stat(out); statErr == nil {
				t.Fatal("a process ran although the secret was refused")
			}
		})
	}
}

// A value stored with the wrong mode is not read: the same rule as for the keys.
func TestWrapperSecrets_AValueReadableByOthersIsNotGiven(t *testing.T) {
	l, _, machine, _ := secretsLoop(t, 0, []any{need("anthropic", "MYKEY")}, activeSecretsRuntime(t, grantW1("anthropic")))
	storeSecret(t, machine, "anthropic", secretTestValue)
	if err := os.Chmod(filepath.Join(machine, shared.SecretsDir, "anthropic"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, _ := l.getWrapperCfg("w1")
	_, err := l.startWrapperProcess(cfg)
	if err == nil || !isWrapperSecretError(err) {
		t.Fatalf("a secret readable by others was given: %v", err)
	}
	assertNoValue(t, "mode 0644", err)
}

func TestWrapperSecrets_ADeclarationThatCannotBeUsedIsRefused(t *testing.T) {
	for label, needs := range map[string][]any{
		"a bad secret name":              {need("Bad Name", "MYKEY")},
		"a path as a name":               {need("../a", "MYKEY")},
		"an empty name":                  {need("", "MYKEY")},
		"a bad variable name":            {need("anthropic", "lower")},
		"an empty variable":              {need("anthropic", "")},
		"a variable of the engine":       {need("anthropic", "BRIQUE_WS_URL")},
		"a variable set in run.env":      {need("anthropic", "OUT")},
		"the same variable twice":        {need("anthropic", "MYKEY"), need("mistral", "MYKEY")},
		"an entry that is not an object": {"anthropic"},
	} {
		t.Run(label, func(t *testing.T) {
			policy := activeSecretsRuntime(t, grantW1("anthropic", "mistral"))
			l, _, machine, _ := secretsLoop(t, 0, needs, policy)
			storeSecret(t, machine, "anthropic", secretTestValue)
			storeSecret(t, machine, "mistral", secretTestValue)
			cfg, _ := l.getWrapperCfg("w1")
			pid, err := l.startWrapperProcess(cfg)
			if err == nil || pid != 0 || !isWrapperSecretError(err) {
				t.Fatalf("a declaration that cannot be used started the wrapper: pid=%d err=%v", pid, err)
			}
			assertNoValue(t, label, err)
		})
	}
}

func TestWrapperSecrets_SeveralSecretsAreEachGivenInTheirOwnVariable(t *testing.T) {
	l, _, machine, out := secretsLoop(t, 0, []any{need("anthropic", "MYKEY"), need("mistral", "OTHERKEY")}, activeSecretsRuntime(t, grantW1("anthropic", "mistral")))
	storeSecret(t, machine, "anthropic", secretTestValue)
	storeSecret(t, machine, "mistral", "another-value")
	cfg, _ := l.getWrapperCfg("w1")
	got, err := l.wrapperSecretEnv(cfg)
	if err != nil || got["MYKEY"] != secretTestValue || got["OTHERKEY"] != "another-value" || len(got) != 2 {
		t.Fatalf("env = %v, %v", got, err)
	}
	_ = out
}

// A failure that comes from a secret is not remembered: once the value is stored, the next call
// starts the wrapper. Without this, a wrapper called before `secret add` would stay refused until
// its context is restarted.
func TestWrapperSecrets_AFailedSecretIsRetriedAndNotRemembered(t *testing.T) {
	l, commCh, machine, out := secretsLoop(t, 300, []any{need("anthropic", "MYKEY")}, activeSecretsRuntime(t, grantW1("anthropic")))

	first := circulation.Intention{IntentionID: "i-secret-1", Correlation: &circulation.Correlation{}}
	if abort := l.ensureWrapperReady("w1", first); !abort {
		t.Fatal("the wrapper started without its secret")
	}
	refused := recvExecMsg(t, commCh, "the refusal for the missing value")
	if refused.Response.Error == nil || refused.Response.Error.Code != circulation.ValueCodeRefused ||
		!strings.Contains(refused.Response.Error.Message, "secret anthropic") {
		t.Fatalf("unexpected answer: %#v", refused.Response)
	}

	storeSecret(t, machine, "anthropic", secretTestValue)
	second := circulation.Intention{IntentionID: "i-secret-2", Correlation: &circulation.Correlation{}}
	if abort := l.ensureWrapperReady("w1", second); !abort {
		t.Fatal("the wrapper cannot be ready here: it never connects")
	}
	answer := recvExecMsg(t, commCh, "the answer after the value was stored")
	if answer.Response.Error == nil {
		t.Fatalf("expected the readiness timeout, got %#v", answer.Response)
	}
	if strings.Contains(answer.Response.Error.Message, "failed to be ready") {
		t.Fatalf("the secret failure was remembered and the start was not tried again: %q", answer.Response.Error.Message)
	}
	if !strings.Contains(answer.Response.Error.Message, "readiness") {
		t.Fatalf("expected the start to be tried and to wait for readiness, got %q", answer.Response.Error.Message)
	}
	if got := readWhenWritten(t, out); got != secretTestValue {
		t.Fatalf("the retried wrapper was given %q", got)
	}
}

// Any other launch failure keeps being remembered, as before.
func TestWrapperSecrets_OtherFailuresAreStillRemembered(t *testing.T) {
	l, commCh, _, _ := secretsLoop(t, 300, nil, nil)
	st := l.frame.Wrappers["w1"]
	st.Lock()
	st.ReadyErr = os.ErrClosed
	st.Unlock()
	in := circulation.Intention{IntentionID: "i-other", Correlation: &circulation.Correlation{}}
	if abort := l.ensureWrapperReady("w1", in); !abort {
		t.Fatal("a remembered failure must still abort")
	}
	answer := recvExecMsg(t, commCh, "the remembered failure")
	if answer.Response.Error == nil || !strings.Contains(answer.Response.Error.Message, "failed to be ready") {
		t.Fatalf("unexpected answer: %#v", answer.Response)
	}
}
