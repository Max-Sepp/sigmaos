package pagerank

import (
	"encoding/json"
	"path/filepath"
	"sigmaos/proc"
	"sigmaos/serr"
	"sigmaos/test"
	"testing"

	"github.com/stretchr/testify/assert"

	sp "sigmaos/sigmap"

	db "sigmaos/debug"
)

func TestCompile(t *testing.T) {
}

func TestBasicGraph(t *testing.T) {
	// wikiGraph: the canonical PageRank example from Wikipedia, with nodes
	// A..K mapped to 1..11. Node 1 (A) has no outbound edges, so this graph
	// also exercises dangling-node handling.
	//
	//	B <-> C, D -> {A,B}, E -> {D,B,F}, F -> {B,E}, G -> {B,E}, H -> {B,E}, I -> {B,E}, J -> E, K -> E
	wikiGraph := []*GraphNode{
		{4, 1, 1}, {4, 2, 1}, // D -> A, B
		{3, 2, 1},                       // C  ->  B
		{2, 3, 1},                       // B  ->  C
		{5, 4, 1}, {5, 2, 1}, {5, 6, 1}, // E  ->  D, B, F
		{6, 2, 1}, {6, 5, 1}, // F  ->  B, E
		{7, 2, 1}, {7, 5, 1}, // G  ->  B, E
		{8, 2, 1}, {8, 5, 1}, // H  ->  B, E
		{9, 2, 1}, {9, 5, 1}, // I  ->  B, E
		{10, 5, 1}, // J  ->  E
		{11, 5, 1}, // K  ->  E
	}

	db.DPrintf(db.TEST, "Wiki Graph created")

	wikiGraphData, err := json.Marshal(wikiGraph)
	if !assert.Nil(t, err, "Error marshalling graph: %v", err) {
		return
	}

	db.DPrintf(db.TEST, "Marshalled to json: %s", string(wikiGraphData))

	mrts, err1 := test.NewMultiRealmTstate(t, []sp.Trealm{test.REALM1})
	if !assert.Nil(t, err1, "Error New Tstate: %v", err1) {
		return
	}
	defer mrts.Shutdown()

	db.DPrintf(db.TEST, "MultiRealmTstate created")

	err = mrts.GetRealm(test.REALM1).MkDir(filepath.Join(sp.S3, sp.LOCAL, "9ps3/pagerank"), 0777)
	if !assert.False(t, err != nil && !serr.IsErrorExists(err), "Error creating directory: %v", err) {
		return
	}

	db.DPrintf(db.TEST, "Directory created")

	in := filepath.Join(sp.S3, sp.LOCAL, "9ps3/pagerank/graph.json")
	_, err = mrts.GetRealm(test.REALM1).PutFile(in, 0777, sp.ORDWR, wikiGraphData)
	if !assert.Nil(t, err, "Error putting graph file: %v", err) {
		return
	}

	db.DPrintf(db.TEST, "Graph file created")

	out := filepath.Join(sp.S3, sp.LOCAL, "9ps3/pagerank/ranks.json")

	p := proc.NewProc("pagerank", []string{in, out, "0.85", "0.0001"})
	err = mrts.GetRealm(test.REALM1).Spawn(p)
	if !assert.Nil(t, err, "Spawn") {
		return
	}

	db.DPrintf(db.TEST, "Pagerank process spawned")

	err = mrts.GetRealm(test.REALM1).WaitStart(p.GetPid())
	if !assert.Nil(t, err, "WaitStart error") {
		return
	}

	db.DPrintf(db.TEST, "Pagerank process started")

	status, err := mrts.GetRealm(test.REALM1).WaitExit(p.GetPid())
	if !assert.Nil(t, err, "WaitExit error %v", err) {
		return
	}
	if !assert.True(t, status.IsStatusOK(), "WaitExit status error: %v", status) {
		return
	}

	db.DPrintf(db.TEST, "Pagerank process exited OK")
}
