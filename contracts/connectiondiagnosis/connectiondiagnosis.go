/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

// Package connectiondiagnosis is the result of a connection diagnosis: the ordered
// steps a connection to a declared peer goes through, the status of each and a stable
// code for each failure. The codes are fixed so that the launcher and any client never
// depend on a text. The result is for the local owner only: it carries resolved
// addresses and bounded certificate facts, never a path, a secret or a key.
package connectiondiagnosis

// Format is the wire name of a diagnosis result.
const Format = "brique.connection-diagnosis/1"

// Step names, in the order they run. A step that cannot run because an earlier one
// failed is reported as skipped.
const (
	StepConfiguration = "configuration"
	StepNetworkPolicy = "network_policy"
	StepDNS           = "dns"
	StepTCP           = "tcp"
	StepTLS           = "tls"
	StepHTTP          = "http"
	StepIdentity      = "identity"
	StepSignature     = "signature"
)

// Steps lists the steps in execution order.
var Steps = []string{StepConfiguration, StepNetworkPolicy, StepDNS, StepTCP, StepTLS, StepHTTP, StepIdentity, StepSignature}

// Status of one step.
const (
	StatusOK      = "ok"
	StatusFailed  = "failed"
	StatusSkipped = "skipped"
	StatusWarning = "warning"
)

// Stable codes. An empty code means the step has nothing to report.
const (
	// configuration
	CodeUnknownPeer      = "unknown_peer"
	CodeUnknownEndpoint  = "unknown_endpoint"
	CodeInvalidURL       = "invalid_url"
	CodeInsecureScheme   = "insecure_scheme"
	CodeNoHTTPSInterface = "no_https_interface"
	// network policy
	CodeAddressDenied = "address_denied"
	CodeNoDialPolicy  = "no_dial_policy"
	// dns
	CodeNameNotFound = "name_not_found"
	CodeDNSTimeout   = "dns_timeout"
	CodeDNSFailed    = "dns_failed"
	// tcp
	CodeConnectionRefused = "connection_refused"
	CodeConnectTimeout    = "connect_timeout"
	CodeUnreachable       = "unreachable"
	// tls
	CodeCertificateExpired     = "certificate_expired"
	CodeCertificateNotYetValid = "certificate_not_yet_valid"
	CodeNameMismatch           = "certificate_name_mismatch"
	CodeUnknownAuthority       = "unknown_authority"
	// CodeCertificateRejected is a failed verification the platform verifier does not say more
	// about (the operating system verifies the chain when no private roots are configured).
	CodeCertificateRejected    = "certificate_rejected"
	CodeTLSHandshakeFailed     = "tls_handshake_failed"
	CodeTLSTimeout             = "tls_timeout"
	CodeVerificationDisabled   = "tls_verification_disabled"
	CodeCertificateExpiresSoon = "certificate_expires_soon"
	// http
	CodeHTTPAnswered      = "http_answered"
	CodeProxyUpstreamDown = "proxy_upstream_error"
	CodeRedirectRefused   = "redirect_refused"
	CodeBadStatus         = "bad_status"
	CodeHTTPFailed        = "http_failed"
	CodeHTTPTimeout       = "http_timeout"
	// identity and signature (reported by the sub-lot that implements them)
	CodePeerNotRecognisedOrAbsent = "peer_not_recognised_or_absent"
	CodeNotRun                    = "not_run"
	// whole run
	CodeBusy         = "busy"
	CodeRateLimited  = "rate_limited"
	CodeTotalTimeout = "total_timeout"
	CodeUnavailable  = "diagnosis_unavailable"
)

// MaxDetailEntries and MaxDetailValueBytes bound what a step may report.
const (
	MaxDetailEntries    = 8
	MaxDetailValueBytes = 128
)

// Step is the outcome of one step.
type Step struct {
	Name           string            `json:"name"`
	Status         string            `json:"status"`
	DurationMillis int64             `json:"duration_ms"`
	Code           string            `json:"code,omitempty"`
	Message        string            `json:"message,omitempty"`
	Detail         map[string]string `json:"detail,omitempty"`
}

// Result is the whole diagnosis of one peer endpoint.
type Result struct {
	Format   string `json:"format"`
	PeerID   string `json:"peer_id"`
	Endpoint string `json:"endpoint,omitempty"`
	// OK is true exactly when no step failed.
	OK    bool   `json:"ok"`
	Steps []Step `json:"steps"`
}

// Valid reports whether the result is well formed: known step names, known statuses,
// ordered, bounded details.
func (result Result) Valid() bool {
	if result.Format != Format || len(result.Steps) == 0 || len(result.Steps) > len(Steps) {
		return false
	}
	position := -1
	failed := false
	for _, step := range result.Steps {
		index := -1
		for i, name := range Steps {
			if name == step.Name {
				index = i
			}
		}
		if index <= position {
			return false
		}
		position = index
		switch step.Status {
		case StatusOK, StatusWarning, StatusSkipped:
		case StatusFailed:
			failed = true
		default:
			return false
		}
		if len(step.Detail) > MaxDetailEntries {
			return false
		}
		for key, value := range step.Detail {
			if key == "" || len(value) > MaxDetailValueBytes {
				return false
			}
		}
	}
	return result.OK == !failed
}

// Refused builds the result of a diagnosis that could not run: one failed step with the
// code, every later step skipped. peerID and endpoint echo the request.
func Refused(peerID, endpoint, step, code, message string) Result {
	result := Result{Format: Format, PeerID: peerID, Endpoint: endpoint}
	failed := false
	for _, name := range Steps {
		switch {
		case failed:
			result.Steps = append(result.Steps, Step{Name: name, Status: StatusSkipped, Code: CodeNotRun, Message: "an earlier step failed"})
		case name == step:
			failed = true
			result.Steps = append(result.Steps, Step{Name: name, Status: StatusFailed, Code: code, Message: message})
		default:
			result.Steps = append(result.Steps, Step{Name: name, Status: StatusOK})
		}
	}
	return result
}
