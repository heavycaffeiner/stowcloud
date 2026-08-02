//! Bomb-protected image decode (`DESIGN-PREVIEW.md` §4.3).
//!
//! The order of operations is the whole point: read *only* the header
//! (`ImageReader::into_decoder` parses just enough to know the format's
//! declared dimensions — for every codec in this crate's feature set that
//! means the SOF/IHDR-equivalent marker, never the compressed pixel data)
//! and reject on declared pixel count before ever asking the decoder to
//! materialize a pixel buffer.

use image::ImageDecoder;

use crate::error::PreviewError;

/// Resource limits applied before any pixel data is touched.
#[derive(Debug, Clone)]
pub struct DecodeLimits {
    /// Maximum `width * height` (as `u64`, to avoid the overflow that a
    /// `u32 * u32` multiplication would hit well before reaching values an
    /// attacker would actually use). `DESIGN-PREVIEW.md` §4.3 default:
    /// 100_000_000.
    pub max_pixels: u64,
    /// Forwarded to `image::Limits::max_alloc` as defense in depth — some
    /// decoders (notably PNG) apply this during header parsing itself, so a
    /// crafted header can be rejected even before our own `max_pixels`
    /// check runs.
    pub max_alloc_bytes: u64,
}

impl Default for DecodeLimits {
    fn default() -> Self {
        Self {
            max_pixels: 100_000_000,
            max_alloc_bytes: 256 * 1024 * 1024,
        }
    }
}

/// Decode `bytes` into a [`image::DynamicImage`], rejecting decompression
/// bombs before any pixel buffer is allocated.
///
/// Order of operations, deliberately:
/// 1. Guess the format from magic bytes (never trust an extension).
/// 2. Build a decoder — for every format we support this only parses the
///    header (dimensions, color type), not the compressed body.
/// 3. Check `width * height` against `limits.max_pixels`. Bail out here on
///    overflow, *before* calling `DynamicImage::from_decoder`, which is the
///    call that would actually allocate the full output pixel buffer.
pub fn decode_bounded(bytes: &[u8], limits: &DecodeLimits) -> Result<image::DynamicImage, PreviewError> {
    let cursor = std::io::Cursor::new(bytes);
    let mut reader = image::ImageReader::new(cursor)
        .with_guessed_format()
        .map_err(PreviewError::Io)?;

    if reader.format().is_none() {
        return Err(PreviewError::UnsupportedFormat);
    }

    let mut ilimits = image::Limits::default();
    ilimits.max_alloc = Some(limits.max_alloc_bytes);
    reader.limits(ilimits);

    // Consumes `reader` into a decoder. For PNG this already re-checks
    // dimensions against whatever `image::Limits` we set (see
    // `png::PngDecoder::with_limits`); for the other formats in our feature
    // set the constructor only parses the header marker. Either way, no
    // pixel buffer has been allocated yet.
    let decoder = reader
        .into_decoder()
        .map_err(|e| PreviewError::Decode(e.to_string()))?;

    let (width, height) = decoder.dimensions();
    let pixels = u64::from(width) * u64::from(height);
    if pixels > limits.max_pixels {
        return Err(PreviewError::DecodeBomb {
            width,
            height,
            pixels,
            max_pixels: limits.max_pixels,
        });
    }

    image::DynamicImage::from_decoder(decoder).map_err(|e| PreviewError::Decode(e.to_string()))
}

/// Read just the declared dimensions without decoding pixel data. Exposed
/// separately for callers (e.g. a future `sc-http` listing endpoint) that
/// only want dimensions and would rather not pull in the rest of the
/// decode path.
pub fn read_dimensions(bytes: &[u8], limits: &DecodeLimits) -> Result<(u32, u32), PreviewError> {
    let cursor = std::io::Cursor::new(bytes);
    let mut reader = image::ImageReader::new(cursor)
        .with_guessed_format()
        .map_err(PreviewError::Io)?;
    if reader.format().is_none() {
        return Err(PreviewError::UnsupportedFormat);
    }
    let mut ilimits = image::Limits::default();
    ilimits.max_alloc = Some(limits.max_alloc_bytes);
    reader.limits(ilimits);
    let decoder = reader
        .into_decoder()
        .map_err(|e| PreviewError::Decode(e.to_string()))?;
    let (width, height) = decoder.dimensions();
    let pixels = u64::from(width) * u64::from(height);
    if pixels > limits.max_pixels {
        return Err(PreviewError::DecodeBomb {
            width,
            height,
            pixels,
            max_pixels: limits.max_pixels,
        });
    }
    Ok((width, height))
}

#[cfg(test)]
mod tests {
    use super::*;

    /// Standard reflected CRC-32 (poly 0xEDB88320), the variant PNG chunks
    /// use. Implemented by hand (rather than pulling in a crate) because
    /// it's only needed to make a hand-patched test fixture's chunk CRC
    /// valid so the PNG decoder accepts the header at all.
    fn crc32(data: &[u8]) -> u32 {
        let mut crc: u32 = 0xFFFF_FFFF;
        for &byte in data {
            crc ^= u32::from(byte);
            for _ in 0..8 {
                let mask = 0u32.wrapping_sub(crc & 1);
                crc = (crc >> 1) ^ (0xEDB8_8320 & mask);
            }
        }
        !crc
    }

    /// Build a structurally valid PNG (correct signature, correct IHDR CRC,
    /// a real — if tiny and irrelevant — IDAT stream) whose IHDR falsely
    /// declares a 100000x100000 image. The real pixel data underneath is
    /// for a 1x1 image, so this file is "corrupt" in the sense that nobody
    /// could actually decode 100000x100000 pixels out of it — which is
    /// exactly the point: rejection must happen at the header-read stage,
    /// before any attempt is made to decode the (mismatched, garbage-by-
    /// declared-size) body.
    fn make_bomb_png(width: u32, height: u32) -> Vec<u8> {
        let real = image::RgbImage::from_pixel(1, 1, image::Rgb([1, 2, 3]));
        let mut bytes = Vec::new();
        image::DynamicImage::ImageRgb8(real)
            .write_to(&mut std::io::Cursor::new(&mut bytes), image::ImageFormat::Png)
            .unwrap();

        // PNG layout: 8-byte signature, then IHDR chunk:
        // [len:4][type:4=="IHDR"][width:4][height:4][bitdepth:1][colortype:1]
        // [compression:1][filter:1][interlace:1][crc:4]
        assert_eq!(&bytes[12..16], b"IHDR");
        bytes[16..20].copy_from_slice(&width.to_be_bytes());
        bytes[20..24].copy_from_slice(&height.to_be_bytes());
        let crc = crc32(&bytes[12..29]); // "IHDR" + 13 bytes of data
        bytes[29..33].copy_from_slice(&crc.to_be_bytes());
        bytes
    }

    #[test]
    fn rejects_huge_declared_dimensions_without_decoding() {
        let bomb = make_bomb_png(100_000, 100_000);
        let limits = DecodeLimits::default();

        let err = decode_bounded(&bomb, &limits).expect_err("bomb PNG must be rejected");
        match err {
            PreviewError::DecodeBomb {
                width,
                height,
                pixels,
                max_pixels,
            } => {
                assert_eq!(width, 100_000);
                assert_eq!(height, 100_000);
                assert_eq!(pixels, 100_000u64 * 100_000u64);
                assert_eq!(max_pixels, limits.max_pixels);
            }
            other => panic!("expected DecodeBomb, got {other:?}"),
        }
    }

    #[test]
    fn read_dimensions_also_rejects_the_bomb() {
        let bomb = make_bomb_png(100_000, 100_000);
        let err = read_dimensions(&bomb, &DecodeLimits::default()).expect_err("must reject");
        assert!(matches!(err, PreviewError::DecodeBomb { .. }));
    }

    #[test]
    fn accepts_a_normal_small_image() {
        let bomb = make_bomb_png(1, 1); // not actually a bomb, just reusing the builder
        let img = decode_bounded(&bomb, &DecodeLimits::default()).unwrap();
        assert_eq!((img.width(), img.height()), (1, 1));
    }

    #[test]
    fn rejects_unrecognized_bytes() {
        let garbage = vec![0u8; 64];
        let err = decode_bounded(&garbage, &DecodeLimits::default()).unwrap_err();
        assert!(matches!(err, PreviewError::UnsupportedFormat));
    }
}
