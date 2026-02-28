// Copyright 2026 Peter Edge
//
// All rights reserved.

// Package backoff provides exponential backoff with jitter for retrying operations.
package backoff

import (
	"context"
	"fmt"
	"math/rand/v2"
	"time"
)

// Retry calls f repeatedly until it succeeds, returns a non-retryable error,
// or the maximum number of attempts is reached. Between attempts, it waits with
// exponential backoff and jitter.
//
// f returns the result, whether the error is retryable, and any error.
// If retryable is true and err is non-nil, Retry will wait and try again.
// If retryable is false, Retry returns immediately with the result and error.
func Retry[T any](
	ctx context.Context,
	maxAttempts int,
	initialDelay time.Duration,
	maxDelay time.Duration,
	f func(ctx context.Context, attempt int) (T, bool, error),
) (T, error) {
	var zero T
	delay := initialDelay
	for attempt := range maxAttempts {
		result, retryable, err := f(ctx, attempt)
		if err == nil {
			return result, nil
		}
		if !retryable {
			return zero, err
		}
		// Don't wait after the last attempt.
		if attempt == maxAttempts-1 {
			return zero, fmt.Errorf("failed after %d attempts: %w", maxAttempts, err)
		}
		// Wait with jitter: random duration between delay/2 and delay.
		jitteredDelay := delay/2 + time.Duration(rand.Int64N(int64(delay/2+1)))
		select {
		case <-ctx.Done():
			return zero, ctx.Err()
		case <-time.After(jitteredDelay):
		}
		// Exponential backoff, capped at maxDelay.
		delay *= 2
		if delay > maxDelay {
			delay = maxDelay
		}
	}
	return zero, fmt.Errorf("failed after %d attempts", maxAttempts)
}
