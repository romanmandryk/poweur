package identity

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

var powSecret = []byte("test-secret-please-rotate")

func TestPowSolveVerifyRoundtrip(t *testing.T) {
	for _, bits := range []int{8, 10, 12} {
		token, challenge, err := NewPowChallenge(powSecret, "msg:alice.poweur.net", bits, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		if challenge.Bits != bits {
			t.Fatalf("bits: %d want %d", challenge.Bits, bits)
		}
		solution, err := SolvePow(context.Background(), token, challenge.Bits)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := VerifyPowSolution(powSecret, token, solution, "msg:alice.poweur.net"); err != nil {
			t.Fatalf("bits=%d: %v", bits, err)
		}
	}
}

func TestPowRejections(t *testing.T) {
	token, challenge, err := NewPowChallenge(powSecret, "msg:alice.poweur.net", 8, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	solution, err := SolvePow(context.Background(), token, challenge.Bits)
	if err != nil {
		t.Fatal(err)
	}

	// Wrong purpose (cross-surface replay).
	if _, err := VerifyPowSolution(powSecret, token, solution, "registration"); err == nil {
		t.Fatal("purpose mismatch must fail")
	}
	// Wrong secret (forged issuer).
	if _, err := VerifyPowSolution([]byte("other-secret"), token, solution, "msg:alice.poweur.net"); err == nil {
		t.Fatal("wrong secret must fail")
	}
	// Garbage solution.
	if _, err := VerifyPowSolution(powSecret, token, "not-a-solution", "msg:alice.poweur.net"); err == nil {
		t.Fatal("bad solution must fail")
	}
	// Tampered token payload (difficulty downgrade attempt).
	parts := strings.SplitN(token, ".", 2)
	tampered := parts[0][:len(parts[0])-2] + "AA." + parts[1]
	if _, err := ParsePowToken(powSecret, tampered); err == nil {
		t.Fatal("tampered token must fail")
	}
	// Expired token.
	expired, _, err := NewPowChallenge(powSecret, "msg:alice.poweur.net", 8, -time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParsePowToken(powSecret, expired); err == nil {
		t.Fatal("expired token must fail")
	}
}

func TestPowClamp(t *testing.T) {
	cases := map[int]int{0: PowDefaultBits, 1: PowMinBits, 12: 12, 99: PowMaxBits, -5: PowDefaultBits}
	for in, want := range cases {
		if got := ClampPowBits(in); got != want {
			t.Fatalf("clamp(%d) = %d want %d", in, got, want)
		}
	}
}

func TestPowSolveCancellable(t *testing.T) {
	// An absurd difficulty must abort promptly when the context cancels.
	token, _, err := NewPowChallenge(powSecret, "x", PowMaxBits, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := SolvePow(ctx, token, 60); err == nil {
		t.Fatal("cancelled solve must return an error")
	}
}

// BenchmarkPowSolve prints the measured bits→duration table that feeds the
// spec (run with `go test -bench PowSolve -run ^$ ./packages/identity/`).
func BenchmarkPowSolve(b *testing.B) {
	for _, bitsN := range []int{8, 12, 16, 20} {
		b.Run(fmt.Sprintf("bits=%d", bitsN), func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				token, _, err := NewPowChallenge(powSecret, "bench", bitsN, time.Minute)
				if err != nil {
					b.Fatal(err)
				}
				if _, err := SolvePow(context.Background(), token, bitsN); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
