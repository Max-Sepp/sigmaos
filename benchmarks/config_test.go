package benchmarks_test

import (
	"encoding/json"
	"flag"
	"os"
	"testing"

	"sigmaos/benchmarks"
	db "sigmaos/debug"
	sp "sigmaos/sigmap"
)

var CosSimBenchConfig *benchmarks.CosSimBenchConfig
var CacheBenchConfig *benchmarks.CacheBenchConfig
var HotelBenchConfig *benchmarks.HotelBenchConfig
var ImgBenchConfig *benchmarks.ImgBenchConfig
var EtcdBenchConfig *benchmarks.EtcdBenchConfig
var MemcachedBenchConfig *benchmarks.MemcachedBenchConfig
var StartLatencyBenchConfig *benchmarks.StartLatencyBenchConfig

var cossimBenchCfgStr string
var cacheBenchCfgStr string
var hotelBenchCfgStr string
var imgBenchCfgStr string
var etcdBenchCfgStr string
var memcachedBenchCfgStr string
var startLatencyBenchCfgStr string

func init() {
	flag.StringVar(&cossimBenchCfgStr, "cossim_bench_cfg", sp.NOT_SET, "JSON string for CosSimBenchConfig")
	flag.StringVar(&cacheBenchCfgStr, "cache_bench_cfg", sp.NOT_SET, "JSON string for CacheBenchConfig")
	flag.StringVar(&hotelBenchCfgStr, "hotel_bench_cfg", sp.NOT_SET, "JSON string for HotelBenchConfig")
	flag.StringVar(&imgBenchCfgStr, "img_bench_cfg", sp.NOT_SET, "JSON string for ImgBenchConfig")
	flag.StringVar(&etcdBenchCfgStr, "etcd_bench_cfg", sp.NOT_SET, "JSON string for EtcdBenchConfig")
	flag.StringVar(&memcachedBenchCfgStr, "memcached_bench_cfg", sp.NOT_SET, "JSON string for MemcachedBenchConfig")
	flag.StringVar(&startLatencyBenchCfgStr, "start_latency_bench_cfg", sp.NOT_SET, "JSON string for StartLatencyBenchConfig")
}

func TestMain(m *testing.M) {
	flag.Parse()

	// Parse CosSimBenchConfig
	if cossimBenchCfgStr == sp.NOT_SET {
		CosSimBenchConfig = benchmarks.DefaultCosSimBenchConfig
		db.DPrintf(db.ALWAYS, "Using default CosSimBenchConfig")
	} else {
		err := json.Unmarshal([]byte(cossimBenchCfgStr), &CosSimBenchConfig)
		if err != nil {
			db.DFatalf("Error unmarshaling cossim_bench_cfg: %v", err)
		}
		db.DPrintf(db.ALWAYS, "Loaded CosSimBenchConfig")
	}

	// Parse CacheBenchConfig
	if cacheBenchCfgStr == sp.NOT_SET {
		CacheBenchConfig = benchmarks.DefaultCacheBenchConfig
		db.DPrintf(db.ALWAYS, "Using default CacheBenchConfig")
	} else {
		err := json.Unmarshal([]byte(cacheBenchCfgStr), &CacheBenchConfig)
		if err != nil {
			db.DFatalf("Error unmarshaling cache_bench_cfg: %v", err)
		}
		db.DPrintf(db.ALWAYS, "Loaded CacheBenchConfig")
	}

	// Parse HotelBenchConfig
	if hotelBenchCfgStr == sp.NOT_SET {
		HotelBenchConfig = benchmarks.DefaultHotelBenchConfig
		db.DPrintf(db.ALWAYS, "Using default HotelBenchConfig")
	} else {
		err := json.Unmarshal([]byte(hotelBenchCfgStr), &HotelBenchConfig)
		if err != nil {
			db.DFatalf("Error unmarshaling hotel_bench_cfg: %v", err)
		}
		db.DPrintf(db.ALWAYS, "Loaded HotelBenchConfig")
	}

	// Parse ImgBenchConfig
	if imgBenchCfgStr == sp.NOT_SET {
		ImgBenchConfig = benchmarks.DefaultImgBenchConfig
		db.DPrintf(db.ALWAYS, "Using default ImgBenchConfig")
	} else {
		err := json.Unmarshal([]byte(imgBenchCfgStr), &ImgBenchConfig)
		if err != nil {
			db.DFatalf("Error unmarshaling img_bench_cfg: %v", err)
		}
		db.DPrintf(db.ALWAYS, "Loaded ImgBenchConfig")
	}

	// Parse EtcdBenchConfig
	if etcdBenchCfgStr == sp.NOT_SET {
		EtcdBenchConfig = benchmarks.DefaultEtcdBenchConfig
		db.DPrintf(db.ALWAYS, "Using default EtcdBenchConfig")
	} else {
		err := json.Unmarshal([]byte(etcdBenchCfgStr), &EtcdBenchConfig)
		if err != nil {
			db.DFatalf("Error unmarshaling etcd_bench_cfg: %v", err)
		}
		db.DPrintf(db.ALWAYS, "Loaded EtcdBenchConfig")
	}

	// Parse MemcachedBenchConfig
	if memcachedBenchCfgStr == sp.NOT_SET {
		MemcachedBenchConfig = benchmarks.DefaultMemcachedBenchConfig
		db.DPrintf(db.ALWAYS, "Using default MemcachedBenchConfig")
	} else {
		err := json.Unmarshal([]byte(memcachedBenchCfgStr), &MemcachedBenchConfig)
		if err != nil {
			db.DFatalf("Error unmarshaling memcached_bench_cfg: %v", err)
		}
		db.DPrintf(db.ALWAYS, "Loaded MemcachedBenchConfig")
	}

	// Parse StartLatencyBenchConfig
	if startLatencyBenchCfgStr == sp.NOT_SET {
		StartLatencyBenchConfig = benchmarks.DefaultStartLatencyBenchConfig
		db.DPrintf(db.ALWAYS, "Using default StartLatencyBenchConfig")
	} else {
		err := json.Unmarshal([]byte(startLatencyBenchCfgStr), &StartLatencyBenchConfig)
		if err != nil {
			db.DFatalf("Error unmarshaling start_latency_bench_cfg: %v", err)
		}
		db.DPrintf(db.ALWAYS, "Loaded StartLatencyBenchConfig")
	}

	CosSimBenchConfig.JobCfg.CacheCfg = CacheBenchConfig.JobCfg
	HotelBenchConfig.JobCfg.CacheCfg = CacheBenchConfig.JobCfg
	HotelBenchConfig.CosSimBenchCfg = CosSimBenchConfig
	HotelBenchConfig.CacheBenchCfg = CacheBenchConfig

	// Pretty-print configs as JSON
	cacheJSON, err := json.MarshalIndent(CacheBenchConfig, "", "  ")
	if err != nil {
		db.DFatalf("Error marshaling CacheBenchConfig: %v", err)
	}
	db.DPrintf(db.ALWAYS, "CacheBenchConfig:\n%s", string(cacheJSON))

	cossimJSON, err := json.MarshalIndent(CosSimBenchConfig, "", "  ")
	if err != nil {
		db.DFatalf("Error marshaling CosSimBenchConfig: %v", err)
	}
	db.DPrintf(db.ALWAYS, "CosSimBenchConfig:\n%s", string(cossimJSON))

	hotelJSON, err := json.MarshalIndent(HotelBenchConfig, "", "  ")
	if err != nil {
		db.DFatalf("Error marshaling HotelBenchConfig: %v", err)
	}
	db.DPrintf(db.ALWAYS, "HotelBenchConfig:\n%s", string(hotelJSON))

	imgJSON, err := json.MarshalIndent(ImgBenchConfig, "", "  ")
	if err != nil {
		db.DFatalf("Error marshaling ImgBenchConfig: %v", err)
	}
	db.DPrintf(db.ALWAYS, "ImgBenchConfig:\n%s", string(imgJSON))

	etcdJSON, err := json.MarshalIndent(EtcdBenchConfig, "", "  ")
	if err != nil {
		db.DFatalf("Error marshaling EtcdBenchConfig: %v", err)
	}
	db.DPrintf(db.ALWAYS, "EtcdBenchConfig:\n%s", string(etcdJSON))

	memcachedJSON, err := json.MarshalIndent(MemcachedBenchConfig, "", "  ")
	if err != nil {
		db.DFatalf("Error marshaling MemcachedBenchConfig: %v", err)
	}
	db.DPrintf(db.ALWAYS, "MemcachedBenchConfig:\n%s", string(memcachedJSON))

	startLatencyJSON, err := json.MarshalIndent(StartLatencyBenchConfig, "", "  ")
	if err != nil {
		db.DFatalf("Error marshaling StartLatencyBenchConfig: %v", err)
	}
	db.DPrintf(db.ALWAYS, "StartLatencyBenchConfig:\n%s", string(startLatencyJSON))

	os.Exit(m.Run())
}
