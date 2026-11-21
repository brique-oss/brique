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

// execution_wrapper_secrets.go
//
// The secrets a wrapper is given at launch. A secret reaches the environment of a wrapper if
// and only if the wrapper declares that it needs it (run.secrets in its context.json), the
// active policy grants it to that wrapper, and the machine holds a value for it. Nothing
// here ever puts a value in an error: a refusal names the secret and the wrapper.

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"brique_engine/junction"
	"brique_engine/security/trust"
	"brique_engine/shared"
)

// errWrapperSecret marks the launch failures that come from a secret: a declaration that
// cannot be used, a right the policy does not give, a value the machine does not hold. Such
// a failure is not remembered by the wrapper state (see ensureWrapperReady): the next call
// tries again, so adding the value or the right is enough to mend it.
var errWrapperSecret = errors.New("wrapper secret")

func isWrapperSecretError(err error) bool { return errors.Is(err, errWrapperSecret) }

type wrapperSecretError struct{ wrapper, secret, reason string }

func (e *wrapperSecretError) Error() string {
	if e.secret == "" {
		return fmt.Sprintf("wrapper %s: secrets: %s", e.wrapper, e.reason)
	}
	return fmt.Sprintf("wrapper %s: secret %s: %s", e.wrapper, e.secret, e.reason)
}

func (e *wrapperSecretError) Is(target error) bool { return target == errWrapperSecret }

var (
	secretNamePattern = regexp.MustCompile(`^[a-z][a-z0-9._-]{0,63}$`)
	secretEnvPattern  = regexp.MustCompile(`^[A-Z_][A-Z0-9_]{0,127}$`)
)

// wrapperSecretEnv resolves the secrets a wrapper declares into environment variables. It
// returns nothing for a wrapper that declares none, and an error as soon as one secret cannot
// be given: a wrapper is never started with part of what it asked for.
//
// The policy is asked of the registry's current trust runtime at each launch and never kept:
// a reload publishes a new runtime.
func (l *ExecutionLoop) wrapperSecretEnv(cfg WrapperRuntimeCfg) (map[string]string, error) {
	needs := cfg.Run.Secrets
	if len(needs) == 0 {
		return nil, nil
	}
	refuse := func(secret, format string, args ...any) error {
		return &wrapperSecretError{wrapper: cfg.Name, secret: secret, reason: fmt.Sprintf(format, args...)}
	}

	// The declaration: what cannot be used is refused, never ignored.
	seen := make(map[string]bool, len(needs))
	for _, need := range needs {
		switch {
		case !secretNamePattern.MatchString(need.Name):
			return nil, refuse(need.Name, "the declared name is not a secret name ([a-z][a-z0-9._-]{0,63})")
		case !secretEnvPattern.MatchString(need.Env):
			return nil, refuse(need.Name, "the variable %q is not an environment variable name ([A-Z_][A-Z0-9_]*)", need.Env)
		case strings.HasPrefix(need.Env, "BRIQUE_"):
			return nil, refuse(need.Name, "the variable %q is reserved to the engine (BRIQUE_)", need.Env)
		case seen[need.Env]:
			return nil, refuse(need.Name, "the variable %q is declared twice", need.Env)
		}
		if _, clash := cfg.Run.Env[need.Env]; clash {
			return nil, refuse(need.Name, "the variable %q is also set in run.env", need.Env)
		}
		seen[need.Env] = true
	}

	var policy *trust.Runtime
	if provider, ok := l.frame.CtxCommReg.(junction.TrustRuntimeProvider); ok {
		policy = provider.TrustRuntime()
	}
	machine := ""
	if provider, ok := l.frame.CtxCommReg.(junction.MachineDirectoryProvider); ok {
		machine = provider.MachineDirectory()
	}

	out := make(map[string]string, len(needs))
	for _, need := range needs {
		if policy == nil || policy.Active() == nil {
			return nil, refuse(need.Name, "no security policy is active: a secret is given only under a policy (brique-sec init, then apply)")
		}
		if !policy.WrapperMaySeeSecret(need.Name, cfg.Name) {
			return nil, refuse(need.Name, "not granted to this wrapper: add it to \"secrets\" in access.json, then brique-sec apply --reload")
		}
		if machine == "" {
			return nil, refuse(need.Name, "this instance has no machine security directory")
		}
		value, err := shared.ReadProtectedFile(machine, shared.SecretsDir+"/"+need.Name, shared.ProtectedFileOptions{MaxBytes: shared.MaxSecretBytes})
		if err != nil {
			return nil, refuse(need.Name, "no usable value on this machine (brique-sec secret add %s): %v", need.Name, err)
		}
		out[need.Env] = string(value)
		shared.ZeroBytes(value)
	}
	return out, nil
}
