package hpsearch

import (
	"encoding/csv"
	"fmt"
	"os"
	"strconv"
)

// DumpCurvesCSV writes every curve's per-iteration scores to path in long
// format (one row per config/iter), so the curves can be loaded and plotted
// with any standard tool (pandas, gnuplot, etc.) instead of only being
// visible as an asymptote+count summary in the debug log. A pruned curve
// only contributes the iterations it actually ran.
func DumpCurvesCSV(curves []*Curve, path string) error {
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("DumpCurvesCSV: create %v err %w", path, err)
	}
	defer f.Close()

	w := csv.NewWriter(f)
	if err := w.Write([]string{"config_id", "iter", "score", "asymptote", "pruned", "pruned_at_iter"}); err != nil {
		return fmt.Errorf("DumpCurvesCSV: write header err %w", err)
	}
	for _, c := range curves {
		for i, s := range c.Scores {
			row := []string{
				strconv.Itoa(c.ConfigId),
				strconv.Itoa(i),
				strconv.FormatFloat(s, 'f', -1, 64),
				strconv.FormatFloat(c.Asymptote, 'f', -1, 64),
				strconv.FormatBool(c.Pruned),
				strconv.Itoa(c.PrunedAtIter),
			}
			if err := w.Write(row); err != nil {
				return fmt.Errorf("DumpCurvesCSV: write row err %w", err)
			}
		}
	}
	w.Flush()
	return w.Error()
}
