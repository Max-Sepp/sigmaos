#!/bin/bash

VERSION=EUROSYS2027

ROOT_DIR=$(realpath $(dirname $0)/../../..)
RES_OUT_DIR=$ROOT_DIR/benchmarks/results/$VERSION
GRAPH_SCRIPTS_DIR=$ROOT_DIR/benchmarks/scripts/graph
GRAPH_OUT_DIR=$ROOT_DIR/benchmarks/results/graphs

#echo "..............................................Cached, no cosandbox.............................................."
#./benchmarks/scripts/graph/start-latency-breakdown-setup-init.py \
#  --start \
#  --dir_path benchmarks/results/$VERSION/start_latency_cached \
#  --proc_name cached-srv-cpp
#echo "..............................................Cached, cosandbox.............................................."
#./benchmarks/scripts/graph/start-latency-breakdown-setup-init.py \
#  --start \
#  --dir_path benchmarks/results/$VERSION/start_latency_cached_cosandbox \
#  --proc_name cached-srv-cpp
#printf "\n\n\n"

echo "Generating imgresize time comparison..."
./benchmarks/scripts/graph/imgresize-time.py \
   --dir_path_nocosandboxes $RES_OUT_DIR/img_process_gvisor \
   --dir_path_cosandboxes $RES_OUT_DIR/img_process_gvisor_cosandbox \
   --dir_path_cosandboxes_writeout $RES_OUT_DIR/img_process_gvisor_cosandbox_writeout \
   --output $GRAPH_OUT_DIR/imgresize-time.pdf
echo "Done generating imgresize time comparison..."

echo "Generating start latency comparison..."
./benchmarks/scripts/graph/start-latency-cosandbox-bar-graph.py \
  --dir_path_etcd $RES_OUT_DIR/start_latency_etcd \
  --dir_path_etcd_cosandbox $RES_OUT_DIR/start_latency_etcd_cosandbox \
  --dir_path_memcached $RES_OUT_DIR/start_latency_memcached \
  --dir_path_memcached_cosandbox $RES_OUT_DIR/start_latency_memcached_cosandbox \
  --dir_path_vecdb $RES_OUT_DIR/start_latency_cossim \
  --dir_path_vecdb_cosandbox $RES_OUT_DIR/start_latency_cossim_cosandbox \
  --dir_path_cached $RES_OUT_DIR/start_latency_cached \
  --dir_path_cached_cosandbox $RES_OUT_DIR/start_latency_cached_cosandbox \
  --dir_path_imgrec_wasm $RES_OUT_DIR/start_latency_imgrec-wasm \
  --dir_path_imgrec_wasm_cosandbox $RES_OUT_DIR/start_latency_imgrec-wasm_cosandbox \
  --dir_path_imgrec_py $RES_OUT_DIR/start_latency_imgrec-py \
  --dir_path_imgrec_py_cosandbox $RES_OUT_DIR/start_latency_imgrec-py_cosandbox \
  --output $GRAPH_OUT_DIR/start-latency.pdf
echo "Done generating start latency comparison..."

echo "Generating sebs start latency comparison..."
./benchmarks/scripts/graph/sebs-start-latency-cosandbox-bar-graph.py \
    --dir_path_thumbnailer              $RES_OUT_DIR/sebs_start_latency_210.thumbnailer \
    --dir_path_thumbnailer_uncompressed $RES_OUT_DIR/sebs_start_latency_210.thumbnailer_uncompressed \
    --dir_path_thumbnailer_cosandbox    $RES_OUT_DIR/sebs_start_latency_210.thumbnailer_cosandbox \
    --dir_path_video_processing              $RES_OUT_DIR/sebs_start_latency_220.video-processing \
    --dir_path_video_processing_uncompressed $RES_OUT_DIR/sebs_start_latency_220.video-processing_uncompressed \
    --dir_path_video_processing_cosandbox    $RES_OUT_DIR/sebs_start_latency_220.video-processing_cosandbox \
    --dir_path_image_recognition              $RES_OUT_DIR/sebs_start_latency_411.image-recognition \
    --dir_path_image_recognition_uncompressed $RES_OUT_DIR/sebs_start_latency_411.image-recognition_uncompressed \
    --dir_path_image_recognition_cosandbox    $RES_OUT_DIR/sebs_start_latency_411.image-recognition_cosandbox \
    --dir_path_dna_visualisation              $RES_OUT_DIR/sebs_start_latency_504.dna-visualisation \
    --dir_path_dna_visualisation_uncompressed $RES_OUT_DIR/sebs_start_latency_504.dna-visualisation_uncompressed \
    --dir_path_dna_visualisation_cosandbox    $RES_OUT_DIR/sebs_start_latency_504.dna-visualisation_cosandbox \
    --output $GRAPH_OUT_DIR/sebs-start-latency.pdf
echo "Done generating sebs start latency comparison..."

echo "Generating imgresize mem usage comparison..."
./benchmarks/scripts/graph/imgresize-mem-usage.py \
   --input_dir $RES_OUT_DIR/img_process_sequential_gvisor_cosandbox_pss \
   --output $GRAPH_OUT_DIR/imgresize-mem-usage.pdf
echo "Done generating imgresize mem usage comparison..."

echo "Generating imgresize writeout cost comparison..."
./benchmarks/scripts/graph/imgresize-cost-writeout.py \
   --cosandbox_dir $RES_OUT_DIR/img_process_gvisor_cosandbox_writeout \
   --nocosandbox_dir $RES_OUT_DIR/img_process_gvisor_cosandbox \
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
  --relabel_1 "DownloadCoSandbox" "DownloadCoSandbox" \
  --omit_1 "GlobalScheduling" \
  --dir_path_2 benchmarks/results/$VERSION/start_latency_cossim_cosandbox \
  --proc_name_2 cossim-srv-cpp \
  --label_2 "VecDB (co-sandbox)" \
  --relabel_2 "ConnectionSetup" "ConnectionSetup-1" \
  --relabel_2 "InitSPProxyConn" "ConnectionSetup-2" \
  --relabel_2 "DownloadCoSandbox" "DownloadCoSandbox" \
  --omit_2 "GlobalScheduling" \
  --subtract_1_from_2 "GlobalScheduling" "DownloadCoSandbox" \
  --simplified \
  --output $GRAPH_OUT_DIR/vecdb-start-latency-breakdown-timeline-simplified.pdf \
  | tee $GRAPH_OUT_DIR/vecdb-start-latency-breakdown-timeline.txt
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
  --relabel_1 "DownloadCoSandbox" "DownloadCoSandbox" \
  --omit_1 "GlobalScheduling" \
  --dir_path_2 benchmarks/results/$VERSION/start_latency_cached_cosandbox_noshmem \
  --proc_name_2 cached-srv-cpp \
  --label_2 "Cached (co-sandbox, no shared-memory)" \
  --relabel_2 "ConnectionSetup" "ConnectionSetup-1" \
  --relabel_2 "InitSPProxyConn" "ConnectionSetup-2" \
  --relabel_2 "DownloadCoSandbox" "DownloadCoSandbox" \
  --omit_2 "GlobalScheduling" \
  --subtract_1_from_2 "GlobalScheduling" "DownloadCoSandbox" \
  --dir_path_3 benchmarks/results/$VERSION/start_latency_cached_cosandbox \
  --proc_name_3 cached-srv-cpp \
  --label_3 "Cached (co-sandbox, shared-memory)" \
  --relabel_3 "ConnectionSetup" "ConnectionSetup-1" \
  --relabel_3 "InitSPProxyConn" "ConnectionSetup-2" \
  --relabel_3 "DownloadCoSandbox" "DownloadCoSandbox" \
  --omit_3 "GlobalScheduling" \
  --subtract_1_from_3 "GlobalScheduling" "DownloadCoSandbox" \
  --output $GRAPH_OUT_DIR/cached-start-latency-breakdown-timeline.pdf \
  | tee $GRAPH_OUT_DIR/cached-start-latency-breakdown-timeline.txt
echo "Done generating cached start latency breakdown graph..."

echo "Generating memcached start latency breakdown graph..."
./benchmarks/scripts/graph/start-latency-breakdown-timeline.py \
  --paper \
  --dir_path_1 benchmarks/results/$VERSION/start_latency_memcached \
  --proc_name_1 memcached-shim \
  --label_1 "Memcached" \
  --omit_1 "GlobalScheduling" \
  --relabel_1 "DownloadCoSandbox" "DownloadCoSandbox" \
  --dir_path_2 benchmarks/results/$VERSION/start_latency_memcached_cosandbox \
  --proc_name_2 memcached-shim \
  --label_2 "Memcached (co-sandbox)" \
  --omit_2 "GlobalScheduling" \
  --relabel_2 "DownloadCoSandbox" "DownloadCoSandbox" \
  --subtract_1_from_2 "GlobalScheduling" "DownloadCoSandbox" \
  --output $GRAPH_OUT_DIR/memcached-start-latency-breakdown-timeline.pdf \
  | tee $GRAPH_OUT_DIR/memcached-start-latency-breakdown-timeline.txt
echo "Done generating memcached start latency breakdown graph..."

echo "Generating imgrec-wasm start latency breakdown graph..."
./benchmarks/scripts/graph/start-latency-breakdown-timeline.py \
  --paper \
  --dir_path_1 benchmarks/results/$VERSION/start_latency_imgrec-wasm \
  --proc_name_1 imgrec_precompiled.wasm \
  --label_1 "Imgrec (WASM)" \
  --omit_1 "GlobalScheduling" \
  --relabel_1 "DownloadCoSandbox" "DownloadCoSandbox" \
  --dir_path_2 benchmarks/results/$VERSION/start_latency_imgrec-wasm_cosandbox \
  --proc_name_2 imgrec_precompiled.wasm \
  --label_2 "Imgrec (WASM, co-sandbox)" \
  --omit_2 "GlobalScheduling" \
  --relabel_2 "DownloadCoSandbox" "DownloadCoSandbox" \
  --subtract_1_from_2 "GlobalScheduling" "DownloadCoSandbox" \
  --output $GRAPH_OUT_DIR/imgrec-wasm-start-latency-breakdown-timeline.pdf \
  | tee $GRAPH_OUT_DIR/imgrec-wasm-start-latency-breakdown-timeline.txt
echo "Done generating imgrec-wasm start latency breakdown graph..."

echo "Generating imgrec-py start latency breakdown graph..."
./benchmarks/scripts/graph/start-latency-breakdown-timeline.py \
  --paper \
  --dir_path_1 benchmarks/results/$VERSION/start_latency_imgrec-py \
  --proc_name_1 imgrec.py \
  --label_1 "Imgrec (Python)" \
  --omit_1 "GlobalScheduling" \
  --relabel_1 "DownloadCoSandbox" "DownloadCoSandbox" \
  --dir_path_2 benchmarks/results/$VERSION/start_latency_imgrec-py_cosandbox \
  --proc_name_2 imgrec.py \
  --label_2 "Imgrec (Python, co-sandbox)" \
  --omit_2 "GlobalScheduling" \
  --relabel_2 "DownloadCoSandbox" "DownloadCoSandbox" \
  --subtract_1_from_2 "GlobalScheduling" "DownloadCoSandbox" \
  --output $GRAPH_OUT_DIR/imgrec-py-start-latency-breakdown-timeline.pdf \
  | tee $GRAPH_OUT_DIR/imgrec-py-start-latency-breakdown-timeline.txt
echo "Done generating imgrec-py start latency breakdown graph..."

echo "Generating SeBS thumbnailer start latency breakdown graph..."
./benchmarks/scripts/graph/start-latency-breakdown-timeline.py \
  --paper \
  --dir_path_1 $RES_OUT_DIR/sebs_start_latency_210.thumbnailer \
  --proc_name_1 sebs-runner.py \
  --label_1 "Thumbnailer" \
  --omit_1 "GlobalScheduling" \
  --relabel_1 "DownloadCoSandbox" "DownloadCoSandbox" \
  --dir_path_2 $RES_OUT_DIR/sebs_start_latency_210.thumbnailer_cosandbox \
  --proc_name_2 sebs-runner.py \
  --label_2 "Thumbnailer (co-sandbox)" \
  --omit_2 "GlobalScheduling" \
  --relabel_2 "DownloadCoSandbox" "DownloadCoSandbox" \
  --subtract_1_from_2 "GlobalScheduling" "DownloadCoSandbox" \
  --output $GRAPH_OUT_DIR/sebs-thumbnailer-start-latency-breakdown-timeline.pdf \
  | tee $GRAPH_OUT_DIR/sebs-thumbnailer-start-latency-breakdown-timeline.txt
echo "Done generating SeBS thumbnailer start latency breakdown graph..."

echo "Generating SeBS video-processing start latency breakdown graph..."
./benchmarks/scripts/graph/start-latency-breakdown-timeline.py \
  --paper \
  --dir_path_1 $RES_OUT_DIR/sebs_start_latency_220.video-processing \
  --proc_name_1 sebs-runner.py \
  --label_1 "Video Processing" \
  --omit_1 "GlobalScheduling" \
  --relabel_1 "DownloadCoSandbox" "DownloadCoSandbox" \
  --dir_path_2 $RES_OUT_DIR/sebs_start_latency_220.video-processing_cosandbox \
  --proc_name_2 sebs-runner.py \
  --label_2 "Video Processing (co-sandbox)" \
  --omit_2 "GlobalScheduling" \
  --relabel_2 "DownloadCoSandbox" "DownloadCoSandbox" \
  --subtract_1_from_2 "GlobalScheduling" "DownloadCoSandbox" \
  --output $GRAPH_OUT_DIR/sebs-video-processing-start-latency-breakdown-timeline.pdf \
  | tee $GRAPH_OUT_DIR/sebs-video-processing-start-latency-breakdown-timeline.txt
echo "Done generating SeBS video-processing start latency breakdown graph..."

echo "Generating SeBS image-recognition start latency breakdown graph..."
./benchmarks/scripts/graph/start-latency-breakdown-timeline.py \
  --paper \
  --dir_path_1 $RES_OUT_DIR/sebs_start_latency_411.image-recognition \
  --proc_name_1 sebs-runner.py \
  --label_1 "Image Recognition" \
  --omit_1 "GlobalScheduling" \
  --relabel_1 "DownloadCoSandbox" "DownloadCoSandbox" \
  --dir_path_2 $RES_OUT_DIR/sebs_start_latency_411.image-recognition_cosandbox \
  --proc_name_2 sebs-runner.py \
  --label_2 "Image Recognition (co-sandbox)" \
  --omit_2 "GlobalScheduling" \
  --relabel_2 "DownloadCoSandbox" "DownloadCoSandbox" \
  --subtract_1_from_2 "GlobalScheduling" "DownloadCoSandbox" \
  --output $GRAPH_OUT_DIR/sebs-image-recognition-start-latency-breakdown-timeline.pdf \
  | tee $GRAPH_OUT_DIR/sebs-image-recognition-start-latency-breakdown-timeline.txt
echo "Done generating SeBS image-recognition start latency breakdown graph..."

echo "Generating SeBS dna-visualisation start latency breakdown graph..."
./benchmarks/scripts/graph/start-latency-breakdown-timeline.py \
  --paper \
  --dir_path_1 $RES_OUT_DIR/sebs_start_latency_504.dna-visualisation \
  --proc_name_1 sebs-runner.py \
  --label_1 "DNA Visualisation" \
  --omit_1 "GlobalScheduling" \
  --relabel_1 "DownloadCoSandbox" "DownloadCoSandbox" \
  --dir_path_2 $RES_OUT_DIR/sebs_start_latency_504.dna-visualisation_cosandbox \
  --proc_name_2 sebs-runner.py \
  --label_2 "DNA Visualisation (co-sandbox)" \
  --omit_2 "GlobalScheduling" \
  --relabel_2 "DownloadCoSandbox" "DownloadCoSandbox" \
  --subtract_1_from_2 "GlobalScheduling" "DownloadCoSandbox" \
  --output $GRAPH_OUT_DIR/sebs-dna-visualisation-start-latency-breakdown-timeline.pdf \
  | tee $GRAPH_OUT_DIR/sebs-dna-visualisation-start-latency-breakdown-timeline.txt
echo "Done generating SeBS dna-visualisation start latency breakdown graph..."

echo "Imgresize breakdown..."
./benchmarks/scripts/graph/imgresize-time-breakdown.py \
  --input_dir $RES_OUT_DIR/img_process_gvisor
echo "Imgresize breakdown..."

echo "Imgresize (cosandbox) breakdown..."
./benchmarks/scripts/graph/imgresize-time-breakdown.py \
  --input_dir $RES_OUT_DIR/img_process_gvisor_cosandbox
echo "Imgresize (cosandbox) breakdown..."

echo "Imgresize (writeout) breakdown..."
./benchmarks/scripts/graph/imgresize-time-breakdown.py \
  --input_dir $RES_OUT_DIR/img_process_gvisor_cosandbox_writeout
echo "Imgresize (writeout) breakdown..."

#echo "..............................................Cached, no cosandbox.............................................."
#./benchmarks/scripts/graph/start-latency-breakdown-setup-init.py \
#  --start \
#  --dir_path benchmarks/results/$VERSION/start_latency_cossim \
#  --proc_name cossim-srv-cpp
#echo "..............................................Cached, cosandbox.............................................."
#./benchmarks/scripts/graph/start-latency-breakdown-setup-init.py \
#  --start \
#  --dir_path benchmarks/results/$VERSION/start_latency_cossim_cosandbox \
#  --proc_name cossim-srv-cpp
#printf "\n\n\n"
