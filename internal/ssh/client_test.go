package ssh

import (
	"os"
	"strings"
	"testing"
)

func TestExpandPath(t *testing.T) {
	homeDir, _ := os.UserHomeDir()

	tests := []struct {
		input string
		want  string
	}{
		{"~/.ssh/id_rsa", homeDir + "/.ssh/id_rsa"},
		{"~/key.pem", homeDir + "/key.pem"},
		{"/absolute/path", "/absolute/path"},
		{"relative/path", "relative/path"},
	}

	for _, tt := range tests {
		got := expandPath(tt.input)
		if got != tt.want {
			t.Errorf("expandPath(%s) = %s, want %s", tt.input, got, tt.want)
		}
	}
}

func TestKeyboardInteractive(t *testing.T) {
	handler := &keyboardInteractiveHandler{Password: "testpass"}
	answers, err := handler.Challenge("server", "prompt", []string{"q1", "q2"}, []bool{false, false})
	if err != nil {
		t.Fatalf("Challenge returned error: %v", err)
	}
	for _, a := range answers {
		if a != "testpass" {
			t.Errorf("answer = %s, want testpass", a)
		}
	}
}

func TestConnectWithoutAnyAuthMaterialFailsFast(t *testing.T) {
	// Isolate from real ~/.ssh keys and any running ssh-agent.
	t.Setenv("HOME", t.TempDir())
	t.Setenv("SSH_AUTH_SOCK", "")

	_, err := Connect("user", "127.0.0.1", 22, AuthConfig{})
	if err == nil {
		t.Fatal("expected error when no auth material is available")
	}
	if got := err.Error(); !strings.HasPrefix(got, "no authentication method available") {
		t.Fatalf("expected 'no authentication method available' error, got: %v", err)
	}
}

func TestDefaultKeyAuthMethodsSkipsMissingAndBadKeys(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	if got := defaultKeyAuthMethods(); len(got) != 0 {
		t.Fatalf("expected no methods with empty ~/.ssh, got %d", len(got))
	}

	sshDir := home + "/.ssh"
	if err := os.MkdirAll(sshDir, 0700); err != nil {
		t.Fatal(err)
	}
	// An unparseable key file must be skipped, not fatal.
	if err := os.WriteFile(sshDir+"/id_ed25519", []byte("not a key"), 0600); err != nil {
		t.Fatal(err)
	}
	if got := defaultKeyAuthMethods(); len(got) != 0 {
		t.Fatalf("expected unparseable key to be skipped, got %d methods", len(got))
	}
}
