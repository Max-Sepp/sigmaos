#!/bin/bash

usage() {
  echo "Usage: $0 [--user_ctr USERCTR]" 1>&2
}

USER_CTR="sigmauser"

while [[ "$#" -gt 0 ]]; do
  case "$1" in
  --user_ctr)
    shift
    USER_CTR="$1"
    shift
    ;;
  -help)
    usage
    exit 0
    ;;
  *)
   echo "unexpected argument $1"
   usage
   exit 1
  esac
done

if [ $# -gt 0 ]; then
    usage
    exit 1
fi

GVISOR_BUNDLE=/tmp/sigmaos-base-user-bundle

echo "Creating gVisor bundle..."
# Remove old bundle if it exists
sudo rm -rf $GVISOR_BUNDLE
mkdir $GVISOR_BUNDLE
mkdir --mode=0755 $GVISOR_BUNDLE/rootfs
echo "Export sigmauser container"
docker export $(docker create $USER_CTR) | sudo tar -xf - -C $GVISOR_BUNDLE/rootfs --same-owner --same-permissions
echo "Done creating gVisor bundle..."
