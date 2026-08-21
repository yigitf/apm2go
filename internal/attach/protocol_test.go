package attach

import (
	"strings"
	"testing"
)

func TestEncodeRequestAlwaysSendsThreeArguments(t *testing.T) {
	// HotSpot reads a fixed number of NUL-terminated fields and blocks until it
	// has them, so a request with fewer arguments would hang rather than fail.
	got, err := encodeRequest(cmdLoad, "instrument", "false", "/tmp/agent.jar")
	if err != nil {
		t.Fatalf("encodeRequest: %v", err)
	}

	want := "1\x00load\x00instrument\x00false\x00/tmp/agent.jar\x00"
	if string(got) != want {
		t.Errorf("encoded = %q, want %q", got, want)
	}

	// A command with no arguments must still emit three empty fields.
	got, err = encodeRequest(cmdProperties)
	if err != nil {
		t.Fatalf("encodeRequest: %v", err)
	}
	if want := "1\x00properties\x00\x00\x00\x00"; string(got) != want {
		t.Errorf("encoded = %q, want %q", got, want)
	}
}

func TestEncodeRequestRejectsEmbeddedNUL(t *testing.T) {
	// An embedded NUL would desynchronise the framing and make the JVM read the
	// remainder of one argument as the next one.
	if _, err := encodeRequest(cmdLoad, "instrument", "false", "bad\x00path"); err == nil {
		t.Error("expected an error for an argument containing a NUL byte")
	}
}

func TestLoadAgentRequestJoinsJarAndOptions(t *testing.T) {
	got, err := loadAgentRequest("/tmp/agent.jar", "/tmp/config.properties")
	if err != nil {
		t.Fatalf("loadAgentRequest: %v", err)
	}
	if !strings.Contains(string(got), "/tmp/agent.jar=/tmp/config.properties") {
		t.Errorf("encoded = %q, want the jar and options joined by '='", got)
	}

	// With no options the '=' must be omitted, matching -javaagent syntax.
	got, err = loadAgentRequest("/tmp/agent.jar", "")
	if err != nil {
		t.Fatalf("loadAgentRequest: %v", err)
	}
	if strings.Contains(string(got), "agent.jar=") {
		t.Errorf("encoded = %q, want no trailing '=' when there are no options", got)
	}
}

func TestDecodeResponse(t *testing.T) {
	tests := []struct {
		name          string
		raw           string
		wantCode      int
		wantAgentCode int
		wantOK        bool
	}{
		{
			name:     "listener accepted with no agent status",
			raw:      "0\n",
			wantCode: 0,
			wantOK:   true,
		},
		{
			name:          "agent reported success",
			raw:           "0\nreturn code: 0\n",
			wantCode:      0,
			wantAgentCode: 0,
			wantOK:        true,
		},
		{
			// The listener accepts the command and only then does the agent
			// fail, so the outer code is still zero. Reading only the first
			// line would report a failed attach as a success.
			name:          "agent failed after the listener accepted",
			raw:           "0\nreturn code: 4\n",
			wantCode:      0,
			wantAgentCode: 4,
			wantOK:        false,
		},
		{
			name:     "listener rejected the request",
			raw:      "101\nDynamic agent loading is not enabled\n",
			wantCode: 101,
			wantOK:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp, err := decodeResponse(strings.NewReader(tt.raw), 0)
			if err != nil {
				t.Fatalf("decodeResponse: %v", err)
			}
			if resp.Code != tt.wantCode {
				t.Errorf("Code = %d, want %d", resp.Code, tt.wantCode)
			}
			if resp.HasAgentCode && resp.AgentCode != tt.wantAgentCode {
				t.Errorf("AgentCode = %d, want %d", resp.AgentCode, tt.wantAgentCode)
			}
			if resp.OK() != tt.wantOK {
				t.Errorf("OK() = %v, want %v", resp.OK(), tt.wantOK)
			}
			if !tt.wantOK && resp.Err() == nil {
				t.Error("Err() = nil for a failed response")
			}
		})
	}
}

func TestDecodeResponseRejectsGarbage(t *testing.T) {
	for _, raw := range []string{"", "not a number\n"} {
		if _, err := decodeResponse(strings.NewReader(raw), 0); err == nil {
			t.Errorf("decodeResponse(%q) = nil error, want a failure", raw)
		}
	}
}

func TestParsePropertiesUnescapes(t *testing.T) {
	body := "#comment\njava.version=21.0.11\njava.home=/opt/java\\:jdk\nempty=\n"
	props := parseProperties(body)

	if got := props["java.version"]; got != "21.0.11" {
		t.Errorf("java.version = %q, want %q", got, "21.0.11")
	}
	if got := props["java.home"]; got != "/opt/java:jdk" {
		t.Errorf("java.home = %q, want the escaped colon resolved", got)
	}
	if _, ok := props["empty"]; !ok {
		t.Error("an empty value should still produce a key")
	}
	if _, ok := props["#comment"]; ok {
		t.Error("comment lines must be skipped")
	}
}
