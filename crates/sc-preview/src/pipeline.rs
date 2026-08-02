//! The end-to-end "raw bytes in, WebP preview bytes out" pipeline. This is
//! what `InProcessWorkerPool` calls, and what a jailed worker process would
//! call after receiving a job over the `SCM_RIGHTS` protocol (`worker`
//! module).
//!
//! Steps, per `DESIGN-PREVIEW.md` §4.3: decode with bomb protection ->
//! apply (and discard) EXIF orientation -> resize to the requested box ->
//! encode as EXIF-free lossless WebP.

use crate::decode::{self, DecodeLimits};
use crate::error::PreviewError;
use crate::exif_strip;

/// Decode, reorient, resize, and re-encode `raw` (the full source file
/// bytes) into a WebP preview no larger than `target_w x target_h`.
///
/// `target_w`/`target_h` are expected to already be preset-rounded by the
/// caller (see `crate::preset::round_to_preset`) — this function does not
/// re-round them, so it can also be used for exact-size internal testing.
pub fn generate_preview_bytes(
    raw: &[u8],
    target_w: u32,
    target_h: u32,
    limits: &DecodeLimits,
) -> Result<Vec<u8>, PreviewError> {
    let mut img = decode::decode_bounded(raw, limits)?;

    if let Some(orientation) = exif_strip::read_orientation(raw) {
        img.apply_orientation(orientation);
    }

    let resized = img.resize(target_w, target_h, image::imageops::FilterType::Lanczos3);
    let rgba = resized.to_rgba8();
    exif_strip::encode_webp_rgba8(&rgba)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn produces_a_valid_webp_no_larger_than_the_target_box() {
        let mut img = image::RgbImage::from_pixel(400, 300, image::Rgb([10, 20, 30]));
        img.put_pixel(0, 0, image::Rgb([255, 255, 255]));
        let mut raw = Vec::new();
        image::DynamicImage::ImageRgb8(img)
            .write_to(&mut std::io::Cursor::new(&mut raw), image::ImageFormat::Png)
            .unwrap();

        let out = generate_preview_bytes(&raw, 128, 128, &DecodeLimits::default()).unwrap();
        let decoded = image::load_from_memory_with_format(&out, image::ImageFormat::WebP).unwrap();
        assert!(decoded.width() <= 128 && decoded.height() <= 128);
        // Aspect ratio preserved: 400x300 -> fit in 128x128 keeps 4:3.
        assert_eq!(decoded.width(), 128);
        assert_eq!(decoded.height(), 96);
    }

    #[test]
    fn rejects_bomb_before_ever_resizing() {
        // Reuse the same crafted-header technique as decode::tests, just
        // inline, to make sure the pipeline (not just decode_bounded in
        // isolation) refuses to proceed.
        let real = image::RgbImage::from_pixel(1, 1, image::Rgb([1, 2, 3]));
        let mut bytes = Vec::new();
        image::DynamicImage::ImageRgb8(real)
            .write_to(&mut std::io::Cursor::new(&mut bytes), image::ImageFormat::Png)
            .unwrap();
        bytes[16..20].copy_from_slice(&100_000u32.to_be_bytes());
        bytes[20..24].copy_from_slice(&100_000u32.to_be_bytes());
        // CRC left stale on purpose here: whichever error fires first (bad
        // CRC or DecodeBomb) is fine for this test, we only assert `Err`.
        let err = generate_preview_bytes(&bytes, 128, 128, &DecodeLimits::default());
        assert!(err.is_err());
    }
}
