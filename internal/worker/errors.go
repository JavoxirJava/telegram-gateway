package worker

import (
	"errors"
	"fmt"
	"time"
)

type RetryError struct {
	After time.Duration
	Err   error
}

func (e *RetryError) Error() string {
	if e.Err == nil {
		return fmt.Sprintf("retry after %s", e.After)
	}
	return fmt.Sprintf("retry after %s: %v", e.After, e.Err)
}

func (e *RetryError) Unwrap() error { return e.Err }

type PermanentError struct {
	Err error
}

func (e *PermanentError) Error() string {
	if e.Err == nil {
		return "permanent worker error"
	}
	return "permanent worker error: " + e.Err.Error()
}

func (e *PermanentError) Unwrap() error { return e.Err }

func RetryAfter(after time.Duration, err error) error {
	if after < time.Second {
		after = time.Second
	}
	return &RetryError{After: after, Err: err}
}

func Permanent(err error) error {
	return &PermanentError{Err: err}
}

func RetryDelay(err error) (time.Duration, bool) {
	var retry *RetryError
	if !errors.As(err, &retry) || retry.After <= 0 {
		return 0, false
	}
	return retry.After, true
}

func IsPermanent(err error) bool {
	var permanent *PermanentError
	return errors.As(err, &permanent)
}
