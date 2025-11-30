#!/bin/bash

GVISOR_BUNDLE=/tmp/sigmaos-base-user-bundle

echo "Creating gVisor bundle..."
# Remove old bundle if it exists
sudo rm -rf $GVISOR_BUNDLE
mkdir $GVISOR_BUNDLE
mkdir --mode=0755 $GVISOR_BUNDLE/rootfs
echo "Export sigmauser container"
docker export $(docker create sigmauser) | sudo tar -xf - -C $GVISOR_BUNDLE/rootfs --same-owner --same-permissions
echo "Done creating gVisor bundle..."
