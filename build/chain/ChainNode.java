import jakarta.servlet.http.HttpServlet;
import jakarta.servlet.http.HttpServletRequest;
import jakarta.servlet.http.HttpServletResponse;

import org.eclipse.jetty.server.Server;
import org.eclipse.jetty.servlet.ServletContextHandler;
import org.eclipse.jetty.servlet.ServletHolder;

import java.io.IOException;
import java.net.URI;
import java.net.http.HttpClient;
import java.net.http.HttpRequest;
import java.net.http.HttpResponse;
import java.sql.Connection;
import java.sql.DriverManager;
import java.sql.ResultSet;
import java.sql.Statement;
import java.util.ArrayList;
import java.util.List;

/**
 * One node of a small service chain, used to verify that apm2go produces a
 * waterfall spanning several services from a single request.
 *
 * <p>The same class becomes a different node — gateway, orders, inventory,
 * payments — purely through system properties, so the chain's shape lives in
 * its systemd units rather than in this code.
 *
 * <p><b>This class contains no tracing code of any kind.</b> That is the point
 * of the test: it is an ordinary servlet application, exactly as a customer's
 * would be, and every span it produces comes from apm2go attaching to it while
 * it is already running. It is built on embedded Jetty rather than the JDK's
 * own {@code com.sun.net.httpserver} for a reason that matters to apm2go:
 * OpenTelemetry instruments the JDK server by hooking {@code createContext},
 * which an application calls once at start-up, so a server that was already
 * listening before apm2go attached would never be instrumented. Servlet
 * containers are hooked on {@code service()} and {@code doFilter()} — methods
 * called on every request — so retransforming their already-loaded classes
 * takes effect on the very next request. Real Java services are servlet
 * applications; this one is representative of them.
 *
 * <p>System properties:
 * <ul>
 *   <li>{@code chain.port} — listen port (default 8081)
 *   <li>{@code chain.path} — path this node serves (default /api/work)
 *   <li>{@code chain.downstream} — comma-separated URLs to call per request
 *   <li>{@code chain.query} — run a local database query (default true)
 *   <li>{@code chain.selfloop} — URL to call on a timer; set only on the
 *       chain's entry point, so the chain drives its own traffic
 *   <li>{@code chain.deadlock} — deadlock two threads on start-up, so the
 *       thread-dump analysis has something real to find
 * </ul>
 */
public class ChainNode {

    public static void main(String[] args) throws Exception {
        int port = Integer.getInteger("chain.port", 8081);
        String path = System.getProperty("chain.path", "/api/work");
        String downstream = System.getProperty("chain.downstream", "").trim();
        boolean withQuery = Boolean.parseBoolean(System.getProperty("chain.query", "true"));
        String selfLoop = System.getProperty("chain.selfloop", "").trim();

        List<String> downstreams = new ArrayList<>();
        for (String url : downstream.split(",")) {
            if (!url.trim().isEmpty()) {
                downstreams.add(url.trim());
            }
        }

        Server server = new Server(port);
        ServletContextHandler context = new ServletContextHandler();
        context.setContextPath("/");
        context.addServlet(new ServletHolder(new WorkServlet(downstreams, withQuery)), path);
        context.addServlet(new ServletHolder(new HealthServlet()), "/health");
        server.setHandler(context);
        server.start();

        System.out.println("[chain] listening on " + port + " path=" + path
                + " downstream=" + downstreams + " query=" + withQuery
                + " pid=" + ProcessHandle.current().pid());

        if (Boolean.getBoolean("chain.deadlock")) {
            deadlockTwoThreads();
        }

        if (selfLoop.isEmpty()) {
            // A leaf node has nothing to drive; it just serves what the chain
            // above it sends.
            server.join();
            return;
        }

        HttpClient client = HttpClient.newHttpClient();
        while (true) {
            try {
                HttpRequest request = HttpRequest.newBuilder(URI.create(selfLoop)).GET().build();
                HttpResponse<String> response = client.send(request, HttpResponse.BodyHandlers.ofString());
                System.out.println("[chain] self-loop -> " + response.statusCode());
            } catch (Exception e) {
                System.out.println("[chain] self-loop failed: " + e);
            }
            Thread.sleep(3000);
        }
    }

    /** Serves the node's work: a database query, then its downstream calls. */
    public static class WorkServlet extends HttpServlet {

        private final List<String> downstreams;
        private final boolean withQuery;
        // Created once and reused, which is how a real service holds its client.
        private final HttpClient client = HttpClient.newHttpClient();

        WorkServlet(List<String> downstreams, boolean withQuery) {
            this.downstreams = downstreams;
            this.withQuery = withQuery;
        }

        @Override
        protected void doGet(HttpServletRequest request, HttpServletResponse response) throws IOException {
            StringBuilder body = new StringBuilder("{");
            try {
                if (withQuery) {
                    body.append("\"rows\":").append(countRows()).append(",");
                }

                // Each downstream call travels as an outgoing HTTP request, so
                // the trace context propagates into that service and its work
                // appears as a nested subtree in the same trace.
                List<String> results = new ArrayList<>();
                for (String url : downstreams) {
                    HttpRequest downstreamRequest = HttpRequest.newBuilder(URI.create(url)).GET().build();
                    HttpResponse<String> downstreamResponse =
                            client.send(downstreamRequest, HttpResponse.BodyHandlers.ofString());
                    results.add(url + "=" + downstreamResponse.statusCode());
                }
                body.append("\"calls\":\"").append(String.join(";", results)).append("\"}");
            } catch (Exception e) {
                // Thrown out of the servlet so the container records a 500 and
                // the agent marks the span as failed, which is what an error in
                // a real service looks like.
                throw new IOException("downstream call failed", e);
            }

            response.setContentType("application/json");
            response.setStatus(HttpServletResponse.SC_OK);
            response.getWriter().write(body.toString());
        }
    }

    /** A cheap endpoint for readiness checks, deliberately doing no work. */
    public static class HealthServlet extends HttpServlet {
        @Override
        protected void doGet(HttpServletRequest request, HttpServletResponse response) throws IOException {
            response.setContentType("text/plain");
            response.getWriter().write("ok");
        }
    }

    /**
     * Deadlocks two threads against each other, permanently and on purpose.
     *
     * <p>This is the fault apm2go's thread-dump analysis exists to find, and the
     * only honest way to test that it finds one. Each thread takes one lock,
     * waits long enough that the other has certainly taken the other lock, and
     * then asks for the one it does not have. Neither ever returns.
     *
     * <p>It is deliberately confined to two threads that do nothing else: the
     * node keeps serving requests normally around them, which is what makes the
     * test meaningful. A deadlock that took the whole service down would be
     * visible without any dump at all.
     */
    private static void deadlockTwoThreads() {
        Object first = new Object();
        Object second = new Object();

        Thread a = new Thread(() -> {
            synchronized (first) {
                sleep(500);
                synchronized (second) {
                    throw new IllegalStateException("unreachable: this is a deadlock");
                }
            }
        }, "chain-deadlock-a");

        Thread b = new Thread(() -> {
            synchronized (second) {
                sleep(500);
                synchronized (first) {
                    throw new IllegalStateException("unreachable: this is a deadlock");
                }
            }
        }, "chain-deadlock-b");

        // Daemon threads, so the deadlock never keeps the JVM alive on its own.
        a.setDaemon(true);
        b.setDaemon(true);
        a.start();
        b.start();

        System.out.println("[chain] two threads deadlocked on purpose");
    }

    private static void sleep(long millis) {
        try {
            Thread.sleep(millis);
        } catch (InterruptedException e) {
            Thread.currentThread().interrupt();
        }
    }

    /** Runs a real JDBC query so each node contributes a database span. */
    private static int countRows() throws Exception {
        try (Connection conn = DriverManager.getConnection(
                "jdbc:h2:mem:chain;DB_CLOSE_DELAY=-1", "sa", "")) {
            try (Statement st = conn.createStatement()) {
                try (ResultSet rs = st.executeQuery("SELECT 42")) {
                    return rs.next() ? rs.getInt(1) : 0;
                }
            }
        }
    }
}
