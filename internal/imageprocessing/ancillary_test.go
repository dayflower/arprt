package imageprocessing

import (
	"context"
	"testing"
)

func TestAncillaryStructureAndOrdering(t *testing.T) {
	base := testChunks(testPNG(1, 1, 3, 4, false, []byte{0, 0, 0}, func(x, y, c int) uint16 { return 0 }))
	for _, tc := range []struct {
		chunk         testChunk
		beforePalette bool
	}{
		{testChunk{"cHRM", make([]byte, 32)}, true},
		{testChunk{"gAMA", []byte{0, 0, 177, 143}}, true},
		{testChunk{"iCCP", append([]byte("Profile\x00\x00"), testDeflate([]byte("ignored"))...)}, true},
		{testChunk{"sBIT", []byte{8, 8, 8}}, true},
		{testChunk{"sRGB", []byte{0}}, true},
		{testChunk{"cICP", []byte{1, 13, 0, 1}}, true},
		{testChunk{"mDCV", make([]byte, 24)}, true},
		{testChunk{"cLLI", make([]byte, 8)}, true},
		{testChunk{"bKGD", []byte{0}}, false},
		{testChunk{"hIST", []byte{0, 1}}, false},
		{testChunk{"pHYs", make([]byte, 9)}, false},
		{testChunk{"eXIf", []byte{'I', 'I', 42, 0, 8, 0, 0, 0}}, false},
		{testChunk{"tIME", []byte{7, 234, 10, 17, 0, 0, 0}}, false},
	} {
		t.Run(tc.chunk.kind, func(t *testing.T) {
			var good, duplicate []byte
			if tc.beforePalette {
				good = testFile(base[0], tc.chunk, base[1], base[2], base[3])
				duplicate = testFile(base[0], tc.chunk, tc.chunk, base[1], base[2], base[3])
			} else {
				good = testFile(base[0], base[1], tc.chunk, base[2], base[3])
				duplicate = testFile(base[0], base[1], tc.chunk, tc.chunk, base[2], base[3])
			}
			if _, err := Decode(context.Background(), good); err != nil {
				t.Fatal(err)
			}
			if _, err := Decode(context.Background(), duplicate); err == nil {
				t.Fatal("accepted duplicate declaration")
			}
			late := testFile(base[0], base[1], base[2], tc.chunk, base[3])
			_, err := Decode(context.Background(), late)
			if (err == nil) != (tc.chunk.kind == "tIME") {
				t.Fatalf("late declaration: %v", err)
			}
			if tc.beforePalette {
				if _, err := Decode(context.Background(), testFile(base[0], base[1], tc.chunk, base[2], base[3])); err == nil {
					t.Fatal("accepted color declaration after PLTE")
				}
			}
			if tc.chunk.kind != "eXIf" {
				if _, err := Decode(context.Background(), testFile(base[0], testChunk{tc.chunk.kind, nil}, base[1], base[2], base[3])); err == nil {
					t.Fatal("accepted empty declaration")
				}
			}
		})
	}
	// Truecolor may include a suggested PLTE; grayscale may not. Neither the
	// suggested palette nor background declarations alter the decoded samples.
	for _, kind := range []byte{0, 2, 4, 6} {
		chunks := testChunks(testPNG(1, 1, kind, 8, false, nil, func(x, y, c int) uint16 { return 255 }))
		background := []byte{0, 0}
		if kind == 2 || kind == 6 {
			background = []byte{0, 0, 0, 0, 0, 0}
		}
		if _, err := Decode(context.Background(), testFile(chunks[0], testChunk{"bKGD", background}, chunks[1], chunks[2])); err != nil {
			t.Fatal(err)
		}
		if kind == 2 || kind == 6 {
			if _, err := Decode(context.Background(), testFile(chunks[0], testChunk{"PLTE", []byte{0, 0, 0}}, chunks[1], chunks[2])); err != nil {
				t.Fatal(err)
			}
		}
	}
}
