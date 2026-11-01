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

package comm

import (
	"fmt"
	"io"
	"os"
	"strings"

	"brique_engine/configuration"
)

// warningWriter receives the configuration warnings; tests replace it.
var warningWriter io.Writer = os.Stderr

// InterfaceWarnings lists the interface settings that widen who can reach the
// context, so that an operator sees them at startup (SECURITY.md §1.4). Nothing
// here refuses a configuration: each line is a fact about the authority it grants.
//
//   - a message that arrives through an ordinary ws/http/https interface, or from a
//     wrapper without a restricted principal, runs with the local authority of its
//     context; `allow_any_origin` lets any web page reach such an interface;
//   - `tls_insecure_skip_verify` turns off certificate verification on an interface;
//     signed external traffic refuses it, so it can only affect unsigned traffic.
func InterfaceWarnings(contextID string, ifaces []InterfaceCfg) []string {
	var warnings []string
	for _, iface := range ifaces {
		config := iface.Config
		ingress := true
		if value, ok := config[configuration.KeyIntIngressEnabled].(bool); ok {
			ingress = value
		}
		anyOrigin, _ := config[configuration.KeyIntAnyOri].(bool)
		insecure, _ := config[configuration.KeyTLSInsecureSkipVerify].(bool)
		driver := strings.ToLower(iface.Driver)
		if ingress && anyOrigin && iface.Type != "outerCtx" && (driver == "ws" || driver == "http" || driver == "https") {
			warnings = append(warnings, fmt.Sprintf(
				"context %s interface %q accepts messages from any origin and runs them with the local authority of the context; restrict allowed_origins, or give the component a restricted principal",
				contextID, iface.Name))
		}
		if insecure {
			warnings = append(warnings, fmt.Sprintf(
				"context %s interface %q does not verify TLS certificates (tls_insecure_skip_verify); signed external traffic will refuse to use it",
				contextID, iface.Name))
		}
	}
	return warnings
}

func (l *CommLoop) warnAboutInterfaces() {
	if l == nil || l.frame == nil {
		return
	}
	for _, warning := range InterfaceWarnings(l.frame.CtxId, l.cfg.Ifaces) {
		fmt.Fprintf(warningWriter, "Warning: %s\n", warning)
	}
}
