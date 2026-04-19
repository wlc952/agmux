package main

import "testing"

func TestJoinCommandArgs(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "empty", args: nil, want: ""},
		{name: "single", args: []string{"echo"}, want: "echo"},
		{name: "multiple", args: []string{"echo", "hello", "world"}, want: "echo hello world"},
		{name: "quoted fragments preserved", args: []string{"sh", "-c", "echo hi"}, want: "sh -c echo hi"},
	}

	for _, tt := range tests {
		if got := joinCommandArgs(tt.args); got != tt.want {
			t.Fatalf("%s: joinCommandArgs() = %q, want %q", tt.name, got, tt.want)
		}
	}
}
