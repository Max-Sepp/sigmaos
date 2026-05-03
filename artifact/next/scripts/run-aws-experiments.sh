#!/bin/bash

usage() {
  echo "Usage: $0 [--exp fig_XXX] [--rerun]" 1>&2
}

EXP="all"
RERUN="false"
while [[ $# -gt 0 ]]; do
  key="$1"
  case $key in
  --exp)
    shift
    EXP=$1
    shift
    ;;
  --rerun)
    shift
    RERUN="true"
    ;;
  -help)
    usage
    exit 0
    ;;
  *)
    echo "Error: unexpected argument '$1'"
    usage
    exit 1
    ;;
  esac
done

if [ $# -gt 0 ]; then
    usage
    exit 1
fi

if [ $# -gt 0 ]; then
    usage
    exit 1
fi

if [ $EXP != "all" ] && [ $EXP != "start-lat" ] && [ $EXP != "sebs" ] && [ $EXP != "imgprocess" ]; then
  echo "Unkown experiment $EXP"
  usage
  exit 1
fi

VERSION=EUROSYS2027
TAG=arielck
BRANCH=wasmd

LOG_DIR=/tmp/sigmaos-experiment-logs

AWS_VPC=vpc-02f7e3816c4cc8e7f

mkdir -p $LOG_DIR

if [ $EXP == "all" ] || [ $EXP == "imgprocess" ]; then
#  if [ $RERUN == "true" ]; then
#    echo "Clearing any cached CosSim data..."
#    rm -rf benchmarks/results/$VERSION/cos_sim_tail_latency_*
#  fi
  echo "Generating ImgProcess data..."
  go clean -testcache; go test -v -timeout 0 sigmaos/benchmarks/remote --run TestImgProcess --parallelize --platform aws --vpc $AWS_VPC --build-tag $TAG --no-shutdown-after-test --bench-version $VERSION --branch $BRANCH 2>&1 | tee $LOG_DIR/imgprocess.out
  echo "Done generating ImgProcess data..."
fi

if [ $EXP == "all" ] || [ $EXP == "start-lat" ]; then
#  if [ $RERUN == "true" ]; then
#    echo "Clearing any cached CosSim data..."
#    rm -rf benchmarks/results/$VERSION/cos_sim_tail_latency_*
#  fi
  echo "Generating StartLatency data..."
  go clean -testcache; go test -v -timeout 0 sigmaos/benchmarks/remote --run TestStartLatency --parallelize --platform aws --vpc $AWS_VPC --build-tag $TAG --no-shutdown-after-test --bench-version $VERSION --branch $BRANCH 2>&1 | tee $LOG_DIR/start-lat.out
  echo "Done generating StartLatency data..."
fi

if [ $EXP == "all" ] || [ $EXP == "sebs" ]; then
#  if [ $RERUN == "true" ]; then
#    echo "Clearing any cached CosSim data..."
#    rm -rf benchmarks/results/$VERSION/cos_sim_tail_latency_*
#  fi
  echo "Generating SeBS StartLatency data..."
  go clean -testcache; go test -v -timeout 0 sigmaos/benchmarks/remote --run TestSebsStartLatency --parallelize --platform aws --vpc $AWS_VPC --build-tag $TAG --no-shutdown-after-test --bench-version $VERSION --branch $BRANCH 2>&1 | tee $LOG_DIR/start-lat.out
  echo "Done generating SeBS StartLatency data..."
fi
