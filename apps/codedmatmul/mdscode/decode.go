package mdscode

import (
	"fmt"
	"sort"

	"gonum.org/v1/gonum/mat"
)

// Decode reconstructs C = A*B from any K finished worker results (keyed by
// worker index). It uses the lowest-indexed K results, builds the KxK submatrix
// G_S of their generator rows, and solves Y_sys = G_S^-1 * Y_S.
func Decode(g *Generator, results map[int]*mat.Dense) (*mat.Dense, error) {
	if len(results) < g.K {
		return nil, fmt.Errorf("mdscode: need %d results to decode, got %d", g.K, len(results))
	}

	idxs := make([]int, 0, len(results))
	for i := range results {
		idxs = append(idxs, i)
	}
	sort.Ints(idxs)
	idxs = idxs[:g.K]

	GS := mat.NewDense(g.K, g.K, nil)
	for row, i := range idxs {
		copy(GS.RawRowView(row), g.Row(i))
	}
	var GSInv mat.Dense
	if err := GSInv.Inverse(GS); err != nil {
		return nil, fmt.Errorf("mdscode: G_S not invertible: %w", err)
	}

	r, w := results[idxs[0]].Dims()
	C := mat.NewDense(g.K*r, w, nil)
	var scaled mat.Dense
	for p := 0; p < g.K; p++ {
		block := mat.NewDense(r, w, nil)
		for q := 0; q < g.K; q++ {
			coef := GSInv.At(p, q)
			if coef == 0 {
				continue
			}
			scaled.Scale(coef, results[idxs[q]])
			block.Add(block, &scaled)
		}
		C.Slice(p*r, (p+1)*r, 0, w).(*mat.Dense).Copy(block)
	}
	return C, nil
}
