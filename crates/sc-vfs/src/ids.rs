//! Newtype identifiers used throughout stowcloud.
//!
//! All ids are `Copy`, totally ordered, hashable, and serde-transparent so
//! they can be used directly as SQLite integer columns / JSON fields.

use serde::{Deserialize, Serialize};

macro_rules! id_type {
    ($name:ident, $inner:ty) => {
        #[derive(
            Clone, Copy, PartialEq, Eq, PartialOrd, Ord, Hash, Debug, Serialize, Deserialize,
        )]
        #[serde(transparent)]
        pub struct $name(pub $inner);

        impl $name {
            #[inline]
            pub const fn new(v: $inner) -> Self {
                Self(v)
            }

            #[inline]
            pub const fn get(self) -> $inner {
                self.0
            }
        }

        impl From<$inner> for $name {
            #[inline]
            fn from(v: $inner) -> Self {
                Self(v)
            }
        }

        impl From<$name> for $inner {
            #[inline]
            fn from(v: $name) -> Self {
                v.0
            }
        }

        impl std::fmt::Display for $name {
            fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
                write!(f, "{}", self.0)
            }
        }
    };
}

id_type!(ShareId, u32);
id_type!(UserId, u32);
id_type!(GroupId, u32);
id_type!(FileId, i64);
