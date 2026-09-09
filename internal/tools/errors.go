package tools

// Error is the error contract (spec §3.6).
type Error struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

// Error codes from the spec §3.6 catalog.
const (
	ErrInvalidArgument      = "INVALID_ARGUMENT"
	ErrClusterNotFound      = "CLUSTER_NOT_FOUND"
	ErrClusterUnavailable   = "CLUSTER_UNAVAILABLE"
	ErrClusterAuthFailed    = "CLUSTER_AUTH_FAILED"
	ErrNamespaceNotFound    = "NAMESPACE_NOT_FOUND"
	ErrResourceNotFound     = "RESOURCE_NOT_FOUND"
	ErrResourceTypeNotFound = "RESOURCE_TYPE_NOT_FOUND"
	ErrK8sForbidden         = "K8S_FORBIDDEN"
	ErrK8sUnauthorized      = "K8S_UNAUTHORIZED"
	ErrK8sTimeout           = "K8S_TIMEOUT"
	ErrK8sAPIError          = "K8S_API_ERROR"
	ErrPolicyDenied         = "POLICY_DENIED"
	ErrMetricsUnavailable   = "METRICS_UNAVAILABLE"
	ErrLogsUnavailable      = "LOGS_UNAVAILABLE"
	ErrResponseTooLarge     = "RESPONSE_TOO_LARGE"
	ErrRateLimited          = "RATE_LIMITED"
	ErrInternal             = "INTERNAL_ERROR"
)

// retryableCodes are the only transient failures: everything else is
// deterministic and retrying unchanged will fail the same way.
var retryableCodes = map[string]bool{
	ErrK8sTimeout:         true,
	ErrClusterUnavailable: true,
	ErrRateLimited:        true,
}

// NewError builds an Error for code with the spec-defined retryable flag.
func NewError(code, message string) *Error {
	return &Error{Code: code, Message: message, Retryable: retryableCodes[code]}
}
