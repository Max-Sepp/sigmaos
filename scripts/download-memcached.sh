#!/bin/bash

ROOT_DIR=$(pwd)
TMP_DIR=/tmp/memcached-dl

# Download and build memcached
rm -rf $TMP_DIR
mkdir $TMP_DIR
cd $TMP_DIR
wget http://memcached.org/latest
tar -zxvf latest
cd memcached-1.*
./configure && make && make test && cp memcached ..

# Copy the memcached bin
cd $ROOT_DIR
cp $TMP_DIR/memcached bin/user/memcached-v1.0
