package metric

import (
	"context"
	"testing"
)

// TestAcceptsWritesDefaultsToTrue guards the fail-safe default: a store that
// never ran RefreshWriteAcceptance must keep the historical behaviour, where the
// per-point retention filter decides what is stored.
func TestAcceptsWritesDefaultsToTrue(t *testing.T) {
	store := &Store{}
	if !store.AcceptsWrites() {
		t.Fatal("a fresh store must accept writes until a policy says otherwise")
	}
}

func TestAcceptsWritesOnNilStore(t *testing.T) {
	var store *Store
	if store.AcceptsWrites() {
		t.Fatal("a nil store must not report that it accepts writes")
	}
	if err := store.RefreshWriteAcceptance(context.Background()); err != nil {
		t.Fatalf("RefreshWriteAcceptance on a nil store: %v", err)
	}
}

func TestRefreshWriteAcceptanceWithoutOpenStore(t *testing.T) {
	// An unopened store cannot list definitions, so the refresh must surface an
	// error rather than silently flipping the switch.
	store := &Store{}
	if err := store.RefreshWriteAcceptance(context.Background()); err == nil {
		t.Fatal("expected an error when the store is not open")
	}
	if !store.AcceptsWrites() {
		t.Fatal("a failed refresh must leave the default in place")
	}
}

func TestRefreshWriteAcceptanceWithNilDatabaseDoesNotPanic(t *testing.T) {
	// ListMetrics dereferences the database handle; the refresh must not turn a
	// half-initialized store into a panic on the report path.
	store := &Store{}
	defer func() {
		if recovered := recover(); recovered != nil {
			t.Fatalf("RefreshWriteAcceptance panicked: %v", recovered)
		}
	}()
	_ = store.RefreshWriteAcceptance(context.Background())
}
