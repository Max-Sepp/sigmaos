package mdscode

import (
	"math/rand"
	"sync/atomic"
	"testing"

	"gonum.org/v1/gonum/mat"
)

// clamp maps an arbitrary fuzzer-supplied int into [lo, hi].
func clamp(v, lo, hi int) int {
	span := hi - lo + 1
	v %= span
	if v < 0 {
		v += span
	}
	return v + lo
}

// FuzzRoundTrip checks that encode->TiledMultiply->Decode always reconstructs
// A*B exactly, for arbitrary (clamped) problem sizes and RNG seeds.
func FuzzRoundTrip(f *testing.F) {
	f.Add(3, 3, 2, 4, 2, int64(1))
	f.Add(9, 6, 4, 8, 5, int64(42))
	f.Fuzz(func(t *testing.T, n, k, r, d, w int, seed int64) {
		k = clamp(k, 1, 8)
		n = clamp(n, k, k+8)
		r = clamp(r, 1, 32)
		d = clamp(d, 1, 128)
		w = clamp(w, 1, 32)

		rng := rand.New(rand.NewSource(seed))
		g := NewGenerator(n, k)

		blocks := make([]*mat.Dense, k)
		for j := range blocks {
			blocks[j] = randMatrix(r, d, rng)
		}
		A := mat.NewDense(k*r, d, nil)
		for j, b := range blocks {
			A.Slice(j*r, (j+1)*r, 0, d).(*mat.Dense).Copy(b)
		}
		B := randMatrix(d, w, rng)
		var want mat.Dense
		want.Mul(A, B)

		results := make(map[int]*mat.Dense, n)
		for i := 0; i < n; i++ {
			Ahat := g.EncodeBlock(i, blocks)
			var cancelled atomic.Bool
			Y, _ := TiledMultiply(Ahat, B, r, d, w, 1, &cancelled, func(float64) {})
			results[i] = Y
		}

		got, err := Decode(g, results)
		if err != nil {
			t.Fatalf("decode: %v", err)
		}
		if !mat.EqualApprox(got, &want, 1e-6) {
			t.Fatalf("mismatch: n=%d k=%d r=%d d=%d w=%d seed=%d", n, k, r, d, w, seed)
		}
	})
}
