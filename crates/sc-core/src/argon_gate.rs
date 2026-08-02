//! Bounds concurrent Argon2 invocations that `sc-core` performs directly —
//! today, exactly the share-link password path in `links.rs`.
//!
//! ## Why this exists
//!
//! `check_link_password` is reachable by anyone holding (or guessing) a share
//! token: no session, no login rate limit, nothing upstream of it. Before
//! this module existed it called `sc_auth::password::verify_phc` (and, on
//! creation, `hash_phc`) with no bound at all — any number of concurrent
//! requests each stood up their own Argon2id buffer (`m_cost` 48 MiB by default), so an attacker who fired enough parallel
//! requests at a public link controlled server memory directly. That is
//! precisely the class of DoS `sc-auth`'s own `ArgonGate` exists to close
//! for login traffic; a public share link is just as unauthenticated as a
//! login attempt, so it needs the same shape of defense.
//!
//! ## Why this is a second gate, not the same object
//!
//! `sc-auth::AuthService` already owns exactly this kind of semaphore, sized
//! at `AuthConfig::argon2_parallelism`. The textbook fix is one shared pool
//! so the *combined* peak (logins + link checks) stays at one
//! `m_cost × parallelism` budget rather than two. That gate is private to
//! `sc-auth` (`pub(crate) argon2_gate` in its `AuthService`) and `sc-auth` is
//! not a crate this change touches — so sharing the literal object would
//! require adding a public accessor there first. Until that lands, this
//! module bounds `sc-core`'s own Argon2 traffic independently, using the
//! same `cfg.argon2_parallelism` value `LinkStore` already carries. Worst
//! case the two independent pools run at once and peak memory is
//! `2 × m_cost × parallelism` instead of `1×` — still a hard bound, which is
//! the property that matters: before this, the share-link path had *no*
//! bound whatsoever. Unifying the two pools into one is a follow-up once
//! `sc-auth` exposes its gate; tracked so the next person touching this
//! finds the seam rather than re-deriving it.
//!
//! The blocking-semaphore shape (a `Mutex`-guarded counter plus a
//! `Condvar`, not `tokio::sync::Semaphore`) mirrors `sc-auth::argon_gate`
//! for the same reason: `check_link_password` is a plain sync fn, callable
//! from a non-async context, and its doc comment already requires async
//! callers to run it inside `spawn_blocking` — this gate's `acquire` blocks
//! the calling OS thread, which is correct in both cases.

use std::sync::Arc;

use parking_lot::{Condvar, Mutex};

pub(crate) struct ArgonGate {
    available: Mutex<usize>,
    cvar: Condvar,

    /// Permits currently held. Test-only introspection, mirroring
    /// `sc-auth::argon_gate::ArgonGate` — proof that the gate actually
    /// bounds peak concurrency rather than just existing unused.
    #[cfg(test)]
    concurrent: std::sync::atomic::AtomicUsize,
    #[cfg(test)]
    high_water: std::sync::atomic::AtomicUsize,
}

impl ArgonGate {
    pub(crate) fn new(limit: usize) -> Self {
        Self {
            available: Mutex::new(limit.max(1)),
            cvar: Condvar::new(),
            #[cfg(test)]
            concurrent: std::sync::atomic::AtomicUsize::new(0),
            #[cfg(test)]
            high_water: std::sync::atomic::AtomicUsize::new(0),
        }
    }

    /// Blocks the calling OS thread until a permit is free.
    pub(crate) fn acquire(self: &Arc<Self>) -> ArgonPermit {
        let mut avail = self.available.lock();
        while *avail == 0 {
            self.cvar.wait(&mut avail);
        }
        *avail -= 1;
        drop(avail);
        #[cfg(test)]
        {
            use std::sync::atomic::Ordering;
            let now = self.concurrent.fetch_add(1, Ordering::SeqCst) + 1;
            self.high_water.fetch_max(now, Ordering::SeqCst);
        }
        ArgonPermit { gate: Arc::clone(self) }
    }

    fn release(&self) {
        #[cfg(test)]
        self.concurrent.fetch_sub(1, std::sync::atomic::Ordering::SeqCst);
        let mut avail = self.available.lock();
        *avail += 1;
        drop(avail);
        self.cvar.notify_one();
    }

    /// Peak concurrent Argon2 invocations since construction. Test-only:
    /// proves the gate actually bounded peak memory as designed.
    #[cfg(test)]
    pub(crate) fn high_water(&self) -> usize {
        self.high_water.load(std::sync::atomic::Ordering::SeqCst)
    }
}

pub(crate) struct ArgonPermit {
    gate: Arc<ArgonGate>,
}

impl Drop for ArgonPermit {
    fn drop(&mut self) {
        self.gate.release();
    }
}
