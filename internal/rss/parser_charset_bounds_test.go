package rss

import (
	"bytes"
	"io"
	"runtime"
	"testing"

	"golang.org/x/text/encoding/unicode"
)

// utf16LEBOMBody builds a UTF-16LE, BOM-prefixed body of approximately
// totalBytes bytes (including the 2-byte BOM), repeating one CJK code point
// (U+9F8D, 2 bytes per UTF-16 code unit but 3 bytes once decoded to UTF-8) so
// the decoded form is realistically larger than the source, matching the
// Finding 2 probe's measured ~1.5x expansion.
func utf16LEBOMBody(totalBytes int) []byte {
	data := make([]byte, 0, totalBytes+2)
	data = append(data, 0xFF, 0xFE)
	unit := []byte{0x8D, 0x9F} // U+9F8D, little-endian
	for len(data) < totalBytes+2 {
		data = append(data, unit...)
	}
	return data[:totalBytes+2]
}

// TestNormalizeFeedReaderStreamsUTF16WithoutMaterializingFullBody pins
// Finding 2: normalizeFeedReader must return an io.Reader without decoding
// the body up front. The allocation delta is measured around the CALL
// itself (before anything is read from the returned reader), because the
// defect was materializing the whole decoded body inside the call -- a lazy
// reader allocates only its small internal wrapper structs at construction
// time and does the actual transcoding as bytes are pulled from it.
func TestNormalizeFeedReaderStreamsUTF16WithoutMaterializingFullBody(t *testing.T) {
	const bodySize = 1 << 20 // 1 MiB
	data := utf16LEBOMBody(bodySize)

	runtime.GC()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)

	reader, transcoded, err := normalizeFeedReader(data)

	var after runtime.MemStats
	runtime.ReadMemStats(&after)

	if err != nil {
		t.Fatalf("normalizeFeedReader() error = %v, want nil", err)
	}
	if !transcoded {
		t.Fatal("transcoded = false, want true for a UTF-16LE BOM")
	}

	delta := after.TotalAlloc - before.TotalAlloc
	t.Logf("TotalAlloc delta constructing the reader over a %d-byte body: %d bytes", len(data), delta)

	const maxConstructionAllocBytes = 64 * 1024
	if delta > maxConstructionAllocBytes {
		t.Fatalf(
			"normalizeFeedReader() allocated %d bytes just to construct the reader, want <= %d (streamed, not buffered)",
			delta, maxConstructionAllocBytes,
		)
	}

	// Correctness check, outside the measured window: draining the reader
	// must still fully decode the body.
	decoded, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("reading decoded body error = %v, want nil", err)
	}
	if len(decoded) == 0 {
		t.Fatal("decoded body is empty")
	}
}

// TestNormalizeFeedReaderMatchesOldBehaviorForMalformedUTF16 pins that a
// truncated or invalid-surrogate UTF-16LE body does NOT error under the old
// buffered decode (unicode.UTF16(_, ExpectBOM) only errors on a *missing*
// BOM, and normalizeFeedBOM/normalizeFeedReader only reach it after a BOM
// has already matched), so there is no error-message change to pin. Instead
// this asserts the streamed decode is byte-identical to the same x/text
// primitive the removed decodeBOMUnicode called directly -- both substitute
// U+FFFD for the malformed bytes, with err == nil.
func TestNormalizeFeedReaderMatchesOldBehaviorForMalformedUTF16(t *testing.T) {
	tests := []struct {
		name string
		body []byte
	}{
		{
			name: "odd byte count (truncated trailing byte)",
			body: []byte{0xFF, 0xFE, 0x41, 0x00, 0x42},
		},
		{
			name: "unpaired low surrogate",
			body: []byte{0xFF, 0xFE, 0x00, 0xDC, 0x41, 0x00},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Ground truth: the same x/text primitive the removed
			// decodeBOMUnicode called directly. Per TESTING.md this never
			// errors once a BOM has matched; it substitutes U+FFFD.
			want, err := unicode.UTF16(unicode.LittleEndian, unicode.ExpectBOM).NewDecoder().Bytes(tc.body)
			if err != nil {
				t.Fatalf("reference decode error = %v, want nil (structurally unreachable)", err)
			}

			reader, transcoded, err := normalizeFeedReader(tc.body)
			if err != nil {
				t.Fatalf("normalizeFeedReader() error = %v, want nil", err)
			}
			if !transcoded {
				t.Fatal("transcoded = false, want true")
			}
			got, err := io.ReadAll(reader)
			if err != nil {
				t.Fatalf("reading streamed decode error = %v, want nil", err)
			}
			if !bytes.Equal(got, want) {
				t.Fatalf("streamed decode = %q, want %q (byte-identical to the old buffered decode)", got, want)
			}
		})
	}
}
