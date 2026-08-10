package runtime

import "testing"

func TestInstanceLockRejectsConcurrentOwnerAndReleases(t *testing.T) {
	layout, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := layout.Ensure(); err != nil {
		t.Fatal(err)
	}
	first, err := layout.AcquireInstanceLock()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := layout.AcquireInstanceLock(); err == nil {
		t.Fatal("expected concurrent instance lock to fail")
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := layout.AcquireInstanceLock()
	if err != nil {
		t.Fatalf("lock was not released: %v", err)
	}
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
}
