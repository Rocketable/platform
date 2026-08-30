package backend

import (
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestKeyedConversationLocksSerializeOneIDAndReleaseEntries(t *testing.T) {
	locks := NewKeyedConversationLocks()
	unlockFirst := locks.Lock("shared")
	acquired := make(chan struct{})
	releaseSecond := make(chan struct{})
	done := make(chan struct{})

	go func() {
		unlockSecond := locks.Lock("shared")

		close(acquired)
		<-releaseSecond
		unlockSecond()
		close(done)
	}()

	select {
	case <-acquired:
		t.Fatal("same conversation lock overtook first holder")
	default:
	}

	unlockFirst()
	<-acquired
	close(releaseSecond)
	<-done

	locks.mu.Lock()
	assert.Empty(t, locks.locks)
	locks.mu.Unlock()
}

func TestKeyedConversationLocksAllowIndependentIDs(t *testing.T) {
	locks := NewKeyedConversationLocks()
	unlockFirst := locks.Lock("first")
	unlockedSecond := make(chan struct{})

	go func() {
		unlockSecond := locks.Lock("second")
		unlockSecond()
		close(unlockedSecond)
	}()

	select {
	case <-unlockedSecond:
	case <-time.After(time.Second):
		t.Fatal("independent conversation ID was blocked")
	}

	unlockFirst()
}

func testLogger() *slog.Logger {
	return slog.New(slog.DiscardHandler)
}
