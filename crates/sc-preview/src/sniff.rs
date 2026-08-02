//! Magic-byte MIME sniffing.
//!
//! Never trust a file extension or a caller-supplied `Content-Type`. Only the
//! first ~8192 bytes of the file are read, and classification is done purely
//! from magic bytes via `infer`.

use std::io::Read;

/// Cap matches's `head` buffer size.
pub const SNIFF_HEAD_BYTES: usize = 8192;

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Sniffed {
    Known(&'static str),
    Unknown,
}

impl Sniffed {
    pub fn mime_type(&self) -> Option<&'static str> {
        match self {
            Sniffed::Known(m) => Some(m),
            Sniffed::Unknown => None,
        }
    }

    pub fn is_image(&self) -> bool {
        matches!(self.mime_type(), Some(m) if m.starts_with("image/"))
    }

    /// Same magic-byte discipline as [`Sniffed::is_image`], for the other
    /// half of's job split
    /// (`crate::worker::JobKind::Image` vs `::Video`). This is what lets
    /// `PreviewService::get_or_generate` route a video file to the job kind
    /// that actually reports "not implemented" instead of falling into the
    /// image decoder and coming back as a generic, misleading decode error.
    pub fn is_video(&self) -> bool {
        matches!(self.mime_type(), Some(m) if m.starts_with("video/"))
    }
}

/// Classify from an in-memory head buffer (already read, e.g. by the caller
/// via `pread` at offset 0). Only the first [`SNIFF_HEAD_BYTES`] bytes are
/// ever consulted, even if a longer slice is passed in.
pub fn sniff_head(head: &[u8]) -> Sniffed {
    let bounded = &head[..head.len().min(SNIFF_HEAD_BYTES)];
    match infer::get(bounded) {
        Some(t) => Sniffed::Known(t.mime_type()),
        None => Sniffed::Unknown,
    }
}

/// Classify by reading only the first [`SNIFF_HEAD_BYTES`] bytes from an
/// arbitrary reader. Does not seek — callers who need the reader positioned
/// back at the start must rewind themselves (e.g. wrap in `Cursor` or
/// `seek(SeekFrom::Start(0))` a real file after calling this).
pub fn sniff_reader(mut r: impl Read) -> std::io::Result<Sniffed> {
    let mut buf = [0u8; SNIFF_HEAD_BYTES];
    let mut total = 0usize;
    while total < buf.len() {
        let n = r.read(&mut buf[total..])?;
        if n == 0 {
            break;
        }
        total += n;
    }
    Ok(sniff_head(&buf[..total]))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn sniffs_png_by_magic_bytes_regardless_of_extension() {
        let png_bytes: &[u8] = &[0x89, b'P', b'N', b'G', 0x0D, 0x0A, 0x1A, 0x0A, 0, 0, 0, 0];
        assert_eq!(sniff_head(png_bytes), Sniffed::Known("image/png"));
    }

    #[test]
    fn does_not_classify_html_masquerading_as_jpeg() {
        // The classic "evil.jpg" that is actually an HTML/SVG payload. Magic
        // bytes say "not an image" even though a filename might end in .jpg.
        let html_bytes = b"<html><body><script>alert(1)</script></body></html>";
        let sniffed = sniff_head(html_bytes);
        assert!(!sniffed.is_image());
    }

    #[test]
    fn reader_variant_matches_head_variant() {
        let png_bytes: &[u8] = &[0x89, b'P', b'N', b'G', 0x0D, 0x0A, 0x1A, 0x0A, 0, 0, 0, 0];
        let sniffed = sniff_reader(std::io::Cursor::new(png_bytes)).unwrap();
        assert_eq!(sniffed, Sniffed::Known("image/png"));
    }

    #[test]
    fn sniffs_webm_by_magic_bytes_as_video_not_image() {
        // The EBML header `infer`'s WebM matcher checks for. Real WebM/MKV
        // files are much larger, but the matcher only inspects these four
        // bytes before falling back to the (longer) Matroska-specific check.
        let webm_bytes: &[u8] = &[0x1A, 0x45, 0xDF, 0xA3];
        let sniffed = sniff_head(webm_bytes);
        assert_eq!(sniffed, Sniffed::Known("video/webm"));
        assert!(sniffed.is_video(), "{sniffed:?}");
        assert!(!sniffed.is_image(), "{sniffed:?}");
    }

    #[test]
    fn an_image_is_never_reported_as_video() {
        let png_bytes: &[u8] = &[0x89, b'P', b'N', b'G', 0x0D, 0x0A, 0x1A, 0x0A, 0, 0, 0, 0];
        assert!(!sniff_head(png_bytes).is_video());
    }
}
