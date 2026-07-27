package mdscode

import "gonum.org/v1/gonum/mat"

// EncodeBlock forms Ã_i = Σ_j G[i][j]·blocks[j] for worker i (0-indexed),
// given the K systematic row-stripes of A. Systematic workers (i < K) get
// blocks[i] back directly (aliased, not copied); parity workers get the
// linear combination.
func (g *Generator) EncodeBlock(i int, blocks []*mat.Dense) *mat.Dense {
	if i < g.K {
		return blocks[i]
	}
	row := g.Row(i)
	r, c := blocks[0].Dims()
	acc := mat.NewDense(r, c, nil)
	var scaled mat.Dense
	for j, coef := range row {
		if coef == 0 {
			continue
		}
		scaled.Scale(coef, blocks[j])
		acc.Add(acc, &scaled)
	}
	return acc
}
