//! Blocking counting semaphore bounding concurrent Argon2 invocations
//! (`DESIGN-AUTH.md` §2.2: peak memory = `m_cost × parallelism`, default
//! 48 MiB × 4 = 192 MiB).
//!
//! `tokio::sync::Semaphore` only exposes an `.await`-based `acquire`, which
//! is fine for the async login/verify paths but cannot be used from the
//! synchronous ones (`create_user`, `set_password`, `totp_enroll`, ...) —
//! a sync fn cannot `.await`. Splitting the gate in two (one tokio
//! semaphore for async callers, one separate primitive for sync callers)
//! would silently double the real cap, since the two would draw from
//! independent permit pools. `ArgonGate` instead blocks the *calling OS
//! thread*, so it works identically from a plain sync fn and from inside
//! `tokio::task::spawn_blocking` (which async callers use to keep the CPU-
//! bound hash off the runtime's worker threads) — there is exactly one
//! pool of permits, shared by every caller.
use std::sync::atomic::{AtomicUsize, Ordering};
use std::sync::{Arc, Condvar, Mutex};

pub(crate) struct ArgonGate {
    /// Permits currently available to hand out.
    available: Mutex<usize>,
    cvar: Condvar,

    /// Permits currently held — i.e. Argon2 invocations in flight right
    /// now. Not the same as `argon2_calls` (cumulative, in `lib.rs`): this
    /// one goes down again on completion, which is what lets
    /// `high_water` prove the *peak* concurrency, not just the total count.
    concurrent: AtomicUsize,
    /// High-water mark of `concurrent` since construction. Test-observed
    /// proof that the gate actually bounds peak memory as designed.
    high_water: AtomicUsize,
}

impl ArgonGate {
    pub(crate) fn new(limit: usize) -> Self {
        let limit = limit.max(1);
        Self {
            available: Mutex::new(limit),
            cvar: Condvar::new(),
            concurrent: AtomicUsize::new(0),
            high_water: AtomicUsize::new(0),
        }
    }

    /// Blocks the calling OS thread until a permit is free. Safe to call
    /// both from an ordinary sync fn and from inside `spawn_blocking` —
    /// unsafe/incorrect to call from a plain async fn body directly (it
    /// would block a runtime worker thread instead of yielding).
    pub(crate) fn acquire(self: &Arc<Self>) -> ArgonPermit {
        let mut avail = self.available.lock().unwrap_or_else(|e| e.into_inner());
        while *avail == 0 {
            avail = self.cvar.wait(avail).unwrap_or_else(|e| e.into_inner());
        }
        *avail -= 1;
        drop(avail);
        self.mark_acquired();
        ArgonPermit { gate: Arc::clone(self) }
    }

    #[cfg(test)]
    pub(crate) fn try_acquire(self: &Arc<Self>) -> Option<ArgonPermit> {
        let mut avail = self.available.lock().unwrap_or_else(|e| e.into_inner());
        if *avail == 0 {
            return None;
        }
        *avail -= 1;
        drop(avail);
        self.mark_acquired();
        Some(ArgonPermit { gate: Arc::clone(self) })
    }

    #[cfg(test)]
    pub(crate) fn available_permits(&self) -> usize {
        *self.available.lock().unwrap_or_else(|e| e.into_inner())
    }

    fn mark_acquired(&self) {
        let now = self.concurrent.fetch_add(1, Ordering::SeqCst) + 1;
        self.high_water.fetch_max(now, Ordering::SeqCst);
    }

    fn release(&self) {
        self.concurrent.fetch_sub(1, Ordering::SeqCst);
        let mut avail = self.available.lock().unwrap_or_else(|e| e.into_inner());
        *avail += 1;
        drop(avail);
        self.cvar.notify_one();
    }

    /// Concurrent Argon2 invocations right now. Test/metrics only.
    #[cfg(test)]
    pub(crate) fn concurrent(&self) -> usize {
        self.concurrent.load(Ordering::SeqCst)
    }

    /// Peak concurrent Argon2 invocations since construction. Test-only —
    /// proves the gate actually bounded peak memory as designed.
    #[cfg(test)]
    pub(crate) fn high_water(&self) -> usize {
        self.high_water.load(Ordering::SeqCst)
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
