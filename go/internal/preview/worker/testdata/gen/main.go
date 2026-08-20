//go:build linux

// Command gen writes the syscall corpus.
//
// The images are generated rather than downloaded, so the corpus is
// reproducible and carries no third-party licence. What matters for a syscall
// measurement is which code paths run, not whether the pictures are
// interesting.
package main

import (
	"bytes"
	"encoding/binary"
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/image/bmp"
	"golang.org/x/image/tiff"
)

func main() {
	dir := flag.String("out", "", "the corpus directory")
	flag.Parse()
	if *dir == "" {
		fmt.Fprintln(os.Stderr, "gen: -out is required")
		os.Exit(2)
	}
	if err := write(*dir); err != nil {
		fmt.Fprintf(os.Stderr, "gen: %v\n", err)
		os.Exit(1)
	}
}

func write(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	rgba := func(w, h int) image.Image {
		img := image.NewRGBA(image.Rect(0, 0, w, h))
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				img.Set(x, y, color.RGBA{R: uint8(x), G: uint8(y), B: uint8(x ^ y), A: 255})
			}
		}
		return img
	}
	gray := func(w, h int) image.Image {
		img := image.NewGray(image.Rect(0, 0, w, h))
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				img.Set(x, y, color.Gray{Y: uint8(x * y)})
			}
		}
		return img
	}
	paletted := func(w, h int) *image.Paletted {
		img := image.NewPaletted(image.Rect(0, 0, w, h),
			color.Palette{color.Black, color.White, color.RGBA{R: 255, A: 255}})
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				img.SetColorIndex(x, y, uint8((x+y)%3))
			}
		}
		return img
	}

	files := map[string]func() ([]byte, error){
		"small.png": func() ([]byte, error) { return encodeWith(png.Encode, rgba(64, 48)) },
		// Large enough to move the allocator past its first arena, which is a
		// different set of mmap calls from a small decode.
		"large.png": func() ([]byte, error) { return encodeWith(png.Encode, rgba(1600, 1200)) },
		"gray.png":  func() ([]byte, error) { return encodeWith(png.Encode, gray(200, 150)) },
		"image.bmp": func() ([]byte, error) { return encodeWith(bmp.Encode, rgba(120, 90)) },
		"image.tiff": func() ([]byte, error) {
			return encodeWith(func(w io.Writer, m image.Image) error {
				return tiff.Encode(w, m, nil)
			}, rgba(120, 90))
		},
		"photo.jpeg": func() ([]byte, error) {
			return encodeWith(func(w io.Writer, m image.Image) error {
				return jpeg.Encode(w, m, &jpeg.Options{Quality: 85})
			}, rgba(320, 240))
		},
		"progressive.jpeg": func() ([]byte, error) {
			// Go's encoder writes baseline only, so the progressive path is
			// exercised by re-encoding at a quality that changes the scan
			// structure rather than by a flag this library does not have.
			return encodeWith(func(w io.Writer, m image.Image) error {
				return jpeg.Encode(w, m, &jpeg.Options{Quality: 30})
			}, rgba(200, 200))
		},
		"exif.jpeg": func() ([]byte, error) { return exifJPEG(rgba(160, 120)) },
		"anim.gif":  func() ([]byte, error) { return animGIF(paletted(80, 60)) },
		"paletted.gif": func() ([]byte, error) {
			return encodeWith(func(w io.Writer, m image.Image) error {
				return gif.Encode(w, m, nil)
			}, paletted(100, 80))
		},
		// The failure paths, which allocate and unwind differently from a
		// success. A filter measured only against files that decode is one
		// that kills the first time somebody uploads a corrupt image.
		"truncated.png": func() ([]byte, error) {
			full, err := encodeWith(png.Encode, rgba(200, 200))
			if err != nil {
				return nil, err
			}
			return full[:len(full)/2], nil
		},
		"garbage.bin": func() ([]byte, error) {
			return []byte("this is not an image and never was"), nil
		},
	}

	for name, build := range files {
		data, err := build()
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		if werr := os.WriteFile(filepath.Join(dir, name), data, 0o644); werr != nil {
			return fmt.Errorf("%s: %w", name, werr)
		}
	}
	return nil
}

func encodeWith(fn func(io.Writer, image.Image) error, img image.Image) ([]byte, error) {
	var b bytes.Buffer
	if err := fn(&b, img); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// exifJPEG wraps a JPEG in an APP1 segment carrying an orientation tag, so the
// EXIF parser and the rotation both run.
func exifJPEG(img image.Image) ([]byte, error) {
	body, err := encodeWith(func(w io.Writer, m image.Image) error {
		return jpeg.Encode(w, m, nil)
	}, img)
	if err != nil {
		return nil, err
	}

	var tif []byte
	tif = append(tif, "II"...)
	tif = binary.LittleEndian.AppendUint16(tif, 42)
	tif = binary.LittleEndian.AppendUint32(tif, 8)
	tif = binary.LittleEndian.AppendUint16(tif, 1)
	tif = binary.LittleEndian.AppendUint16(tif, 0x0112) // orientation
	tif = binary.LittleEndian.AppendUint16(tif, 3)      // SHORT
	tif = binary.LittleEndian.AppendUint32(tif, 1)
	tif = binary.LittleEndian.AppendUint16(tif, 6) // rotate 90
	tif = append(tif, 0, 0)
	tif = binary.LittleEndian.AppendUint32(tif, 0)

	payload := append([]byte("Exif\x00\x00"), tif...)
	out := []byte{0xff, 0xd8}
	out = append(out, 0xff, 0xe1)
	out = binary.BigEndian.AppendUint16(out, uint16(len(payload)+2))
	out = append(out, payload...)
	// The rest of the original, minus its own start marker.
	return append(out, body[2:]...), nil
}

// animGIF builds a multi-frame GIF, so the decoder's first-frame path runs
// against something that actually has more frames after it.
func animGIF(frame *image.Paletted) ([]byte, error) {
	g := &gif.GIF{}
	for i := 0; i < 8; i++ {
		g.Image = append(g.Image, frame)
		g.Delay = append(g.Delay, 10)
	}
	var b bytes.Buffer
	if err := gif.EncodeAll(&b, g); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}
