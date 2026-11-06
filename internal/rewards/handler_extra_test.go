package rewards_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/rentpoints/pointsd/internal/ledger"
	"github.com/rentpoints/pointsd/internal/rewards"
)

func TestReversal_ConcurrentRedeliveryAcrossReplicasIsReversedOnce(t *testing.T) {
	store := ledger.NewMemoryStore()
	replicas := []*rewards.Handler{newHandler(store), newHandler(store)}
	deliver(t, replicas[0], settled("pay_1", "mbr_1", 2000))

	messages := make([]*fakeMessage, 8)
	var calls []func()
	for i := range messages {
		messages[i] = newMessage(t, reversed("pay_1", "mbr_1", 2000))
		h, msg := replicas[i%2], messages[i]
		calls = append(calls, func() { h.Handle(context.Background(), msg) })
	}
	concurrently(calls...)
	for _, msg := range messages {
		if got := msg.outcome(t); got != acked {
			t.Errorf("reversal outcome = %v, want ack", got)
		}
	}
	if got := balance(t, store, "mbr_1"); got != 0 {
		t.Fatalf("balance = %d, want 0", got)
	}
}

func TestReversal_TransientErrorsAreRetried(t *testing.T) {
	failures := 0
	store := ledger.NewMemoryStore(ledger.WithFaults(func(op string) error {
		if op == "reverse" && failures < 2 {
			failures++
			return ledger.ErrUnavailable
		}
		return nil
	}))
	h := newHandler(store)
	deliver(t, h, settled("pay_1", "mbr_1", 2000))

	for attempt := 1; attempt <= 3; attempt++ {
		msg := newMessage(t, reversed("pay_1", "mbr_1", 2000))
		msg.attempt = attempt
		h.Handle(context.Background(), msg)
		want := nacked
		wantBalance := int64(2000)
		if attempt == 3 {
			want = acked
			wantBalance = 0
		}
		if got := msg.outcome(t); got != want {
			t.Fatalf("attempt %d outcome = %v, want %v", attempt, got, want)
		}
		if got := balance(t, store, "mbr_1"); got != wantBalance {
			t.Fatalf("attempt %d balance = %d, want %d", attempt, got, wantBalance)
		}
	}
}

func TestReversal_ZeroAwardLeavesBalanceUnchanged(t *testing.T) {
	store := ledger.NewMemoryStore()
	h := newHandler(store)
	deliver(t, h, settled("pay_cap", "mbr_1", 10000))
	deliver(t, h, settled("pay_zero", "mbr_1", 500))
	deliver(t, h, reversed("pay_zero", "mbr_1", 500))
	if got := balance(t, store, "mbr_1"); got != 10000 {
		t.Fatalf("balance = %d, want 10000", got)
	}
}

func TestReversal_UsesStoredAwardDespiteDivergentEvent(t *testing.T) {
	store := ledger.NewMemoryStore()
	h := newHandler(store)
	deliver(t, h, settled("pay_1", "mbr_1", 2000))
	ev := reversed("pay_1", "mbr_2", 500)
	ev.OccurredAt = oct.AddDate(0, 2, 0)
	deliver(t, h, ev)
	if got := balance(t, store, "mbr_1"); got != 0 {
		t.Errorf("original member balance = %d, want 0", got)
	}
	if got := balance(t, store, "mbr_2"); got != 0 {
		t.Errorf("event member balance = %d, want 0", got)
	}
}

type handlerErrorStore struct {
	awardErr   error
	reverseErr error
}

func (s handlerErrorStore) Award(context.Context, ledger.Entry, int64) error { return s.awardErr }
func (s handlerErrorStore) Reverse(context.Context, string) error            { return s.reverseErr }
func (s handlerErrorStore) Balance(context.Context, string) (int64, error)   { return 0, nil }

func TestHandle_StoreErrorsSettleExactlyOnce(t *testing.T) {
	failure := errors.New("store failure")
	tests := []struct {
		name  string
		store handlerErrorStore
		event event
		want  outcome
	}{
		{"award error", handlerErrorStore{awardErr: failure}, settled("pay_1", "mbr_1", 100), nacked},
		{"reverse error", handlerErrorStore{reverseErr: failure}, reversed("pay_1", "mbr_1", 100), nacked},
		{"wrapped missing award", handlerErrorStore{reverseErr: fmt.Errorf("reverse: %w", ledger.ErrNoAward)}, reversed("pay_1", "mbr_1", 100), acked},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg := newMessage(t, tt.event)
			newHandler(tt.store).Handle(context.Background(), msg)
			if got := msg.outcome(t); got != tt.want {
				t.Fatalf("outcome = %v, want %v", got, tt.want)
			}
		})
	}
}
