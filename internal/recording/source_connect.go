package recording

import "errors"

// Only fixed stage codes may reach observations. The underlying error remains
// available to ownership checks, but its text (which can include a URL) is hidden.
type sourceConnectionFailure struct {
	stage string
	cause error
}

func (e *sourceConnectionFailure) Error() string { return "source_connection_" + e.stage }
func (e *sourceConnectionFailure) Unwrap() error { return e.cause }
func sourceFailure(stage string, err error) error {
	return &sourceConnectionFailure{stage: stage, cause: err}
}
func recoveryFailureReason(err error) string {
	var failure *sourceConnectionFailure
	if errors.As(err, &failure) {
		return "source_recovery_" + failure.stage + "_unavailable"
	}
	return "source_unavailable"
}

func subFailureReason(err error) string {
	var failure *sourceConnectionFailure
	if errors.As(err, &failure) {
		switch failure.stage {
		case "inspect", "credentials", "add_proxy", "first_frame":
			return "sub_source_" + failure.stage + "_unavailable"
		}
	}
	return "sub_source_unavailable"
}
