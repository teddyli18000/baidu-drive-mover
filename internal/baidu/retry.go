package baidu

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"
)

const (
	defaultRetryBase = 500 * time.Millisecond
	maxRetryDelay    = 30 * time.Second
)

type TransientError struct {
	Operation  string
	Status     int
	RetryAfter time.Duration
	Err        error
}

func (e *TransientError) Error() string {
	if e == nil {
		return "transient remote error"
	}
	if e.Status != 0 {
		return fmt.Sprintf("%s: transient HTTP %d", e.Operation, e.Status)
	}
	if e.Err != nil {
		return fmt.Sprintf("%s: transient network failure: %v", e.Operation, e.Err)
	}
	return fmt.Sprintf("%s: transient remote failure", e.Operation)
}

func (e *TransientError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func IsTransient(err error) bool {
	if err == nil {
		return false
	}
	var transient *TransientError
	if errors.As(err, &transient) {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr) && (netErr.Timeout() || netErr.Temporary())
}

func RetryDelay(err error, attempt int) time.Duration {
	var transient *TransientError
	if errors.As(err, &transient) && transient.RetryAfter > 0 {
		if transient.RetryAfter > maxRetryDelay {
			return fallbackRetryDelay(attempt)
		}
		return transient.RetryAfter
	}
	return fallbackRetryDelay(attempt)
}

func fallbackRetryDelay(attempt int) time.Duration {
	if attempt < 0 {
		attempt = 0
	}
	if attempt > 5 {
		attempt = 5
	}
	delay := defaultRetryBase * time.Duration(1<<attempt)
	if delay > maxRetryDelay {
		return maxRetryDelay
	}
	return delay
}

func parseRetryAfter(value string, now time.Time) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	if seconds, err := strconv.ParseInt(value, 10, 64); err == nil {
		if seconds <= 0 {
			return 0
		}
		delay := time.Duration(seconds) * time.Second
		if delay > maxRetryDelay {
			return 0
		}
		return delay
	}
	when, err := time.Parse(time.RFC1123, value)
	if err != nil {
		return 0
	}
	delay := when.Sub(now)
	if delay <= 0 || delay > maxRetryDelay {
		return 0
	}
	return delay
}

func (c *Client) retryRead(ctx context.Context, operation string, fn func() ([]byte, int, error)) ([]byte, int, error) {
	attempts := c.maxListRetries
	if attempts <= 0 {
		attempts = defaultMaxListRetries
	}
	var lastErr error
	var lastStatus int
	for attempt := 0; attempt < attempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, 0, err
		}
		body, status, err := fn()
		if err == nil {
			return body, status, nil
		}
		lastErr = err
		lastStatus = status
		if !IsTransient(err) || attempt+1 >= attempts {
			return nil, status, err
		}
		if err := c.sleep(ctx, RetryDelay(err, attempt)); err != nil {
			return nil, status, err
		}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("retry loop exhausted")
	}
	return nil, lastStatus, fmt.Errorf("%s after bounded retries: %w", operation, lastErr)
}
