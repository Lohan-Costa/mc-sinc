package logging

import "testing"

func TestSanitizePath(t *testing.T) {
	cases := []struct {
		name        string
		in          string
		home        string
		winProfile  string
		root        string
		want        string
	}{
		{
			name: "home substitui",
			in:   "/Users/ciclomedia/mc-sinc-cross-test/MXF/1/clip.mxf",
			home: "/Users/ciclomedia",
			want: "<USER>/mc-sinc-cross-test/MXF/1/clip.mxf",
		},
		{
			name:       "windows userprofile substitui",
			in:         `C:\Users\Lohan\mc-sinc\file.mxf`,
			winProfile: `C:\Users\Lohan`,
			want:       `<USER>\mc-sinc\file.mxf`,
		},
		{
			name: "root substitui antes do home",
			in:   "/Users/ciclomedia/mc-sinc-cross-test/MXF/1/clip.mxf",
			home: "/Users/ciclomedia",
			root: "/Users/ciclomedia/mc-sinc-cross-test",
			want: "<ROOT>/MXF/1/clip.mxf",
		},
		{
			name: "sem match passa intacto",
			in:   "/tmp/whatever",
			home: "/Users/ciclomedia",
			want: "/tmp/whatever",
		},
		{
			name: "string sem path nao muda",
			in:   "Sessão iniciada",
			home: "/Users/ciclomedia",
			want: "Sessão iniciada",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := sanitizePath(tc.in, tc.home, tc.winProfile, tc.root)
			if got != tc.want {
				t.Errorf("sanitizePath(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
