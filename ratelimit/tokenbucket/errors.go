package tokenbucket

import "errors"

// ErrInvalidConfig indicates that the provided rate or burst is invalid.
var ErrInvalidConfig = errors.New("tokenbucket: rate and burst must be positive")
