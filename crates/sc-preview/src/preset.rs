//! Size-preset rounding.
//!
//! Allowing arbitrary requested preview sizes would blow up the cache (one
//! entry per distinct size ever requested) and turn resizing into a
//! CPU-DoS vector. Instead every request is rounded up to the nearest
//! preset, so the cache key space is bounded and small.

/// Ascending size presets. Requests are rounded *up* to the nearest one.
pub const PRESETS: [u32; 5] = [64, 128, 256, 512, 1024];

/// The largest preset. Requests for a dimension larger than this are
/// clamped down to it rather than served at arbitrary resolution — a
/// documented trade-off: nobody gets a preview larger than 1024px on a side
/// through this path, regardless of what they ask for.
pub const MAX_PRESET: u32 = PRESETS[PRESETS.len() - 1];

/// Round a single dimension up to the nearest preset, clamping to
/// [`MAX_PRESET`] if the request exceeds every preset.
pub fn round_dim_to_preset(v: u32) -> u32 {
    for &p in PRESETS.iter() {
        if v <= p {
            return p;
        }
    }
    MAX_PRESET
}

/// Round a `(w, h)` request to presets independently per dimension. Each
/// dimension is rounded up to its own nearest-or-larger preset (not a joint
/// "nearest preset box") — this keeps the cache key derivation a pure,
/// per-axis function, and matches how callers actually request thumbnails
/// (a target width and a target height that need not be equal).
pub fn round_to_preset(w: u32, h: u32) -> (u32, u32) {
    (round_dim_to_preset(w), round_dim_to_preset(h))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn boundary_cases() {
        assert_eq!(round_dim_to_preset(0), 64);
        assert_eq!(round_dim_to_preset(1), 64);
        assert_eq!(round_dim_to_preset(63), 64);
        assert_eq!(round_dim_to_preset(64), 64);
        assert_eq!(round_dim_to_preset(65), 128);
        assert_eq!(round_dim_to_preset(127), 128);
        assert_eq!(round_dim_to_preset(128), 128);
        assert_eq!(round_dim_to_preset(129), 256);
        assert_eq!(round_dim_to_preset(255), 256);
        assert_eq!(round_dim_to_preset(256), 256);
        assert_eq!(round_dim_to_preset(257), 512);
        assert_eq!(round_dim_to_preset(511), 512);
        assert_eq!(round_dim_to_preset(512), 512);
        assert_eq!(round_dim_to_preset(513), 1024);
        assert_eq!(round_dim_to_preset(1023), 1024);
        assert_eq!(round_dim_to_preset(1024), 1024);
    }

    #[test]
    fn clamps_above_largest_preset() {
        assert_eq!(round_dim_to_preset(1025), 1024);
        assert_eq!(round_dim_to_preset(4000), 1024);
        assert_eq!(round_dim_to_preset(u32::MAX), 1024);
    }

    #[test]
    fn rounds_each_dimension_independently() {
        assert_eq!(round_to_preset(63, 64), (64, 64));
        assert_eq!(round_to_preset(65, 1025), (128, 1024));
        assert_eq!(round_to_preset(1, 1), (64, 64));
    }
}
