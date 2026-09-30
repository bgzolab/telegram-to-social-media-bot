package SocialMediaUtils

import "testing"

func TestChunkImagePaths(t *testing.T) {
	cases := []struct {
		name  string
		paths []string
		size  int
		want  int
	}{
		{name: "empty input", paths: nil, size: 4, want: 0},
		{name: "invalid size", paths: []string{"a.jpg"}, size: 0, want: 0},
		{name: "single chunk", paths: []string{"a.jpg", "b.jpg", "c.jpg"}, size: 4, want: 1},
		{name: "exact boundary", paths: []string{"a.jpg", "b.jpg", "c.jpg", "d.jpg"}, size: 4, want: 1},
		{name: "two chunks", paths: []string{"a.jpg", "b.jpg", "c.jpg", "d.jpg", "e.jpg"}, size: 4, want: 2},
		{name: "three chunks", paths: []string{"1", "2", "3", "4", "5", "6", "7", "8", "9", "10"}, size: 4, want: 3},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			chunks := chunkImagePaths(tc.paths, tc.size)
			if len(chunks) != tc.want {
				t.Fatalf("expected %d chunks, got %d", tc.want, len(chunks))
			}
			if tc.size <= 0 {
				return
			}

			flattened := make([]string, 0, len(tc.paths))
			for _, chunk := range chunks {
				flattened = append(flattened, chunk...)
			}
			if len(flattened) != len(tc.paths) {
				t.Fatalf("expected %d paths preserved, got %d", len(tc.paths), len(flattened))
			}
			for i := range tc.paths {
				if flattened[i] != tc.paths[i] {
					t.Fatalf("expected original order at %d, got: %+v", i, flattened)
				}
			}
		})
	}
}
