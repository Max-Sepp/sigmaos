package sebs_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"

	sebs "sigmaos/apps/sebs"
	sp "sigmaos/sigmap"
	"sigmaos/test"
)

func sebsCoSandboxRun(t *testing.T, mrts *test.MultiRealmTstate, conf *sebs.SebsWASMJobConfig) map[string]any {
	t.Helper()
	rts := mrts.GetRealm(test.REALM1)
	job, err := sebs.NewSebsWASMJob(conf, rts.SigmaClnt)
	if !assert.Nil(t, err, "NewSebsWASMJob: %v", err) {
		return nil
	}
	msg, err := job.Run(sp.NOT_SET)
	if !assert.Nil(t, err, "Run: %v", err) {
		return nil
	}
	var result map[string]any
	if !assert.Nil(t, json.Unmarshal([]byte(msg), &result), "unmarshal result") {
		return nil
	}
	return result
}

func TestSebsThumbnailerCoSandbox(t *testing.T) {
	mrts, err := test.NewMultiRealmTstate(t, []sp.Trealm{test.REALM1})
	if !assert.Nil(t, err, "NewMultiRealmTstate: %v", err) {
		return
	}
	defer mrts.Shutdown()

	rts := mrts.GetRealm(test.REALM1)
	sc := sebsS3Clnt(t, rts)

	inputPrefix := fmt.Sprintf("%s/210.thumbnailer/input/0", INPUT_BASE)
	outputPrefix := fmt.Sprintf("%s/210.thumbnailer/output/0", INPUT_BASE)
	imgKey := "test.jpg"
	s3Key := fmt.Sprintf("%s/%s", inputPrefix, imgKey)
	if err := sc.PutObject(SEBS_BUCKET, s3Key, makeTestJPEG()); !assert.Nil(t, err, "PutObject: %v", err) {
		return
	}

	eventMap := map[string]any{
		"bucket": map[string]any{
			"bucket": SEBS_BUCKET,
			"input":  inputPrefix,
			"output": outputPrefix,
		},
		"object": map[string]any{
			"key":    imgKey,
			"width":  200,
			"height": 200,
		},
	}
	eventJSON, _ := json.Marshal(eventMap)

	conf, err := sebs.NewSebsThumbnailerWASMJobConfig(string(eventJSON), SEBS_KID, 0, 128)
	if !assert.Nil(t, err, "NewSebsThumbnailerWASMJobConfig: %v", err) {
		return
	}
	result := sebsCoSandboxRun(t, mrts, conf)
	if result == nil {
		return
	}
	res, _ := result["result"].(map[string]any)
	key, _ := res["key"].(string)
	assert.NotEmpty(t, key, "thumbnailer cosandbox should return non-empty key")
}

func TestSebsVideoProcessingCoSandbox(t *testing.T) {
	dataDir := SEBS_DATA_DIR
	videoPath := filepath.Join(dataDir, "220.video-processing", "city.mp4")
	if dataDir == "" || !fileExists(videoPath) {
		t.Skipf("TestSebsVideoProcessingCoSandbox: set SEBS_DATA_DIR with city.mp4 to enable (missing: %v)", videoPath)
	}

	mrts, err := test.NewMultiRealmTstate(t, []sp.Trealm{test.REALM1})
	if !assert.Nil(t, err, "NewMultiRealmTstate: %v", err) {
		return
	}
	defer mrts.Shutdown()

	rts := mrts.GetRealm(test.REALM1)
	sc := sebsS3Clnt(t, rts)

	inputPrefix := fmt.Sprintf("%s/220.video-processing/input/0", INPUT_BASE)
	outputPrefix := fmt.Sprintf("%s/220.video-processing/output/0", INPUT_BASE)
	videoData, err := os.ReadFile(videoPath)
	if !assert.Nil(t, err, "ReadFile: %v", err) {
		return
	}
	s3Key := fmt.Sprintf("%s/city.mp4", inputPrefix)
	if err := sc.PutObject(SEBS_BUCKET, s3Key, videoData); !assert.Nil(t, err, "PutObject: %v", err) {
		return
	}

	eventMap := map[string]any{
		"bucket": map[string]any{
			"bucket": SEBS_BUCKET,
			"input":  inputPrefix,
			"output": outputPrefix,
		},
		"object": map[string]any{
			"key":      "city.mp4",
			"op":       "watermark",
			"duration": 1,
		},
	}
	eventJSON, _ := json.Marshal(eventMap)

	conf, err := sebs.NewSebsVideoProcessingWASMJobConfig(string(eventJSON), SEBS_KID, 0, 128)
	if !assert.Nil(t, err, "NewSebsVideoProcessingWASMJobConfig: %v", err) {
		return
	}
	result := sebsCoSandboxRun(t, mrts, conf)
	if result == nil {
		return
	}
	res, _ := result["result"].(map[string]any)
	key, _ := res["key"].(string)
	assert.NotEmpty(t, key, "video-processing cosandbox should return non-empty key")
}

func TestSebsImageRecognitionCoSandbox(t *testing.T) {
	dataDir := SEBS_DATA_DIR
	modelPath := filepath.Join(dataDir, "411.image-recognition", "model", "resnet50-19c8e357.pth")
	imgPath := filepath.Join(dataDir, "411.image-recognition", "fake-resnet", "800px-Porsche_991_silver_IAA.jpg")
	if dataDir == "" || !fileExists(modelPath) || !fileExists(imgPath) {
		t.Skipf("TestSebsImageRecognitionCoSandbox: set SEBS_DATA_DIR with model and image to enable")
	}

	mrts, err := test.NewMultiRealmTstate(t, []sp.Trealm{test.REALM1})
	if !assert.Nil(t, err, "NewMultiRealmTstate: %v", err) {
		return
	}
	defer mrts.Shutdown()

	rts := mrts.GetRealm(test.REALM1)
	sc := sebsS3Clnt(t, rts)

	modelPrefix := fmt.Sprintf("%s/411.image-recognition/input/0", INPUT_BASE)
	inputPrefix := fmt.Sprintf("%s/411.image-recognition/input/1", INPUT_BASE)
	modelKey := "resnet50-19c8e357.pth"
	imgKey := "800px-Porsche_991_silver_IAA.jpg"

	modelData, err := os.ReadFile(modelPath)
	if !assert.Nil(t, err, "ReadFile model: %v", err) {
		return
	}
	imgData, err := os.ReadFile(imgPath)
	if !assert.Nil(t, err, "ReadFile image: %v", err) {
		return
	}
	if err := sc.PutObject(SEBS_BUCKET, fmt.Sprintf("%s/%s", modelPrefix, modelKey), modelData); !assert.Nil(t, err) {
		return
	}
	if err := sc.PutObject(SEBS_BUCKET, fmt.Sprintf("%s/%s", inputPrefix, imgKey), imgData); !assert.Nil(t, err) {
		return
	}

	eventMap := map[string]any{
		"bucket": map[string]any{
			"bucket": SEBS_BUCKET,
			"model":  modelPrefix,
			"input":  inputPrefix,
		},
		"object": map[string]any{
			"model": modelKey,
			"input": imgKey,
		},
	}
	eventJSON, _ := json.Marshal(eventMap)

	// 256 MB shmem to accommodate the ~97 MB ResNet50 model.
	conf, err := sebs.NewSebsImageRecognitionWASMJobConfig(string(eventJSON), SEBS_KID, 0, 256)
	if !assert.Nil(t, err, "NewSebsImageRecognitionWASMJobConfig: %v", err) {
		return
	}
	result := sebsCoSandboxRun(t, mrts, conf)
	if result == nil {
		return
	}
	res, _ := result["result"].(map[string]any)
	assert.Equal(t, "sports_car", res["class"], "image-recognition cosandbox class mismatch")
	assert.Equal(t, float64(817), res["idx"], "image-recognition cosandbox idx mismatch")
}

func TestSebsDnaVisualisationCoSandbox(t *testing.T) {
	mrts, err := test.NewMultiRealmTstate(t, []sp.Trealm{test.REALM1})
	if !assert.Nil(t, err, "NewMultiRealmTstate: %v", err) {
		return
	}
	defer mrts.Shutdown()

	rts := mrts.GetRealm(test.REALM1)
	sc := sebsS3Clnt(t, rts)

	inputPrefix := fmt.Sprintf("%s/504.dna-visualisation/input/0", INPUT_BASE)
	outputPrefix := fmt.Sprintf("%s/504.dna-visualisation/output/0", INPUT_BASE)
	fastaKey := "test.fasta"
	s3Key := fmt.Sprintf("%s/%s", inputPrefix, fastaKey)
	if err := sc.PutObject(SEBS_BUCKET, s3Key, minimalFASTA()); !assert.Nil(t, err, "PutObject: %v", err) {
		return
	}

	eventMap := map[string]any{
		"bucket": map[string]any{
			"bucket": SEBS_BUCKET,
			"input":  inputPrefix,
			"output": outputPrefix,
		},
		"object": map[string]any{
			"key": fastaKey,
		},
	}
	eventJSON, _ := json.Marshal(eventMap)

	conf, err := sebs.NewSebsDnaVisualisationWASMJobConfig(string(eventJSON), SEBS_KID, 0, 128)
	if !assert.Nil(t, err, "NewSebsDnaVisualisationWASMJobConfig: %v", err) {
		return
	}
	result := sebsCoSandboxRun(t, mrts, conf)
	if result == nil {
		return
	}
	res, _ := result["result"].(map[string]any)
	key, _ := res["key"].(string)
	assert.NotEmpty(t, key, "dna-visualisation cosandbox should return non-empty key")
}
