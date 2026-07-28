package codedmatmul

import (
	"fmt"
	"path"
	"strconv"

	db "sigmaos/debug"
	"sigmaos/sigmaclnt/fslib"
	sp "sigmaos/sigmap"
)

func progressPath(progressDir string, idx int) string {
	return path.Join(progressDir, strconv.Itoa(idx))
}

// publishProgress overwrites this worker's progress file with its latest
// fraction-of-D-processed hint. Best effort: a failed publish doesn't affect
// correctness, it just means the hint isn't visible.
func publishProgress(sc *fslib.FsLib, progressDir string, idx int, frac float64) {
	pn := progressPath(progressDir, idx)
	sc.Remove(pn)
	if _, err := sc.PutFile(pn, 0777, sp.OWRITE, []byte(fmt.Sprintf("%f", frac))); err != nil {
		db.DPrintf(db.CODEDMATMUL, "publishProgress: PutFile %v err %v", pn, err)
	}
}
