//! LEB128-style unsigned varints. Posting lists are delta+varint encoded
//! (§4.1); block payloads use the same encoding for lengths.

/// Append `v` to `out`.
pub fn put(out: &mut Vec<u8>, mut v: u64) {
    loop {
        let b = (v & 0x7f) as u8;
        v >>= 7;
        if v == 0 {
            out.push(b);
            return;
        }
        out.push(b | 0x80);
    }
}

/// Read a varint at `*pos`, advancing it. `None` on truncation or overlong
/// encodings.
pub fn get(buf: &[u8], pos: &mut usize) -> Option<u64> {
    let mut v: u64 = 0;
    let mut shift = 0u32;
    loop {
        let b = *buf.get(*pos)?;
        *pos += 1;
        if shift >= 64 {
            return None;
        }
        v |= ((b & 0x7f) as u64) << shift;
        if b & 0x80 == 0 {
            return Some(v);
        }
        shift += 7;
    }
}

/// Encoded width of `v`, in bytes. Used by the size estimator (§6.3).
pub fn len(v: u64) -> usize {
    let mut n = 1;
    let mut v = v >> 7;
    while v != 0 {
        n += 1;
        v >>= 7;
    }
    n
}

/// Delta+varint encode a strictly ascending list of block ids.
pub fn encode_ascending(out: &mut Vec<u8>, ids: &[u32]) {
    let mut prev = 0u64;
    for (i, &id) in ids.iter().enumerate() {
        let id = id as u64;
        if i == 0 {
            put(out, id);
        } else {
            put(out, id - prev);
        }
        prev = id;
    }
}

/// Inverse of [`encode_ascending`].
pub fn decode_ascending(buf: &[u8]) -> Option<Vec<u32>> {
    let mut out = Vec::new();
    let mut pos = 0usize;
    let mut prev = 0u64;
    while pos < buf.len() {
        let d = get(buf, &mut pos)?;
        let v = if out.is_empty() { d } else { prev + d };
        if v > u32::MAX as u64 {
            return None;
        }
        out.push(v as u32);
        prev = v;
    }
    Some(out)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn roundtrip() {
        for v in [0u64, 1, 127, 128, 300, 16383, 16384, u32::MAX as u64, u64::MAX] {
            let mut buf = Vec::new();
            put(&mut buf, v);
            assert_eq!(buf.len(), len(v), "len mismatch for {v}");
            let mut p = 0;
            assert_eq!(get(&buf, &mut p), Some(v));
            assert_eq!(p, buf.len());
        }
    }

    #[test]
    fn ascending_roundtrip() {
        let ids: Vec<u32> = vec![0, 1, 5, 400, 401, 100_000];
        let mut buf = Vec::new();
        encode_ascending(&mut buf, &ids);
        assert_eq!(decode_ascending(&buf).unwrap(), ids);
    }

    #[test]
    fn truncated_is_none() {
        let mut buf = Vec::new();
        put(&mut buf, 300);
        buf.pop();
        let mut p = 0;
        assert_eq!(get(&buf, &mut p), None);
    }
}
