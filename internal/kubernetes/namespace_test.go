package kubernetes

import "testing"

func TestMatchNamespace(t *testing.T) {
	tests := []struct {
		name    string
		ns      string
		include []string
		exclude []string
		want    bool
		wantErr bool
	}{
		{
			name:    "empty include never matches",
			ns:      "default",
			include: nil,
			want:    false,
		},
		{
			name:    "match-all dotstar",
			ns:      "default",
			include: []string{".*"},
			want:    true,
		},
		{
			name:    "exact prefix match",
			ns:      "team-a",
			include: []string{"^team-.*"},
			want:    true,
		},
		{
			name:    "no pattern hits",
			ns:      "kube-system",
			include: []string{"^team-.*"},
			want:    false,
		},
		{
			name:    "exclude wins over include",
			ns:      "kube-system",
			include: []string{".*"},
			exclude: []string{"^kube-.*"},
			want:    false,
		},
		{
			name:    "multiple include patterns - any hit",
			ns:      "billing",
			include: []string{"^team-.*", "^bill.*"},
			want:    true,
		},
		{
			name:    "multiple exclude patterns - any hit blocks",
			ns:      "openshift-sdn",
			include: []string{".*"},
			exclude: []string{"^kube-.*", "^openshift-.*"},
			want:    false,
		},
		{
			name:    "match included but excluded explicitly",
			ns:      "team-secret",
			include: []string{"^team-.*"},
			exclude: []string{".*-secret$"},
			want:    false,
		},
		{
			name:    "invalid include regex returns error",
			ns:      "anything",
			include: []string{"[unterminated"},
			wantErr: true,
		},
		{
			name:    "invalid exclude regex returns error when include matched",
			ns:      "default",
			include: []string{".*"},
			exclude: []string{"[bad"},
			wantErr: true,
		},
		{
			name:    "invalid exclude regex ignored when include did not match",
			ns:      "default",
			include: []string{"^never-.*"},
			exclude: []string{"[bad"},
			want:    false,
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := MatchNamespace(tt.ns, tt.include, tt.exclude)
			if (err != nil) != tt.wantErr {
				t.Fatalf("MatchNamespace() error = %v, wantErr %v", err, tt.wantErr)
			}
			if !tt.wantErr && got != tt.want {
				t.Errorf("MatchNamespace() = %v, want %v", got, tt.want)
			}
		})
	}
}
