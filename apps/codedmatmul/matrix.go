package codedmatmul

import (
	"math/rand"

	"gonum.org/v1/gonum/mat"
)

// GenBlocks deterministically regenerates the K row-stripes of A (each r x D)
// and B (D x W) from seed. Every worker (and the benchmark test, for its
// reference C) calls this with the same seed, so they all reconstruct identical
// matrices without sharing any state.
func GenBlocks(seed int64, K, r, D, W int) (blocks []*mat.Dense, B *mat.Dense) {
	rng := rand.New(rand.NewSource(seed))
	blocks = make([]*mat.Dense, K)
	for j := range blocks {
		blocks[j] = randMatrix(r, D, rng)
	}
	B = randMatrix(D, W, rng)
	return blocks, B
}

func randMatrix(r, c int, rng *rand.Rand) *mat.Dense {
	data := make([]float64, r*c)
	for i := range data {
		data[i] = rng.Float64()*2 - 1
	}
	return mat.NewDense(r, c, data)
}
