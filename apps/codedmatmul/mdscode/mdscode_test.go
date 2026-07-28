package mdscode

import (
	"fmt"
	"math/rand"
	"sync/atomic"
	"testing"

	"gonum.org/v1/gonum/mat"
)

func randMatrix(r, c int, rng *rand.Rand) *mat.Dense {
	data := make([]float64, r*c)
	for i := range data {
		data[i] = rng.Float64()*2 - 1
	}
	return mat.NewDense(r, c, data)
}

// checkRoundTrip generates a random A (K*r x D) / B (D x W), encodes and
// tiled-multiplies it across N workers, and asserts Decode reconstructs A*B
// exactly from both an all-systematic and a parity-including subset.
func checkRoundTrip(t *testing.T, N, K, r, D, W, tiles int) {
	t.Helper()
	rng := rand.New(rand.NewSource(1))
	g := NewGenerator(N, K)

	blocks := make([]*mat.Dense, K)
	for j := range blocks {
		blocks[j] = randMatrix(r, D, rng)
	}
	A := mat.NewDense(K*r, D, nil)
	for j, b := range blocks {
		A.Slice(j*r, (j+1)*r, 0, D).(*mat.Dense).Copy(b)
	}
	B := randMatrix(D, W, rng)
	var want mat.Dense
	want.Mul(A, B)

	results := make(map[int]*mat.Dense, N)
	for i := 0; i < N; i++ {
		Ahat := g.EncodeBlock(i, blocks)
		var cancelled atomic.Bool
		Y, slabs := TiledMultiply(Ahat, B, r, D, W, tiles, &cancelled, func(float64) {})
		if slabs != tiles {
			t.Fatalf("worker %d: expected %d slabs, got %d", i, tiles, slabs)
		}
		results[i] = Y
	}

	all := make([]int, N)
	for i := range all {
		all[i] = i
	}
	subsets := [][]int{all[:K]} // first K, all-systematic
	if N > K {
		subsets = append(subsets, all[N-K:]) // last K, includes parity
	}

	for _, s := range subsets {
		sub := make(map[int]*mat.Dense, len(s))
		for _, i := range s {
			sub[i] = results[i]
		}
		got, err := Decode(g, sub)
		if err != nil {
			t.Fatalf("decode subset %v: %v", s, err)
		}
		if !mat.EqualApprox(got, &want, 1e-6) {
			t.Fatalf("decode subset %v: mismatch", s)
		}
	}
}

func TestRoundTrip(t *testing.T) {
	cases := []struct{ N, K, r, D, W int }{
		{N: 3, K: 3, r: 4, D: 8, W: 5}, // no redundancy
		{N: 5, K: 3, r: 4, D: 8, W: 5},
		{N: 9, K: 6, r: 4, D: 8, W: 5},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprintf("N=%d,K=%d", tc.N, tc.K), func(t *testing.T) {
			checkRoundTrip(t, tc.N, tc.K, tc.r, tc.D, tc.W, 4)
		})
	}
}

// TestRoundTripLarge exercises the same encode/decode path at a scale (roughly
// 1000x1000 matrices) closer to the benchmark's real defaults
// (apps/codedmatmul, D=65536), to catch anything that only shows up with large
// dimensions (e.g. Vandermonde conditioning, slicing bugs).
func TestRoundTripLarge(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping large matmul round trip in -short mode")
	}
	checkRoundTrip(t, 9, 6, 167, 1000, 1000, 8)
}

func TestDecodeInsufficientResults(t *testing.T) {
	g := NewGenerator(5, 3)
	results := map[int]*mat.Dense{
		0: mat.NewDense(2, 2, nil),
		1: mat.NewDense(2, 2, nil),
	}
	if _, err := Decode(g, results); err == nil {
		t.Fatal("expected error decoding with fewer than K results")
	}
}
