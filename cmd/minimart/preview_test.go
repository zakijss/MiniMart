package main

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/png"
	"testing"
)

// TestDecodePreviewRefusesHuge: a tiny PNG whose header claims 4000x4000 (over the cap, small enough for the png package on 32 bits)
// is refused before it is decoded, and a normal one still decodes.
func TestDecodePreviewRefusesHuge(t *testing.T) {
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatal(err)
	}
	if _, err := decodePreview(buf.Bytes()); err != nil {
		t.Fatalf("1x1 preview: %v", err)
	}
	b := buf.Bytes()
	// IHDR: 8-byte signature, length, "IHDR", width, height, ... then CRC
	binary.BigEndian.PutUint32(b[16:], 4000)
	binary.BigEndian.PutUint32(b[20:], 4000)
	binary.BigEndian.PutUint32(b[29:], crc32.ChecksumIEEE(b[12:29]))
	if cfg, _, err := image.DecodeConfig(bytes.NewReader(b)); err != nil || cfg.Width != 4000 {
		t.Fatalf("patched header doesn't parse: %v", err)
	}
	if _, err := decodePreview(b); err == nil {
		t.Fatal("4000x4000 preview decoded")
	}
}
