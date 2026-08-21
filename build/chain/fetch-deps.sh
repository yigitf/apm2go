#!/usr/bin/env bash
#
# Downloads the chain workload's dependencies.
#
# They are fetched rather than vendored: they are a test fixture, not part of
# apm2go, and committing several megabytes of third-party jars into the repo to
# support one acceptance test is not a trade worth making.
#
# Jetty is deliberate. The workload has to be something OpenTelemetry
# instruments the way it instruments real services — on methods called per
# request — because that is what makes attaching to an already-running process
# take effect. See the note in ChainNode.java.
set -euo pipefail

LIB_DIR="${1:-$(dirname "$0")/lib}"
mkdir -p "${LIB_DIR}"

JETTY_VERSION=11.0.20
SERVLET_API_VERSION=5.0.0
SLF4J_VERSION=2.0.16
H2_VERSION=2.2.224

MAVEN=https://repo1.maven.org/maven2

fetch() {
    local url=$1 name=$2
    if [ -f "${LIB_DIR}/${name}" ]; then
        return
    fi
    echo "  fetching ${name}"
    curl -sSLf -o "${LIB_DIR}/${name}" "${url}"
}

for module in jetty-server jetty-http jetty-io jetty-util jetty-servlet jetty-security; do
    fetch "${MAVEN}/org/eclipse/jetty/${module}/${JETTY_VERSION}/${module}-${JETTY_VERSION}.jar" "${module}.jar"
done

fetch "${MAVEN}/jakarta/servlet/jakarta.servlet-api/${SERVLET_API_VERSION}/jakarta.servlet-api-${SERVLET_API_VERSION}.jar" jakarta.servlet-api.jar

# Jetty logs through slf4j and will not start without a binding on the classpath.
fetch "${MAVEN}/org/slf4j/slf4j-api/${SLF4J_VERSION}/slf4j-api-${SLF4J_VERSION}.jar" slf4j-api.jar
fetch "${MAVEN}/org/slf4j/slf4j-simple/${SLF4J_VERSION}/slf4j-simple-${SLF4J_VERSION}.jar" slf4j-simple.jar

# A real JDBC driver, so each node contributes a database span.
fetch "${MAVEN}/com/h2database/h2/${H2_VERSION}/h2-${H2_VERSION}.jar" h2.jar

echo "chain dependencies ready in ${LIB_DIR}"
