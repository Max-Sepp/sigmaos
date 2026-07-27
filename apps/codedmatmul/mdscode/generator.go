// Package mdscode implements a systematic maximum-distance-separable (MDS)
// erasure code over Vandermonde parity rows, used to reconstruct the
// distributed matrix product C = A*B from any K of N worker results. It has no
// SigmaOS dependencies and is unit-testable with plain `go test`.
package mdscode

// Generator is the NxK systematic MDS generator matrix G: rows 0..K-1 are the
// identity (systematic workers), rows K..N-1 are Vandermonde parity rows
// (parity workers).
type Generator struct {
	N, K   int
	points []float64 // evaluation points for the N-K parity rows
}

// NewGenerator builds the generator for N workers, K of which are needed to
// reconstruct the result.
func NewGenerator(N, K int) *Generator {
	m := N - K
	points := make([]float64, m)
	for i := 0; i < m; i++ {
		points[i] = float64(i + 1)
	}
	return &Generator{N: N, K: K, points: points}
}

// Row returns the i-th (0-indexed) row of G, length K.
func (g *Generator) Row(i int) []float64 {
	row := make([]float64, g.K)
	if i < g.K {
		row[i] = 1
		return row
	}
	x := g.points[i-g.K]
	xp := 1.0
	for j := 0; j < g.K; j++ {
		row[j] = xp
		xp *= x
	}
	return row
}
