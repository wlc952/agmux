package ssh

import (
	"os"
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