// Package testutil provides shared test helpers for imgrec tests.
package testutil

import (
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"

	s3clnt "sigmaos/proxy/s3/clnt"
	"sigmaos/sigmaclnt/fslib"
)

const (
	modelTmpPath = "/tmp/imgrec_model.onnx"
	imgTmpPath   = "/tmp/imgrec_img.jpg"
	// ScoreTolerance is the maximum allowed absolute difference between the
	// reference score and the proc's score. onnxruntime and tract-onnx use
	// different operator implementations so small differences are expected.
	ScoreTolerance = 4e-1
)

var (
	refOnce   sync.Once
	refResult string
	refErr    error
)

// GetReferenceOutput downloads the model and image from S3 on the first call,
// runs imgrec_linux.py, and returns the cached "class_idx,score" string.
func GetReferenceOutput(t *testing.T, fsl *fslib.FsLib, imgBucket, imgKey, modelBucket, modelKey, kid string) string {
	t.Helper()
	refOnce.Do(func() {
		refResult, refErr = computeReferenceOutput(fsl, imgBucket, imgKey, modelBucket, modelKey, kid)
	})
	if !assert.Nil(t, refErr, "reference imgrec_linux.py failed: %v", refErr) {
		t.FailNow()
	}
	return refResult
}

// AssertMatchesReference parses procMsg ("class_idx,score") and the cached
// reference output and asserts they agree on class index and score.
func AssertMatchesReference(t *testing.T, procMsg string, refMsg string) {
	t.Helper()
	refIdx, refScore, err := parseResult(refMsg)
	if !assert.Nil(t, err, "parse reference result %q: %v", refMsg, err) {
		return
	}
	procIdx, procScore, err := parseResult(procMsg)
	if !assert.Nil(t, err, "parse proc result %q: %v", procMsg, err) {
		return
	}
	assert.Equal(t, refIdx, procIdx, "class_idx mismatch: ref=%d proc=%d", refIdx, procIdx)
	assert.True(t, math.Abs(refScore-procScore) <= ScoreTolerance,
		"score mismatch: ref=%.6f proc=%.6f diff=%.6f (tolerance %.6f)",
		refScore, procScore, math.Abs(refScore-procScore), ScoreTolerance)
}

func parseResult(s string) (int, float64, error) {
	parts := strings.SplitN(strings.TrimSpace(s), ",", 2)
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("expected class_idx,score got %q", s)
	}
	idx, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0, 0, fmt.Errorf("parse class_idx %q: %w", parts[0], err)
	}
	score, err := strconv.ParseFloat(parts[1], 64)
	if err != nil {
		return 0, 0, fmt.Errorf("parse score %q: %w", parts[1], err)
	}
	return idx, score, nil
}

func computeReferenceOutput(fsl *fslib.FsLib, imgBucket, imgKey, modelBucket, modelKey, kid string) (string, error) {
	pn := "name/s3/" + kid
	sc, err := s3clnt.NewS3Clnt(fsl, pn)
	if err != nil {
		return "", fmt.Errorf("NewS3Clnt: %w", err)
	}

	modelBytes, err := sc.GetObject(modelBucket, modelKey, false)
	if err != nil {
		return "", fmt.Errorf("GetObject model: %w", err)
	}
	imgBytes, err := sc.GetObject(imgBucket, imgKey, false)
	if err != nil {
		return "", fmt.Errorf("GetObject image: %w", err)
	}

	if err := os.WriteFile(modelTmpPath, modelBytes, 0644); err != nil {
		return "", fmt.Errorf("write model: %w", err)
	}
	if err := os.WriteFile(imgTmpPath, imgBytes, 0644); err != nil {
		return "", fmt.Errorf("write image: %w", err)
	}

	out, err := exec.Command("python3", imgrecLinuxScriptPath(), modelTmpPath, imgTmpPath).Output()
	if err != nil {
		return "", fmt.Errorf("run imgrec_linux.py: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

func imgrecLinuxScriptPath() string {
	_, b, _, _ := runtime.Caller(0)
	// b = .../apps/imgrec/testutil/testutil.go
	// go up two levels to .../apps/imgrec, then into py/
	imgrec := filepath.Dir(filepath.Dir(b))
	return filepath.Join(imgrec, "py", "imgrec_linux.py")
}
