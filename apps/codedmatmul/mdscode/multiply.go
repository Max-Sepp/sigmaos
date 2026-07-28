package mdscode

import (
	"sync/atomic"

	"gonum.org/v1/gonum/mat"
)

// TiledMultiply computes Y (rxW) = Ahat (rxD) * B (DxW) in T slabs of width
// D/T, publishing progress and checking cancellation between slabs. It returns
// the (possibly partial) result and the number of slabs completed, so a
// cancelled caller can still account for wasted work.
func TiledMultiply(Ahat, B *mat.Dense, r, D, W, T int, cancelled *atomic.Bool,
	publish func(frac float64)) (*mat.Dense, int) {
	Y := mat.NewDense(r, W, nil)
	d := D / T
	var acc mat.Dense
	for t := 0; t < T; t++ {
		if cancelled.Load() {
			return Y, t
		}
		As := Ahat.Slice(0, r, t*d, (t+1)*d).(*mat.Dense)
		Bs := B.Slice(t*d, (t+1)*d, 0, W).(*mat.Dense)
		acc.Mul(As, Bs)
		Y.Add(Y, &acc)
		publish(float64(t+1) / float64(T))
	}
	return Y, T
}
