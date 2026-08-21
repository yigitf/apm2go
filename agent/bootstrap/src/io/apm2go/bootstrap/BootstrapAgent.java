package io.apm2go.bootstrap;

import java.io.File;
import java.io.FileInputStream;
import java.io.IOException;
import java.io.InputStream;
import java.io.StringReader;
import java.lang.instrument.Instrumentation;
import java.util.Map;
import java.util.Properties;
import java.util.TreeMap;

/**
 * Seeds a running JVM with the system properties that configure the
 * OpenTelemetry agent, so that the agent itself can then be attached through
 * the JVM's ordinary agent loading path.
 *
 * <p>The OpenTelemetry agent reads its configuration from environment variables
 * and system properties at the moment it initialises. When an agent is injected
 * into a live process there is no opportunity to set environment variables, and
 * the attach protocol offers only a single option string. apm2go therefore
 * performs two attaches in sequence: this agent first, which applies the
 * configuration, and the OpenTelemetry agent second, which reads it.
 *
 * <p>Keeping the two steps separate means apm2go never reaches into
 * OpenTelemetry's internals, and stays compatible across agent releases.
 *
 * <p>This class is compiled against Java 8 so that it can be injected into the
 * oldest JVM apm2go supports. It has no dependencies beyond the JDK.
 */
public final class BootstrapAgent {

    /** Marker property recording that apm2go configured this JVM, and when. */
    public static final String MARKER_PROPERTY = "apm2go.bootstrap.applied";

    /** Property carrying the bootstrap agent's own version, for diagnostics. */
    public static final String VERSION_PROPERTY = "apm2go.bootstrap.version";

    private static final String VERSION = "1";

    /** Prefix for keys that configure this agent rather than the target JVM. */
    private static final String INTERNAL_PREFIX = "apm2go.internal.";

    private BootstrapAgent() {
    }

    /**
     * Entry point used when the agent is loaded into a running JVM.
     *
     * @param args path to a properties file holding the configuration to apply
     * @param inst unused; required by the agent contract
     */
    public static void agentmain(String args, Instrumentation inst) throws Exception {
        apply(args);
    }

    /**
     * Entry point used when the agent is listed on the command line with
     * -javaagent, which is how a permanent installation runs.
     */
    public static void premain(String args, Instrumentation inst) throws Exception {
        apply(args);
    }

    /**
     * Copies apm2go's configuration into the system properties.
     *
     * <p>The argument is either the configuration itself, or the path to a
     * file holding it. Which one it is, is decided by its first character:
     * apm2go's own rendering always opens with a {@code #} comment line, and a
     * path is always absolute, so the two never collide. Inline is what apm2go
     * sends whenever the configuration is short enough to fit in the attach
     * protocol's own option string; the file exists only for configuration too
     * large for that.
     *
     * <p>The distinction matters beyond size. A target running under a
     * {@link SecurityManager} — Elasticsearch, notably, on any JDK where it is
     * not yet removed — grants a dynamically attached agent no
     * {@code FilePermission} by default, and {@link FileInputStream} is
     * exactly the call that check applies to. Reading the same bytes out of
     * the argument string that got this agent loaded in the first place needs
     * no permission at all: nothing here opens a file unless the configuration
     * did not fit and one genuinely has to be read.
     *
     * <p>Any failure is thrown rather than swallowed: the attach protocol
     * reports a throwing agentmain back to apm2go, which is how the operator
     * finds out that configuration did not apply. A silent failure here would
     * surface much later as an agent that traces nothing.
     */
    private static void apply(String args) throws IOException {
        if (args == null || args.trim().isEmpty()) {
            throw new IllegalArgumentException(
                    "apm2go bootstrap agent requires its configuration, or the path to a file "
                            + "holding it, as its argument");
        }

        Properties props = new Properties();
        if (args.charAt(0) == '/') {
            File file = new File(args.trim());
            if (!file.isFile()) {
                throw new IOException("apm2go configuration file not found: " + file.getAbsolutePath());
            }
            InputStream in = new FileInputStream(file);
            try {
                props.load(in);
            } finally {
                in.close();
            }
        } else {
            props.load(new StringReader(args));
        }

        // Sorted purely so the log line below is stable and diffable.
        Map<String, String> applied = new TreeMap<String, String>();
        for (String name : props.stringPropertyNames()) {
            if (name.startsWith(INTERNAL_PREFIX)) {
                continue;
            }
            String value = props.getProperty(name);
            if (value == null) {
                continue;
            }
            System.setProperty(name, value);
            applied.put(name, redact(name, value));
        }

        System.setProperty(VERSION_PROPERTY, VERSION);
        System.setProperty(MARKER_PROPERTY, Long.toString(System.currentTimeMillis()));

        // Written to stdout of the target process, where it lands in the
        // application's own log. This is the only trace this agent leaves, and
        // it is what an operator looks for when a trace fails to appear.
        System.out.println("[apm2go] applied " + applied.size()
                + " configuration properties: " + applied);
    }

    /**
     * Hides the value of anything that looks like a credential, since this line
     * is printed into the application's log.
     */
    private static String redact(String name, String value) {
        String lower = name.toLowerCase();
        if (lower.contains("token") || lower.contains("password")
                || lower.contains("secret") || lower.contains("apikey")
                || lower.contains("api-key")) {
            return "***";
        }
        return value;
    }
}
