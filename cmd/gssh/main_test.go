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
		{name: "single full command string stays raw", args: []string{"echo hello | wc -c"}, want: "echo hello | wc -c"},
		{name: "multiple args are shell quoted", args: []string{"echo", "hello", "world"}, want: "'echo' 'hello' 'world'"},
		{name: "space in argument is preserved", args: []string{"touch", "a b"}, want: "'touch' 'a b'"},
		{name: "metacharacters stay literal", args: []string{"printf", "%s\n", "*"}, want: "'printf' '%s\n' '*'"},
		{name: "single quote is escaped safely", args: []string{"printf", "pa'ss"}, want: "'printf' 'pa'\"'\"'ss'"},
		{name: "empty arg is preserved", args: []string{"printf", ""}, want: "'printf' ''"},
	}

	for _, tt := range tests {
		if got := joinCommandArgs(tt.args); got != tt.want {
			t.Fatalf("%s: joinCommandArgs() = %q, want %q", tt.name, got, tt.want)
		}
	}
}
