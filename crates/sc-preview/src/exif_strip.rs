//! EXIF orientation handling.
//!
//! The rule is: read *only* the orientation tag from the source, apply the
//! equivalent rotation/flip to the decoded pixels, then encode the output
//! with no EXIF segment at all. We never copy the source's EXIF blob (or
//! any subset of it, including GPS) into the encoder — `image`'s encoders
//! only embed EXIF if you explicitly call `set_exif_metadata`, which
//! nothing in this crate ever does, so simply not doing that is sufficient
//! to satisfy "strip all EXIF except orientation, GPS must not survive".

use image::ImageEncoder as _;

use crate::error::PreviewError;

/// Read the EXIF orientation tag (if any) from raw source bytes, via
/// `kamadak-exif`. Returns `None` if there is no EXIF, no orientation
/// field, or the container isn't one `kamadak-exif` understands — all of
/// which just mean "apply no transform", not an error.
pub fn read_orientation(bytes: &[u8]) -> Option<image::metadata::Orientation> {
    let mut cursor = std::io::Cursor::new(bytes);
    let exif = exif::Reader::new().read_from_container(&mut cursor).ok()?;
    let field = exif.get_field(exif::Tag::Orientation, exif::In::PRIMARY)?;
    let raw = field.value.get_uint(0)?;
    image::metadata::Orientation::from_exif(u8::try_from(raw).ok()?)
}

/// Encode an 8-bit RGBA image as lossless WebP. `image`'s `WebPEncoder`
/// never writes EXIF/ICC unless `set_exif_metadata`/`set_icc_profile` is
/// called on it explicitly, which we never do — so the output is
/// guaranteed metadata-free by construction.
pub fn encode_webp_rgba8(img: &image::RgbaImage) -> Result<Vec<u8>, PreviewError> {
    let mut out = Vec::new();
    image::codecs::webp::WebPEncoder::new_lossless(&mut out)
        .write_image(
            img.as_raw(),
            img.width(),
            img.height(),
            image::ExtendedColorType::Rgba8,
        )
        .map_err(|e| PreviewError::Encode(e.to_string()))?;
    Ok(out)
}

#[cfg(test)]
mod tests {
    use super::*;
    use image::GenericImageView;

    /// Hand-build a minimal, valid TIFF/EXIF blob (little-endian) with:
    /// - IFD0 entry: Orientation (tag 0x0112) = the given value
    /// - IFD0 entry: GPSInfoIFDPointer (tag 0x8825) -> a GPS IFD
    /// - GPS IFD entry: GPSLatitudeRef (tag 0x0001) = "N"
    ///
    /// Returns the bytes to place *inside* a JPEG APP1 segment, i.e.
    /// starting at `"Exif\0\0"`.
    fn build_exif_app1_payload(orientation: u16) -> Vec<u8> {
        fn entry(tag: u16, ty: u16, count: u32, value: [u8; 4]) -> [u8; 12] {
            let mut e = [0u8; 12];
            e[0..2].copy_from_slice(&tag.to_le_bytes());
            e[2..4].copy_from_slice(&ty.to_le_bytes());
            e[4..8].copy_from_slice(&count.to_le_bytes());
            e[8..12].copy_from_slice(&value);
            e
        }

        let mut tiff = Vec::new();
        tiff.extend_from_slice(b"II"); // little-endian
        tiff.extend_from_slice(&42u16.to_le_bytes());
        tiff.extend_from_slice(&8u32.to_le_bytes()); // offset of IFD0

        // IFD0 starts at offset 8: 2(count) + 2*12(entries) + 4(next) = 30
        // bytes, so the GPS IFD starts at offset 38.
        let gps_ifd_offset: u32 = 8 + 2 + 2 * 12 + 4;
        assert_eq!(gps_ifd_offset, 38);

        tiff.extend_from_slice(&2u16.to_le_bytes()); // IFD0 entry count
        tiff.extend_from_slice(&entry(0x0112, 3 /* SHORT */, 1, {
            let mut v = [0u8; 4];
            v[0..2].copy_from_slice(&orientation.to_le_bytes());
            v
        }));
        tiff.extend_from_slice(&entry(
            0x8825, /* GPSInfoIFDPointer */
            4,      /* LONG */
            1,
            gps_ifd_offset.to_le_bytes(),
        ));
        tiff.extend_from_slice(&0u32.to_le_bytes()); // no IFD1

        assert_eq!(tiff.len() as u32, gps_ifd_offset);

        // GPS IFD: one ASCII field, "N\0" (2 bytes incl. NUL), fits inline.
        tiff.extend_from_slice(&1u16.to_le_bytes());
        tiff.extend_from_slice(&entry(0x0001, 2 /* ASCII */, 2, [b'N', 0, 0, 0]));
        tiff.extend_from_slice(&0u32.to_le_bytes()); // no next IFD

        let mut payload = Vec::new();
        payload.extend_from_slice(b"Exif\0\0");
        payload.extend_from_slice(&tiff);
        payload
    }

    /// Encode a small, easily-fingerprinted JPEG and splice in a hand-built
    /// EXIF APP1 segment (orientation + GPS) right after the SOI marker.
    ///
    /// Uses a solid 8x8 red block in the top-left corner of an otherwise
    /// black 16x16 image, rather than a single distinct pixel: JPEG is a
    /// lossy, block-based codec, and a single-pixel feature gets smeared
    /// across an 8x8 DCT block by quantization, which makes an
    /// exact-color assertion on a single output pixel flaky. An 8x8-aligned
    /// solid block survives compression with its color close to intact in
    /// the block interior.
    fn make_jpeg_with_exif(orientation: u16) -> (Vec<u8>, u32, u32) {
        let (w, h) = (16u32, 16u32);
        let mut img = image::RgbImage::from_pixel(w, h, image::Rgb([0, 0, 0]));
        for y in 0..8 {
            for x in 0..8 {
                img.put_pixel(x, y, image::Rgb([220, 20, 20]));
            }
        }

        let mut jpeg_bytes = Vec::new();
        image::DynamicImage::ImageRgb8(img)
            .write_to(&mut std::io::Cursor::new(&mut jpeg_bytes), image::ImageFormat::Jpeg)
            .unwrap();

        assert_eq!(&jpeg_bytes[0..2], &[0xFF, 0xD8], "expected SOI marker");

        let payload = build_exif_app1_payload(orientation);
        let seg_len = (payload.len() + 2) as u16; // length field includes itself
        let mut app1 = Vec::new();
        app1.extend_from_slice(&[0xFF, 0xE1]);
        app1.extend_from_slice(&seg_len.to_be_bytes());
        app1.extend_from_slice(&payload);

        let mut spliced = Vec::new();
        spliced.extend_from_slice(&jpeg_bytes[0..2]); // SOI
        spliced.extend_from_slice(&app1);
        spliced.extend_from_slice(&jpeg_bytes[2..]); // everything else

        (spliced, w, h)
    }

    #[test]
    fn reads_orientation_and_ignores_it_when_absent() {
        let (jpeg, _w, _h) = make_jpeg_with_exif(6);
        let orientation = read_orientation(&jpeg);
        assert_eq!(orientation, Some(image::metadata::Orientation::Rotate90));

        // A file with no EXIF at all yields None, not an error path taken.
        let mut plain = Vec::new();
        image::DynamicImage::ImageRgb8(image::RgbImage::new(2, 2))
            .write_to(&mut std::io::Cursor::new(&mut plain), image::ImageFormat::Png)
            .unwrap();
        assert_eq!(read_orientation(&plain), None);
    }

    #[test]
    fn orientation_is_applied_and_no_exif_survives_in_the_output() {
        let (jpeg, w, h) = make_jpeg_with_exif(6); // 6 = Rotate90 (90 deg CW)

        let limits = crate::decode::DecodeLimits::default();
        let mut decoded = crate::decode::decode_bounded(&jpeg, &limits).unwrap();
        assert_eq!((decoded.width(), decoded.height()), (w, h));

        let orientation = read_orientation(&jpeg).expect("orientation must be present");
        decoded.apply_orientation(orientation);

        // Rotate90 (90 deg clockwise) swaps width/height, and moves the
        // 8x8 red block that was in the top-left corner to the top-right
        // corner. Sample well inside each expected region's interior
        // (avoiding block edges) to stay robust to minor JPEG ringing.
        assert_eq!((decoded.width(), decoded.height()), (h, w));
        let moved = decoded.get_pixel(decoded.width() - 4, 4).0;
        let untouched = decoded.get_pixel(4, decoded.height() - 4).0;
        assert!(
            i32::from(moved[0]) > i32::from(moved[1]) + 50 && i32::from(moved[0]) > i32::from(moved[2]) + 50,
            "red block should have rotated to the top-right corner, got {moved:?}"
        );
        assert!(
            untouched[0] < 60 && untouched[1] < 60 && untouched[2] < 60,
            "bottom-left corner should have stayed black, got {untouched:?}"
        );

        let rgba = decoded.to_rgba8();
        let webp = encode_webp_rgba8(&rgba).unwrap();

        // The output must decode back to WebP with the rotated dimensions...
        let redecoded = image::load_from_memory_with_format(&webp, image::ImageFormat::WebP).unwrap();
        assert_eq!((redecoded.width(), redecoded.height()), (h, w));
        // ...and must carry no EXIF at all: either kamadak-exif can't find
        // a container-level EXIF chunk in the WebP at all (the expected,
        // normal outcome), or if it somehow parses anything, it must find
        // zero fields (in particular: no Orientation, no GPS).
        match exif::Reader::new().read_from_container(&mut std::io::Cursor::new(&webp)) {
            Err(_) => {}
            Ok(exif) => {
                assert_eq!(exif.fields().count(), 0, "expected zero EXIF fields in output");
            }
        }
    }
}
