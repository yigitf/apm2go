// One node of the multi-language acceptance test: an ordinary Node.js HTTP
// server that calls into the Java chain (see build/chain/ChainNode.java).
//
// Like the Java chain, this contains no tracing code of any kind. Every span
// it produces has to come from apm2go's eBPF instrumentation observing it from
// outside — that is the entire premise this test exists to check, and the
// reason for using Node's plain http module rather than a framework: nothing
// here should be able to hide whether apm2go noticed the request on its own.
const http = require("http");

const PORT = Number(process.env.CALLER_PORT || 8090);
const DOWNSTREAM = process.env.CALLER_DOWNSTREAM || "http://127.0.0.1:8081/api/gateway";
// Self-driven, like the Java chain's own gateway: a test relying on someone
// else to send requests at the right moments is a test with a race condition
// baked into it, and a chart with one lonely point looks broken even when the
// pipeline behind it is not.
const SELFLOOP_MS = Number(process.env.CALLER_SELFLOOP_MS || 3000);

function callDownstream() {
  return new Promise((resolve, reject) => {
    http.get(DOWNSTREAM, (res) => {
      res.resume();
      res.on("end", () => resolve(res.statusCode));
    }).on("error", reject);
  });
}

http
  .createServer((req, res) => {
    if (req.url === "/health") {
      res.writeHead(200, { "Content-Type": "text/plain" });
      res.end("ok");
      return;
    }
    callDownstream()
      .then((status) => {
        res.writeHead(200, { "Content-Type": "application/json" });
        res.end(JSON.stringify({ node: "ok", downstream: status }));
      })
      .catch((err) => {
        res.writeHead(502, { "Content-Type": "application/json" });
        res.end(JSON.stringify({ error: String(err) }));
      });
  })
  .listen(PORT, () => {
    console.log(`[node-caller] listening on ${PORT} downstream=${DOWNSTREAM} pid=${process.pid}`);
    if (SELFLOOP_MS > 0) {
      setInterval(() => {
        callDownstream()
          .then((status) => console.log(`[node-caller] self-loop -> ${status}`))
          .catch((err) => console.log(`[node-caller] self-loop failed: ${err}`));
      }, SELFLOOP_MS);
    }
  });
