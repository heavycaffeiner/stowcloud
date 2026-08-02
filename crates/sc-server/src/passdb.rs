//! The live end of `sc_auth::PassdbSink`: an NT-hash change in the database
//! turns into a rewritten `smbpasswd` without an operator running anything.
//!
//! is the reason this exists. Deleting a
//! `user_smb_secret` row closes SMB in the database and nowhere else, because
//! `smbd` authenticates against the file this server last published. Linking
//! an OIDC identity deletes that row precisely to close SMB, so without a
//! sink installed the account kept working over SMB with the credential the
//! link had just revoked, until somebody ran `sc-server smb-sync`.
//!
//! Three properties this is built around, in the order they mattered:
//!
//! 1. **Nothing renders inside the caller's transaction.** The sink hangs off
//!    `AuthService` and the render reads that same `AuthService` back
//!    (`export_smbpasswd`, `list_users`). Doing it synchronously would mean
//!    calling into the service from inside one of its own write paths, which
//!    is a re-entrancy hazard at best and a connection-pool stall at worst.
//!    So [`PassdbSink::republish`] only sets a flag. This is the
//!    `smb_sync.mark_dirty(u)` the proposal's §4.3.6 step 2 describes, and
//! names.
//! 2. **Bursts collapse into one render.** Rewriting `smb.conf`/`smbpasswd`/
//!    `passwd` costs a full user and share projection; an admin walking
//!    through five accounts should pay for it once, not five times.
//! 3. **A failed render never reaches the request.** The database change has
//!    already committed by the time the mark lands. A render that fails
//!    leaves the published file stale, which is exactly what this whole
//!    server did before this module existed, so the fallback is the old
//!    behaviour with a loud log line rather than a failed request or a dead
//!    thread.

use std::sync::{Arc, Weak};
use std::time::Duration;

use parking_lot::{Condvar, Mutex};

/// How long a mark waits for company before the render runs.
///
/// Long enough that the handful of writes behind one admin action collapse
/// into a single render, short enough that a linked account's SMB access is
/// closed in the published file while the user is still looking at the
/// confirmation screen. It is not a correctness knob: any value works, and
/// a longer one only widens the window in which the file is stale.
const COALESCE_WINDOW: Duration = Duration::from_millis(250);

/// What the publisher thread calls to actually rewrite Samba's files.
///
/// A trait rather than a direct call to [`crate::smb_cmd::render_live`] for
/// two reasons. The render needs the *live* config (the settings screen can
/// change `smb.workgroup` and the service user at runtime), which lives
/// behind `SettingsBridge`'s lock and nowhere else; and the coalescing and
/// failure behaviour below is worth testing without standing up a whole
/// `App`, which the fake in this module's tests does.
pub trait PassdbRender: Send + Sync {
    fn render_passdb(&self) -> anyhow::Result<()>;
}

#[derive(Default)]
struct State {
    /// An NT hash changed since the last render started.
    dirty: bool,
    /// [`PassdbPublisher::stop`] was called.
    stop: bool,
}

/// The half of the publisher that `AuthService` holds forever.
struct Shared {
    state: Mutex<State>,
    wake: Condvar,
    /// `Weak`, and it has to be. `AuthService` keeps the sink for the life of
    /// the process (`OnceLock`), the sink is this object, and the render
    /// target holds an `Arc<AuthService>`. An `Arc` here would close that
    /// loop and leak both for the life of the process, so the publisher
    /// borrows its target and stops when the target is gone.
    target: Weak<dyn PassdbRender>,
    window: Duration,
}

impl sc_auth::PassdbSink for Shared {
    /// Called from inside `sc-auth`'s write paths, sometimes with a database
    /// connection still open. Everything here is a flag and a notify: no
    /// database, no filesystem, no lock this crate holds elsewhere.
    fn republish(&self) {
        self.state.lock().dirty = true;
        self.wake.notify_one();
    }
}

/// Owns the publisher thread. `App` holds one, armed by `cmd_serve` and
/// stopped by the shutdown sequence.
pub struct PassdbPublisher {
    shared: Arc<Shared>,
    /// `Option` so [`Self::stop`] is idempotent: the shutdown sequence calls
    /// it, and `Drop` calls it again for the paths that never reach a clean
    /// shutdown. Joining twice would panic.
    join: Mutex<Option<std::thread::JoinHandle<()>>>,
}

impl PassdbPublisher {
    /// Starts the thread. Nothing is published until something marks the
    /// passdb dirty.
    pub fn start(target: Weak<dyn PassdbRender>) -> Self {
        Self::start_with_window(target, COALESCE_WINDOW)
    }

    fn start_with_window(target: Weak<dyn PassdbRender>, window: Duration) -> Self {
        let shared = Arc::new(Shared {
            state: Mutex::new(State::default()),
            wake: Condvar::new(),
            target,
            window,
        });
        // A plain OS thread, not a Tokio task, for the same reason
        // `app.rs::spawn_upload_gc` uses one: the render is blocking work
        // (SQLite reads and three file writes), and it must not occupy a
        // runtime worker for as long as it takes.
        let t = shared.clone();
        let join = std::thread::Builder::new()
            .name("sc-passdb".into())
            .spawn(move || run(&t))
            .expect("spawning the passdb publisher thread");
        Self {
            shared,
            join: Mutex::new(Some(join)),
        }
    }

    /// The object to hand `AuthService::set_passdb_sink`.
    pub fn sink(&self) -> Arc<dyn sc_auth::PassdbSink> {
        self.shared.clone()
    }

    /// Stops the thread, first letting it publish anything already marked.
    ///
    /// The flush is the point: a password change three milliseconds before
    /// `SIGTERM` would otherwise leave the old NT hash published until the
    /// next start, and the next start does not republish on its own either.
    /// Idempotent, and bounded by one render.
    pub fn stop(&self) {
        {
            let mut st = self.shared.state.lock();
            st.stop = true;
        }
        self.shared.wake.notify_all();
        if let Some(join) = self.join.lock().take() {
            let _ = join.join();
        }
    }
}

impl Drop for PassdbPublisher {
    fn drop(&mut self) {
        self.stop();
    }
}

fn run(shared: &Arc<Shared>) {
    loop {
        {
            let mut st = shared.state.lock();
            while !st.dirty && !st.stop {
                shared.wake.wait(&mut st);
            }
            if st.stop && !st.dirty {
                return;
            }
        }

        {
            let mut st = shared.state.lock();
            // Property 2: let the rest of the burst arrive. A stop request
            // cuts the wait short, so shutdown never pays this window.
            if !st.stop {
                shared
                    .wake
                    .wait_while_for(&mut st, |st| !st.stop, shared.window);
            }
            // Cleared *before* the render reads the database, never after. A
            // change that commits while the render is running has to leave
            // the flag set so it gets its own pass; clearing afterwards would
            // swallow it and publish a file that is one change behind with
            // nothing left to say so.
            st.dirty = false;
        }

        let Some(target) = shared.target.upgrade() else {
            // The server is being torn down. Not an error, and not worth a
            // render against half-dropped state.
            tracing::debug!("passdb publisher: render target dropped, stopping");
            return;
        };
        if let Err(e) = target.render_passdb() {
            // Property 3. Loud, because the consequence is a security one:
            // the file smbd reads no longer matches the database, and the
            // most likely reason is `validate_bind`'s LAN-only refusal, which
            // an operator has to act on.
            tracing::error!(
                error = %e,
                "publishing smbpasswd failed: the file smbd authenticates against is now stale \
                 and may still hold a credential the server has revoked. Fix the cause and run \
                 `sc-server smb-sync`; the next NT hash change will try again."
            );
        }
        drop(target);

        let st = shared.state.lock();
        if st.stop && !st.dirty {
            return;
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::sync::atomic::{AtomicU64, Ordering};

    /// Counts renders and can be told to fail. Stands in for
    /// `SettingsBridge`, which needs a data directory, four databases and a
    /// share registry to exist at all.
    struct FakeRender {
        calls: AtomicU64,
        fail: bool,
    }

    impl FakeRender {
        fn new(fail: bool) -> Arc<Self> {
            Arc::new(Self {
                calls: AtomicU64::new(0),
                fail,
            })
        }
        fn calls(&self) -> u64 {
            self.calls.load(Ordering::SeqCst)
        }
    }

    impl PassdbRender for FakeRender {
        fn render_passdb(&self) -> anyhow::Result<()> {
            self.calls.fetch_add(1, Ordering::SeqCst);
            if self.fail {
                anyhow::bail!("no such directory: /nonexistent/samba");
            }
            Ok(())
        }
    }

    /// Short enough to keep the tests quick, long enough that a burst issued
    /// in a tight loop lands inside one window on a loaded machine.
    const TEST_WINDOW: Duration = Duration::from_millis(150);

    fn wait_for(f: impl Fn() -> bool) -> bool {
        let deadline = std::time::Instant::now() + Duration::from_secs(5);
        while std::time::Instant::now() < deadline {
            if f() {
                return true;
            }
            std::thread::sleep(Duration::from_millis(5));
        }
        false
    }

    #[test]
    fn a_mark_publishes_without_anyone_asking() {
        let fake = FakeRender::new(false);
        let pub_ = PassdbPublisher::start_with_window(
            Arc::downgrade(&fake) as Weak<dyn PassdbRender>,
            TEST_WINDOW,
        );

        pub_.sink().republish();

        assert!(
            wait_for(|| fake.calls() == 1),
            "a marked passdb has to be published by the process itself, not by a later `smb-sync`"
        );
    }

    /// The `mark_dirty` half of §4.3.6 step 2: five changed accounts are one
    /// file, so they are one render.
    #[test]
    fn a_burst_collapses_into_one_render() {
        let fake = FakeRender::new(false);
        let pub_ = PassdbPublisher::start_with_window(
            Arc::downgrade(&fake) as Weak<dyn PassdbRender>,
            TEST_WINDOW,
        );

        let sink = pub_.sink();
        for _ in 0..10 {
            sink.republish();
        }

        assert!(wait_for(|| fake.calls() >= 1));
        // Past the window, and then some: anything the coalescing missed
        // would have rendered again by now.
        std::thread::sleep(TEST_WINDOW * 3);
        assert_eq!(fake.calls(), 1, "ten marks in one window are one file");
    }

    /// A change landing while a render is in flight must not be swallowed by
    /// the flag being cleared at the wrong end of the render.
    #[test]
    fn a_mark_during_a_render_gets_its_own_pass() {
        let fake = FakeRender::new(false);
        let pub_ = PassdbPublisher::start_with_window(
            Arc::downgrade(&fake) as Weak<dyn PassdbRender>,
            TEST_WINDOW,
        );

        let sink = pub_.sink();
        sink.republish();
        assert!(wait_for(|| fake.calls() == 1));
        sink.republish();
        assert!(
            wait_for(|| fake.calls() == 2),
            "the second change is a second file, not a repeat of the first"
        );
    }

    /// Property 3. The caller is long gone by the time this runs, so a failed
    /// render has nowhere to propagate to. What it must not do is take the
    /// thread with it and silently stop publishing everything after it.
    #[test]
    fn a_failed_render_is_contained_and_the_thread_survives_it() {
        let fake = FakeRender::new(true);
        let pub_ = PassdbPublisher::start_with_window(
            Arc::downgrade(&fake) as Weak<dyn PassdbRender>,
            TEST_WINDOW,
        );

        let sink = pub_.sink();
        sink.republish();
        assert!(wait_for(|| fake.calls() == 1));
        sink.republish();
        assert!(
            wait_for(|| fake.calls() == 2),
            "one failure must not stop the next change from being tried"
        );

        // And the handle still shuts down cleanly rather than hanging on a
        // thread that died inside the failure.
        pub_.stop();
    }

    /// A change marked at the last moment is still published, because nothing
    /// republishes at the *next* start either.
    #[test]
    fn stopping_flushes_a_pending_mark() {
        let fake = FakeRender::new(false);
        let pub_ = PassdbPublisher::start_with_window(
            Arc::downgrade(&fake) as Weak<dyn PassdbRender>,
            // Deliberately longer than the test is willing to wait: the flush
            // must come from the stop, not from the window expiring.
            Duration::from_secs(30),
        );

        pub_.sink().republish();
        pub_.stop();

        assert_eq!(fake.calls(), 1, "a mark at shutdown is still a stale file");
    }

    #[test]
    fn stopping_twice_is_a_no_op_rather_than_a_double_join() {
        let fake = FakeRender::new(false);
        let pub_ = PassdbPublisher::start_with_window(
            Arc::downgrade(&fake) as Weak<dyn PassdbRender>,
            TEST_WINDOW,
        );
        pub_.stop();
        pub_.stop();
        assert_eq!(fake.calls(), 0, "nothing was marked, so nothing was written");
    }

    /// The seam end to end, minus the render itself: a real `AuthService`, a
    /// real sink, and the two changes is about. Before
    /// this module existed both of these logged "no passdb sink installed"
    /// and stopped there.
    #[test]
    fn a_real_nt_hash_change_reaches_the_renderer_with_nobody_running_smb_sync() {
        let dir = tempfile::tempdir().unwrap();
        let auth = sc_auth::AuthService::new(
            &dir.path().join("auth.db"),
            sc_auth::AuthConfig {
                // The suite's cost, not production's: this test hashes three
                // passwords and cares about none of them.
                argon2_m_cost_kib: 8 * 1024,
                argon2_t_cost: 1,
                argon2_p_cost: 1,
                ..sc_auth::AuthConfig::default()
            },
            [9u8; 32],
        )
        .unwrap();

        let fake = FakeRender::new(false);
        let pub_ = PassdbPublisher::start_with_window(
            Arc::downgrade(&fake) as Weak<dyn PassdbRender>,
            TEST_WINDOW,
        );
        assert!(auth.set_passdb_sink(pub_.sink()));

        let uid = auth
            .create_user(
                "ingrid",
                &secrecy::SecretString::from("correct horse battery".to_string()),
            )
            .unwrap();

        auth.set_password(
            uid,
            &secrecy::SecretString::from("a different long password".to_string()),
        )
        .unwrap();
        assert!(
            wait_for(|| fake.calls() == 1),
            "a password change leaves the previous hash published until this runs"
        );

        auth.link_oidc_identity(uid, "https://idp.example.test", "sub-ingrid")
            .unwrap();
        assert!(
            wait_for(|| fake.calls() == 2),
            "linking deletes the row and has to take the published entry with it"
        );
        assert!(!auth.nt_hash_present(uid).unwrap());
    }

    /// The `Weak` in action: `AuthService` outliving the render target must
    /// not keep the thread spinning or panic it.
    #[test]
    fn a_dropped_render_target_stops_the_thread() {
        let fake = FakeRender::new(false);
        let weak = Arc::downgrade(&fake) as Weak<dyn PassdbRender>;
        let pub_ = PassdbPublisher::start_with_window(weak, TEST_WINDOW);
        let sink = pub_.sink();
        drop(fake);

        sink.republish();
        // The join is the assertion: the thread noticed the dead `Weak` and
        // returned instead of unwrapping it.
        pub_.stop();
    }
}
