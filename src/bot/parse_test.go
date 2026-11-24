package bot

import (
	"reflect"
	"testing"
)

func TestParseArgs(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected []string
	}{
		{
			name:     "simple unquoted",
			input:    "bebot move 100 64 -200",
			expected: []string{"bebot", "move", "100", "64", "-200"},
		},
		{
			name:     "quoted emote name",
			input:    `bebot emote "hello world"`,
			expected: []string{"bebot", "emote", "hello world"},
		},
		{
			name:     "single quotes",
			input:    `bebot emote 'hello world'`,
			expected: []string{"bebot", "emote", "hello world"},
		},
		{
			name:     "emote add with quotes and uuid",
			input:    `bebot emote add "my dance" c1819d2b-c8c3-4d69-b5f7-66a7b370605a`,
			expected: []string{"bebot", "emote", "add", "my dance", "c1819d2b-c8c3-4d69-b5f7-66a7b370605a"},
		},
		{
			name:     "multiple spaces and trailing space",
			input:    `  bebot   emote   list  `,
			expected: []string{"bebot", "emote", "list"},
		},
		{
			name:     "escaped quote inside quotes",
			input:    `bebot say "hello \"world\""`,
			expected: []string{"bebot", "say", `hello "world"`},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseArgs(tt.input)
			if !reflect.DeepEqual(got, tt.expected) {
				t.Errorf("parseArgs(%q) = %v, want %v", tt.input, got, tt.expected)
			}
		})
	}
}

func TestHasBotPrefix(t *testing.T) {
	tests := []struct {
		input    string
		expected bool
	}{
		{"!b say hello", true},
		{"!b coords", true},
		{"!bhelp", true},
		{"bebot say hello", true},
		{"bebot: say hello", true},
		{"bebop coords", true},
		{"hello world", false},
		{"!c say hello", false},
	}

	for _, tt := range tests {
		if got := hasBotPrefix(tt.input); got != tt.expected {
			t.Errorf("hasBotPrefix(%q) = %v, want %v", tt.input, got, tt.expected)
		}
	}
}

func TestIsBotTrigger(t *testing.T) {
	tests := []struct {
		input    string
		expected bool
	}{
		{"!b", true},
		{"!b:", true},
		{"!bebot", true},
		{"bebot", true},
		{"bebot:", true},
		{"bebop", true},
		{"other", false},
	}

	for _, tt := range tests {
		if got := isBotTrigger(tt.input); got != tt.expected {
			t.Errorf("isBotTrigger(%q) = %v, want %v", tt.input, got, tt.expected)
		}
	}
}
