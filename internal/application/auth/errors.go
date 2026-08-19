package auth

import "errors"

// MinPasswordLength is the shortest password RegisterUseCase accepts. It is
// deliberately modest: this is a technical test, not a production password
// policy, but *some* floor keeps "12345" style accounts out.
const MinPasswordLength = 8

// ErrPasswordTooShort is returned when a registration password is shorter
// than MinPasswordLength.
var ErrPasswordTooShort = errors.New("auth: password must be at least 8 characters")
