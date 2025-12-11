#!/bin/bash

VERSION=OSDI26

ROOT_DIR=$(realpath $(dirname $0)/../../..)
RES_OUT_DIR=$ROOT_DIR/benchmarks/results/$VERSION
GRAPH_SCRIPTS_DIR=$ROOT_DIR/benchmarks/scripts/graph
GRAPH_OUT_DIR=$ROOT_DIR/benchmarks/results/graphs

#echo "..............................................Cached, no initscript.............................................."
#./benchmarks/scripts/graph/start-latency-breakdown-setup-init.py \
#  --start \
#  --dir_path benchmarks/results/$VERSION/start_latency_cached \
#  --proc_name cached-srv-cpp
#echo "..............................................Cached, initscript.............................................."
#./benchmarks/scripts/graph/start-latency-breakdown-setup-init.py \
#  --start \
#  --dir_path benchmarks/results/$VERSION/start_latency_cached_initscript \
#  --proc_name cached-srv-cpp
#printf "\n\n\n"

echo "Generating imgresize time comparison..."
./benchmarks/scripts/graph/imgresize-time.py \
   --dir_path_noinitscripts $RES_OUT_DIR/img_process_gvisor \
   --dir_path_initscripts $RES_OUT_DIR/img_process_gvisor_initscript \
   --dir_path_initscripts_writeout $RES_OUT_DIR/img_process_gvisor_initscript_writeout \
   --output $GRAPH_OUT_DIR/imgresize-time.pdf
echo "Done generating imgresize time comparison..."

echo "Generating start latency comparison..."
./benchmarks/scripts/graph/start-latency-initscript-bar-graph.py \
  --dir_path_etcd $RES_OUT_DIR/start_latency_etcd \
  --dir_path_etcd_initscript $RES_OUT_DIR/start_latency_etcd_initscript \
  --dir_path_memcached $RES_OUT_DIR/start_latency_memcached \
  --dir_path_memcached_initscript $RES_OUT_DIR/start_latency_memcached_initscript \
  --dir_path_vecdb $RES_OUT_DIR/start_latency_cossim \
  --dir_path_vecdb_initscript $RES_OUT_DIR/start_latency_cossim_initscript \
  --dir_path_cached $RES_OUT_DIR/start_latency_cached \
  --dir_path_cached_initscript $RES_OUT_DIR/start_latency_cached_initscript \
  --output $GRAPH_OUT_DIR/start-latency.pdf
echo "Done generating start latency comparison..."

echo "Generating imgresize mem usage comparison..."
./benchmarks/scripts/graph/imgresize-mem-usage.py \
   --input_dir $RES_OUT_DIR/img_process_sequential_gvisor_initscript_pss \
   --output $GRAPH_OUT_DIR/imgresize-mem-usage.pdf
echo "Done generating imgresize mem usage comparison..."

echo "Generating imgresize writeout cost comparison..."
./benchmarks/scripts/graph/imgresize-cost-writeout.py \
   --initscript_dir $RES_OUT_DIR/img_process_gvisor_initscript_writeout \
   --noinitscript_dir $RES_OUT_DIR/img_process_gvisor_initscript \
   --output $GRAPH_OUT_DIR/imgresize-cost-writeout.pdf
echo "Done generating imgresize writeout cost comparison..."

echo "Generating vecdb start latency breakdown simplified graph..."
./benchmarks/scripts/graph/start-latency-breakdown-timeline.py \
  --paper \
  --dir_path_1 benchmarks/results/$VERSION/start_latency_cossim \
  --proc_name_1 cossim-srv-cpp \
  --label_1 "VecDB" \
  --combine_1 "InitSPProxyConn" "ConnectionSetup" \
  --relabel_1 "ConnectionSetup" "ConnectionSetup-2" \
  --relabel_1 "InitSPProxyConn" "ConnectionSetup-1" \
  --relabel_1 "DownloadInitScript" "DownloadCoSandbox" \
  --omit_1 "GlobalScheduling" \
  --dir_path_2 benchmarks/results/$VERSION/start_latency_cossim_initscript \
  --proc_name_2 cossim-srv-cpp \
  --label_2 "VecDB (co-sandbox)" \
  --relabel_2 "ConnectionSetup" "ConnectionSetup-1" \
  --relabel_2 "InitSPProxyConn" "ConnectionSetup-2" \
  --relabel_2 "DownloadInitScript" "DownloadCoSandbox" \
  --omit_2 "GlobalScheduling" \
  --subtract_1_from_2 "GlobalScheduling" "DownloadInitScript" \
  --simplified \
  --output $GRAPH_OUT_DIR/vecdb-start-latency-breakdown-timeline-simplified.pdf
echo "Done generating vecdb start latency breakdown simplified graph..."

echo "Generating cached start latency breakdown graph..."
./benchmarks/scripts/graph/start-latency-breakdown-timeline.py \
  --paper \
  --dir_path_1 benchmarks/results/$VERSION/start_latency_cached \
  --proc_name_1 cached-srv-cpp \
  --label_1 "Cached" \
  --combine_1 "InitSPProxyConn" "ConnectionSetup" \
  --relabel_1 "ConnectionSetup" "ConnectionSetup-2" \
  --relabel_1 "InitSPProxyConn" "ConnectionSetup-1" \
  --relabel_1 "DownloadInitScript" "DownloadCoSandbox" \
  --omit_1 "GlobalScheduling" \
  --dir_path_2 benchmarks/results/$VERSION/start_latency_cached_initscript_noshmem \
  --proc_name_2 cached-srv-cpp \
  --label_2 "Cached (co-sandbox, no shmem)" \
  --relabel_2 "ConnectionSetup" "ConnectionSetup-1" \
  --relabel_2 "InitSPProxyConn" "ConnectionSetup-2" \
  --relabel_2 "DownloadInitScript" "DownloadCoSandbox" \
  --omit_2 "GlobalScheduling" \
  --subtract_1_from_2 "GlobalScheduling" "DownloadInitScript" \
  --dir_path_3 benchmarks/results/$VERSION/start_latency_cached_initscript \
  --proc_name_3 cached-srv-cpp \
  --label_3 "Cached (co-sandbox, shmem)" \
  --relabel_3 "ConnectionSetup" "ConnectionSetup-1" \
  --relabel_3 "InitSPProxyConn" "ConnectionSetup-2" \
  --relabel_3 "DownloadInitScript" "DownloadCoSandbox" \
  --omit_3 "GlobalScheduling" \
  --subtract_1_from_3 "GlobalScheduling" "DownloadInitScript" \
  --output $GRAPH_OUT_DIR/cached-start-latency-breakdown-timeline.pdf
echo "Done generating cached start latency breakdown graph..."

echo "Generating memcached start latency breakdown graph..."
./benchmarks/scripts/graph/start-latency-breakdown-timeline.py \
  --paper \
  --dir_path_1 benchmarks/results/$VERSION/start_latency_memcached \
  --proc_name_1 memcached-shim \
  --label_1 "Memcached" \
  --omit_1 "GlobalScheduling" \
  --relabel_1 "DownloadInitScript" "DownloadCoSandbox" \
  --dir_path_2 benchmarks/results/$VERSION/start_latency_memcached_initscript \
  --proc_name_2 memcached-shim \
  --label_2 "Memcached (co-sandbox)" \
  --omit_2 "GlobalScheduling" \
  --relabel_2 "DownloadInitScript" "DownloadCoSandbox" \
  --subtract_1_from_2 "GlobalScheduling" "DownloadInitScript" \
  --output $GRAPH_OUT_DIR/memcached-start-latency-breakdown-timeline.pdf
echo "Done generating memcached start latency breakdown graph..."

echo "Imgresize breakdown..."
./benchmarks/scripts/graph/imgresize-time-breakdown.py \
  --input_dir $RES_OUT_DIR/img_process_gvisor
echo "Imgresize breakdown..."

echo "Imgresize (initscript) breakdown..."
./benchmarks/scripts/graph/imgresize-time-breakdown.py \
  --input_dir $RES_OUT_DIR/img_process_gvisor_initscript
echo "Imgresize (initscript) breakdown..."

echo "Imgresize (writeout) breakdown..."
./benchmarks/scripts/graph/imgresize-time-breakdown.py \
  --input_dir $RES_OUT_DIR/img_process_gvisor_initscript_writeout
echo "Imgresize (writeout) breakdown..."

#echo "..............................................Cached, no initscript.............................................."
#./benchmarks/scripts/graph/start-latency-breakdown-setup-init.py \
#  --start \
#  --dir_path benchmarks/results/$VERSION/start_latency_cossim \
#  --proc_name cossim-srv-cpp
#echo "..............................................Cached, initscript.............................................."
#./benchmarks/scripts/graph/start-latency-breakdown-setup-init.py \
#  --start \
#  --dir_path benchmarks/results/$VERSION/start_latency_cossim_initscript \
#  --proc_name cossim-srv-cpp
#printf "\n\n\n"
