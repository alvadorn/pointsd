package ledger

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
)

func TestAwardDeduplicatesPayment(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryStore()
	first := Entry{MemberID: "member", PaymentID: "payment", Points: 200, Month: "2026-10"}
	if err := s.Award(ctx, first, 10000); err != nil {
		t.Fatal(err)
	}
	duplicate := Entry{MemberID: "other", PaymentID: "payment", Points: 900, Month: "2026-11"}
	if err := s.Award(ctx, duplicate, 10000); err != nil {
		t.Fatal(err)
	}
	want := []Entry{{MemberID: "member", PaymentID: "payment", Kind: KindAward, Points: 200, Month: "2026-10"}}
	if !reflect.DeepEqual(s.entries, want) {
		t.Fatalf("entries = %#v, want %#v", s.entries, want)
	}
}

func TestAwardCapsAndRecordsZero(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryStore()
	for _, e := range []Entry{
		{MemberID: "member", PaymentID: "first", Points: 9000, Month: "2026-10"},
		{MemberID: "member", PaymentID: "cut", Points: 2000, Month: "2026-10"},
		{MemberID: "member", PaymentID: "zero", Points: 100, Month: "2026-10"},
	} {
		if err := s.Award(ctx, e, 10000); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Reverse(ctx, "cut"); err != nil {
		t.Fatal(err)
	}
	if err := s.Award(ctx, Entry{MemberID: "member", PaymentID: "after-reversal", Points: 100, Month: "2026-10"}, 10000); err != nil {
		t.Fatal(err)
	}
	want := []Entry{
		{MemberID: "member", PaymentID: "first", Kind: KindAward, Points: 9000, Month: "2026-10"},
		{MemberID: "member", PaymentID: "cut", Kind: KindAward, Points: 1000, Month: "2026-10"},
		{MemberID: "member", PaymentID: "zero", Kind: KindAward, Points: 0, Month: "2026-10"},
		{MemberID: "member", PaymentID: "cut", Kind: KindReversal, Points: -1000, Month: "2026-10"},
		{MemberID: "member", PaymentID: "after-reversal", Kind: KindAward, Points: 0, Month: "2026-10"},
	}
	if !reflect.DeepEqual(s.entries, want) {
		t.Fatalf("entries = %#v, want %#v", s.entries, want)
	}
}

func TestAwardConcurrentSameMemberRespectsCap(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryStore()
	const count = 20
	start := make(chan struct{})
	errs := make(chan error, count)
	var wg sync.WaitGroup
	for i := range count {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			errs <- s.Award(ctx, Entry{MemberID: "member", PaymentID: fmt.Sprintf("payment-%d", i), Points: 1000, Month: "2026-10"}, 10000)
		}(i)
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(s.entries) != count {
		t.Fatalf("got %d entries, want %d", len(s.entries), count)
	}
	var total int64
	for _, e := range s.entries {
		total += e.Points
	}
	if total != 10000 {
		t.Fatalf("awarded %d points, want 10000", total)
	}
}

func TestReverseUsesStoredAward(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryStore()
	if err := s.Award(ctx, Entry{MemberID: "member", PaymentID: "payment", Points: 2000, Month: "2026-10"}, 1000); err != nil {
		t.Fatal(err)
	}
	if err := s.Reverse(ctx, "payment"); err != nil {
		t.Fatal(err)
	}
	want := Entry{MemberID: "member", PaymentID: "payment", Kind: KindReversal, Points: -1000, Month: "2026-10"}
	if len(s.entries) != 2 || s.entries[1] != want {
		t.Fatalf("entries = %#v, want reversal %#v", s.entries, want)
	}
}

func TestReverseDeduplicatesPayment(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryStore()
	if err := s.Award(ctx, Entry{MemberID: "member", PaymentID: "payment", Points: 300, Month: "2026-10"}, 10000); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := s.Reverse(ctx, "payment"); err != nil {
			t.Fatal(err)
		}
	}
	if len(s.entries) != 2 || s.entries[1].Kind != KindReversal || s.entries[1].Points != -300 {
		t.Fatalf("entries = %#v, want one reversal of -300", s.entries)
	}
}

func TestReverseUnknownPayment(t *testing.T) {
	s := NewMemoryStore()
	if err := s.Reverse(context.Background(), "missing"); !errors.Is(err, ErrNoAward) {
		t.Fatalf("Reverse error = %v, want ErrNoAward", err)
	}
	if len(s.entries) != 0 {
		t.Fatalf("unexpected entries: %#v", s.entries)
	}
}

func TestReverseZeroAward(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryStore()
	if err := s.Award(ctx, Entry{MemberID: "member", PaymentID: "payment", Points: 100, Month: "2026-10"}, 0); err != nil {
		t.Fatal(err)
	}
	if err := s.Reverse(ctx, "payment"); err != nil {
		t.Fatal(err)
	}
	want := Entry{MemberID: "member", PaymentID: "payment", Kind: KindReversal, Points: 0, Month: "2026-10"}
	if len(s.entries) != 2 || s.entries[1] != want {
		t.Fatalf("entries = %#v, want zero reversal %#v", s.entries, want)
	}
	balance, err := s.Balance(ctx, "member")
	if err != nil || balance != 0 {
		t.Fatalf("balance = %d, error = %v, want 0 and nil", balance, err)
	}
}

func TestAwardFaultHasNoEffect(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryStore(WithFaults(func(op string) error {
		if op == "award" {
			return ErrUnavailable
		}
		return nil
	}))
	err := s.Award(ctx, Entry{MemberID: "member", PaymentID: "payment", Points: 100, Month: "2026-10"}, 10000)
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Award error = %v, want ErrUnavailable", err)
	}
	if len(s.entries) != 0 {
		t.Fatalf("faulted award wrote entries: %#v", s.entries)
	}
}

func TestReverseFaultHasNoEffect(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryStore(WithFaults(func(op string) error {
		if op == "reverse" {
			return ErrUnavailable
		}
		return nil
	}))
	if err := s.Award(ctx, Entry{MemberID: "member", PaymentID: "payment", Points: 100, Month: "2026-10"}, 10000); err != nil {
		t.Fatal(err)
	}
	before := append([]Entry(nil), s.entries...)
	if err := s.Reverse(ctx, "payment"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Reverse error = %v, want ErrUnavailable", err)
	}
	if !reflect.DeepEqual(s.entries, before) {
		t.Fatalf("entries = %#v, want unchanged %#v", s.entries, before)
	}
}
