// Package fault describes public errors without exposing internal diagnostics.
package fault

type Error struct {
	Code, Message string
	Status        int
	Fields        map[string]string
	Retryable     bool
}

func (e *Error) Error() string { return e.Code }
func New(status int, code, message string) *Error {
	return &Error{Status: status, Code: code, Message: message, Retryable: status == 503}
}
