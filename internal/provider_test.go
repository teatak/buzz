package buzz

import "testing"

func TestProviderRequestPathPreservesBasePrefix(t *testing.T) {
	tests := []struct {
		name        string
		basePath    string
		requestPath string
		want        string
	}{
		{
			name:        "root base keeps protocol version",
			basePath:    "https://api.anthropic.com",
			requestPath: "/v1/messages",
			want:        "https://api.anthropic.com/v1/messages",
		},
		{
			name:        "custom prefix keeps protocol version",
			basePath:    "https://api.xiaomimimo.com/anthropic",
			requestPath: "/v1/messages",
			want:        "https://api.xiaomimimo.com/anthropic/v1/messages",
		},
		{
			name:        "versioned base deduplicates protocol version",
			basePath:    "https://api.openai.com/v1",
			requestPath: "/v1/chat/completions",
			want:        "https://api.openai.com/v1/chat/completions",
		},
		{
			name:        "nested versioned base deduplicates protocol version",
			basePath:    "https://example.com/proxy/v1/",
			requestPath: "v1/models",
			want:        "https://example.com/proxy/v1/models",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := providerRequestPath(tt.basePath, tt.requestPath); got != tt.want {
				t.Fatalf("providerRequestPath(%q, %q) = %q, want %q", tt.basePath, tt.requestPath, got, tt.want)
			}
		})
	}
}
