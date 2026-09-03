package engine

import "errors"

// Sentinel errors returned by engine methods.
// The HTTP layer uses errors.Is to map them to status codes.
var (
	// ErrNotFound is returned when a requested entity does not exist.
	ErrNotFound = errors.New("not found")

	// ErrValidation is returned when request input fails validation.
	ErrValidation = errors.New("validation error")

	// ErrForbidden is returned when the actor's role allows the operation but
	// the object is outside their teams. It is distinct from ErrNotFound on
	// purpose: hiding the object's existence would be a stronger guarantee, but
	// it would also make "why did my acknowledge fail" unanswerable in a
	// deployment where responders routinely see a colleague's alert id.
	ErrForbidden = errors.New("forbidden")
)

// notFoundError wraps ErrNotFound with a descriptive message.
type notFoundError struct{ msg string }

func (e *notFoundError) Error() string   { return e.msg }
func (e *notFoundError) Is(t error) bool { return t == ErrNotFound }

// validationError wraps ErrValidation with a descriptive message.
type validationError struct{ msg string }

func (e *validationError) Error() string   { return e.msg }
func (e *validationError) Is(t error) bool { return t == ErrValidation }

// errNotFound returns a not-found error with a descriptive message.
func errNotFound(msg string) error { return &notFoundError{msg} }

// errValidation returns a validation error with a descriptive message.
func errValidation(msg string) error { return &validationError{msg} }

// forbiddenError wraps ErrForbidden with a descriptive message.
type forbiddenError struct{ msg string }

func (e *forbiddenError) Error() string   { return e.msg }
func (e *forbiddenError) Is(t error) bool { return t == ErrForbidden }

// isForbidden reports whether err is a team-boundary refusal.
func isForbidden(err error) bool { return errors.Is(err, ErrForbidden) }

// errForbidden reports a refusal with a caller-supplied reason, for the cases
// that are not team boundaries — a role that is too low, for instance.
func errForbidden(msg string) error { return &forbiddenError{msg} }

// errForbiddenTeam reports a team-boundary refusal. The message names the
// collection but never the owning team: which teams exist is not something a
// scoped actor is entitled to learn from a refusal.
func errForbiddenTeam(collection string) error {
	return &forbiddenError{"this " + singular(collection) + " belongs to a team you are not a member of"}
}
