package baidu

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestAccessSharePageRetries429UsingRetryAfter(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts < 3 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			fmt.Fprint(w, "rate limited")
			return
		}
		fmt.Fprint(w, strings.ReplaceAll(fakeSharePage(), `\"`, `"`))
	}))
	defer server.Close()
	var sleeps []time.Duration
	client, err := NewClient("BDUSS=fake; STOKEN=fake",
		WithBaseURL(server.URL),
		WithSleep(func(_ context.Context, d time.Duration) error {
			sleeps = append(sleeps, d)
			return nil
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	link, _ := ParseShareLink("https://pan.baidu.com/s/1Synthetic")
	if _, err := client.AccessSharePage(context.Background(), link); err != nil {
		t.Fatal(err)
	}
	if attempts != 3 {
		t.Fatalf("attempts=%d want=3", attempts)
	}
	if len(sleeps) != 2 || sleeps[0] != time.Second || sleeps[1] != time.Second {
		t.Fatalf("Retry-After sleeps=%v", sleeps)
	}
}

func TestAccessSharePage503RetriesAreBounded(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprint(w, "temporary outage")
	}))
	defer server.Close()
	var sleeps []time.Duration
	client, err := NewClient("BDUSS=fake; STOKEN=fake",
		WithBaseURL(server.URL),
		WithSleep(func(_ context.Context, d time.Duration) error {
			sleeps = append(sleeps, d)
			return nil
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	link, _ := ParseShareLink("https://pan.baidu.com/s/1Synthetic")
	_, err = client.AccessSharePage(context.Background(), link)
	if err == nil || !IsTransient(err) {
		t.Fatalf("expected bounded transient error, got %v", err)
	}
	if attempts != defaultMaxListRetries {
		t.Fatalf("attempts=%d want=%d", attempts, defaultMaxListRetries)
	}
	if len(sleeps) != defaultMaxListRetries-1 {
		t.Fatalf("sleeps=%v", sleeps)
	}
	want := []time.Duration{500 * time.Millisecond, time.Second, 2 * time.Second}
	for i := range want {
		if sleeps[i] != want[i] {
			t.Fatalf("sleep[%d]=%v want=%v", i, sleeps[i], want[i])
		}
	}
}

func TestRetryAfterAboveCapFallsBackToExponentialDelay(t *testing.T) {
	err := &TransientError{Operation: "test", Status: 429, RetryAfter: 2 * time.Minute}
	if got := RetryDelay(err, 2); got != 2*time.Second {
		t.Fatalf("delay=%v want=2s", got)
	}
}

func TestReadRetryCancellationStopsImmediately(t *testing.T) {
	attempts := 0
	ctx, cancel := context.WithCancel(context.Background())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	client, err := NewClient("BDUSS=fake; STOKEN=fake",
		WithBaseURL(server.URL),
		WithSleep(func(ctx context.Context, _ time.Duration) error {
			cancel()
			<-ctx.Done()
			return ctx.Err()
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	link, _ := ParseShareLink("https://pan.baidu.com/s/1Synthetic")
	_, err = client.AccessSharePage(ctx, link)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation, got %v", err)
	}
	if attempts != 1 {
		t.Fatalf("attempts=%d want=1", attempts)
	}
}

func TestPasswordVerificationDoesNotBlindRetryTransientHTTPFailure(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprint(w, "temporary outage")
	}))
	defer server.Close()
	client, err := NewClient("BDUSS=fake; STOKEN=fake", WithBaseURL(server.URL), WithSleep(func(context.Context, time.Duration) error {
		t.Fatal("password verification must not enter read retry sleep")
		return nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	link, _ := ParseShareLink("https://pan.baidu.com/s/1Synthetic")
	err = client.VerifyPassword(context.Background(), link, ShareContext{BDSToken: "t", ShareID: "1", ShareUK: "2"}, "abcd")
	if err == nil || !IsTransient(err) {
		t.Fatalf("expected one-shot transient error, got %v", err)
	}
	if attempts != 1 {
		t.Fatalf("password verification attempts=%d want=1", attempts)
	}
}
