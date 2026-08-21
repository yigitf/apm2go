#!/usr/bin/env bash
#
# Compiles the chain workload. A JDK is needed here but not on the host that
# runs it, which is why the classes are built once and copied in.
set -euo pipefail

cd "$(dirname "$0")"
./fetch-deps.sh ./lib

CP="$(ls ./lib/*.jar | tr '\n' ':')"
rm -rf ./classes
mkdir -p ./classes

# Java 21 is the floor for Jetty 11 with the servlet 5 API.
javac --release 21 -cp "${CP}" -d ./classes ChainNode.java
echo "chain workload compiled into $(pwd)/classes"
