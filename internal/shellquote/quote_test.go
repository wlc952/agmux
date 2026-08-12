package shellquote

import "testing"

func TestQuote(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "empty", input: "", want: "''"},
		{name: "safe passes through", input: "www-data", want: "www-data"},
		{name: "safe path", input: "/usr/local/bin", want: "/usr/local/bin"},
		{name: "spaces are quoted", input: "echo hello", want: "'echo hello'"},
		{name: "single quote escaped", input: "pa'ss", want: `'pa'"'"'ss'`},
		{name: "newline preserved literally", input: "echo a\necho b", want: "'echo a\necho b'"},
		{name: "metacharacters quoted", input: "ls | wc -l", want: "'ls | wc -l'"},
		{name: "double quotes quoted", input: `echo "hi"`, want: `'echo "hi"'`},
	}

	for _, tt := range tests {
		if got := Quote(tt.input); got != tt.want {
			t.Errorf("%s: Quote(%q) = %q, want %q", tt.name, tt.input, got, tt.want)
		}
	}
}
