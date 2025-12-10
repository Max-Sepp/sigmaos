#!/bin/bash

VERSION=OSDI26

ROOT_DIR=$(realpath $(dirname $0)/../../..)
RES_OUT_DIR=$ROOT_DIR/benchmarks/results/$VERSION
GRAPH_SCRIPTS_DIR=$ROOT_DIR/benchmarks/scripts/graph
GRAPH_OUT_DIR=$ROOT_DIR/benchmarks/results/graphs

#echo "..............................................Cached, no initscript.............................................."
#./benchmarks/scripts/graph/start-latency-breakdown-setup-init.py \
#  --start \
#  --dir_path benchmarks/results/NEXT/start_latency_cached \
#  --proc_name cached-srv-cpp
#echo "..............................................Cached, initscript.............................................."
#./benchmarks/scripts/graph/start-latency-breakdown-setup-init.py \
#  --start \
#  --dir_path benchmarks/results/NEXT/start_latency_cached_initscript \
#  --proc_name cached-srv-cpp
#printf "\n\n\n"

echo "Generating imgresize time comparison..."
./benchmarks/scripts/graph/imgresize-time.py \
   --dir_path_noinitscripts $RES_OUT_DIR/img_process \
   --dir_path_initscripts $RES_OUT_DIR/img_process_initscript \
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
   --initscript_dir $RES_OUT_DIR/img_process_initscript_writeout \
   --noinitscript_dir $RES_OUT_DIR/img_process_initscript \
   --output $GRAPH_OUT_DIR/imgresize-cost-writeout.pdf
echo "Done generating imgresize writeout cost comparison..."

echo "Generating cached start latency breakdown graph..."
./benchmarks/scripts/graph/start-latency-breakdown-timeline.py \
  --paper \
  --dir_path_1 benchmarks/results/NEXT/start_latency_cached \
  --proc_name_1 cached-srv-cpp \
  --label_1 "Cached" \
  --dir_path_2 benchmarks/results/NEXT/start_latency_cached_initscript \
  --proc_name_2 cached-srv-cpp \
  --label_2 "Cached (co-sandbox)" \
  --output $GRAPH_OUT_DIR/cached-start-latency-breakdown-timeline.pdf
echo "Done generating cached start latency breakdown graph..."

echo "Generating memcached start latency breakdown graph..."
./benchmarks/scripts/graph/start-latency-breakdown-timeline.py \
  --paper \
  --dir_path_1 benchmarks/results/NEXT/start_latency_memcached \
  --proc_name_1 memcached-shim \
  --label_1 "Memcached" \
  --dir_path_2 benchmarks/results/NEXT/start_latency_memcached_initscript \
  --proc_name_2 memcached-shim \
  --label_2 "Memcached (co-sandbox)" \
  --output $GRAPH_OUT_DIR/memcached-start-latency-breakdown-timeline.pdf
echo "Done generating memcached start latency breakdown graph..."
