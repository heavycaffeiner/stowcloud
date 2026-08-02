//! WebSocket invalidation hub — `DESIGN-API.md` §7.
//!
//! ```text
//! C→S {"t":"sub","paths":[...]}      # only watched directories
//! C→S {"t":"unsub","paths":[...]}
//! C→S {"t":"ping"}
//! S→C {"t":"pong"}
//! S→C {"t":"inval","path":"/photos","etag":"..."}
//! S→C {"t":"job","id":"...","done":412,"total":1204}
//! S→C {"t":"quota","used":...,"limit":...}
//! S→C {"t":"revoked"}
//! ```
//!
//! Two invariants the design calls out explicitly:
//! * READ permission on a subscribed path is rechecked **both** at
//!   subscribe time and at send time — a session whose access was revoked
//!   mid-subscription must stop receiving events for that path.
//! * Events are **200ms debounced + coalesced per path** per connection —
//!   rapid repeated invalidations collapse into a single send carrying the
//!   latest etag.

use std::collections::{HashMap, HashSet};
use std::sync::atomic::{AtomicU64, Ordering};
use std::sync::Arc;

use sc_vfs::ids::UserId;
use parking_lot::Mutex;
use serde::{Deserialize, Serialize};
use tokio::sync::mpsc;
use tokio::time::Duration;

pub const DEBOUNCE: Duration = Duration::from_millis(200);

#[derive(Clone, Copy, PartialEq, Eq, Hash, Debug)]
pub struct ConnId(u64);

#[derive(Clone, Debug, Serialize, PartialEq)]
#[serde(tag = "t", rename_all = "snake_case")]
pub enum ServerMsg {
    Inval { path: String, etag: String },
    Job { id: String, done: u64, total: u64 },
    Quota { used: u64, limit: Option<u64> },
    Revoked,
    Pong,
}

#[derive(Clone, Debug, Deserialize)]
#[serde(tag = "t", rename_all = "snake_case")]
pub enum ClientMsg {
    Sub { paths: Vec<String> },
    Unsub { paths: Vec<String> },
    Ping,
}

/// READ-permission recheck hook. Implemented by whatever holds the ACL
/// engine — kept as a trait so the hub is independently testable with a
/// fake that can flip a path from allowed to denied mid-test.
pub trait ReadPermCheck: Send + Sync {
    fn can_read(&self, user: UserId, vpath: &str) -> bool;

    /// A path went from zero subscribers to one (across all connections) —
    /// the hook's one caller, `sc-server`'s `AclReadCheck`, uses it to
    /// register a live OS-level watch. Default no-op so the fakes in this
    /// module's own tests don't need to care.
    fn watch_subscribe(&self, _user: UserId, _vpath: &str) {}

    /// The mirror of `watch_subscribe`: a path went from one subscriber to
    /// zero, called once per (connection, path) that actually drops out —
    /// never on a redundant unsub/disconnect of a path that wasn't held.
    fn watch_unsubscribe(&self, _user: UserId, _vpath: &str) {}
}

struct ConnState {
    user: UserId,
    /// `token_hash_hex` of the cookie session that authenticated this socket,
    /// if any — `None` for Bearer/app-password connections. Lets
    /// `revoke_session` close just the one socket a specific session owns,
    /// as opposed to `revoke_user`'s whole-account grain.
    session_hash: Option<String>,
    subs: HashSet<String>,
    tx: mpsc::UnboundedSender<ServerMsg>,
}

pub struct WsHub {
    perm: Arc<dyn ReadPermCheck>,
    conns: Mutex<HashMap<ConnId, ConnState>>,
    next_id: AtomicU64,
    /// Latest etag seen per path, so a debounce task fires with the freshest
    /// value even if several `inval`s land inside the same window.
    latest_etag: Mutex<HashMap<String, String>>,
    /// `(conn, path)` pairs with a debounce task already scheduled — used to
    /// avoid spawning a new timer per event within the coalescing window.
    pending: Mutex<HashSet<(ConnId, String)>>,
}

impl WsHub {
    pub fn new(perm: Arc<dyn ReadPermCheck>) -> Arc<Self> {
        Arc::new(Self {
            perm,
            conns: Mutex::new(HashMap::new()),
            next_id: AtomicU64::new(1),
            latest_etag: Mutex::new(HashMap::new()),
            pending: Mutex::new(HashSet::new()),
        })
    }

    pub fn connect(self: &Arc<Self>, user: UserId) -> (ConnId, mpsc::UnboundedReceiver<ServerMsg>) {
        self.connect_with_session(user, None)
    }

    /// Same as `connect`, but records which session (if any) authenticated
    /// the socket, so `revoke_session` can target it later. `routes.rs`'s
    /// `events_ws` is the one real caller — it passes `Some(token_hash_hex)`
    /// for a cookie session and `None` for Bearer/app-password auth.
    pub fn connect_with_session(
        self: &Arc<Self>,
        user: UserId,
        session_hash: Option<String>,
    ) -> (ConnId, mpsc::UnboundedReceiver<ServerMsg>) {
        let id = ConnId(self.next_id.fetch_add(1, Ordering::Relaxed));
        let (tx, rx) = mpsc::unbounded_channel();
        self.conns.lock().insert(id, ConnState { user, session_hash, subs: HashSet::new(), tx });
        (id, rx)
    }

    pub fn disconnect(&self, id: ConnId) {
        if let Some(state) = self.conns.lock().remove(&id) {
            // Drop every OS-level watch this connection was the last
            // subscriber for — otherwise a client that disconnects without
            // sending `unsub` first would leak a sticky watch forever.
            for p in &state.subs {
                self.perm.watch_unsubscribe(state.user, p);
            }
        }
    }

    /// Force-disconnects every connection belonging to `user` with a
    /// `revoked` message (account-wide events only: logout, admin
    /// disable/delete — `routes.rs`'s
    /// `auth_logout`/`admin_patch_user`/`admin_delete_user`). Deliberately
    /// account-grain, not session-grain: in each of those three cases every
    /// session this account holds is meant to die (logout ends the only
    /// session that request can identify but treats a stale push channel on
    /// another tab as a hygiene issue, not a hole; disable/delete end all of
    /// them by definition). Revoking one *specific* session from the active-
    /// session list is a different case — see `revoke_session` below —
    /// because that UI only ever targets a session that isn't the caller's
    /// current one, so reusing this method there would wrongly kill the
    /// tab performing the revoke too.
    ///
    /// `DESIGN-API.md` §7: `{"t":"revoked"}` → immediate client logout.
    /// Sending it does not itself close the socket: the frontend hub reacts
    /// to `revoked` by closing its own connection
    /// (`web/src/lib/state/events.ts`'s `#onMessage`), which is what
    /// produces the `Message::Close`/error that ends `handle_socket`'s loop
    /// and calls `disconnect` server-side. A client that never got the
    /// memo — or isn't this frontend — still loses the socket the moment it
    /// tries anything: every subsequent event delivery and `Sub` still goes
    /// through `can_read`, which is unaffected by this call and keyed on the
    /// ACL, not on whether `revoke_user` was ever sent.
    pub fn revoke_user(&self, user: UserId) {
        let conns = self.conns.lock();
        for c in conns.values().filter(|c| c.user == user) {
            let _ = c.tx.send(ServerMsg::Revoked);
        }
    }

    /// Closes only the socket(s) authenticated by one specific session
    /// (`DELETE /api/auth/sessions/{id_hash}` → `auth_revoke_session`).
    /// `SessionsSection.svelte` only offers this action on a session that
    /// isn't `current` — the whole point of "individual" revocation (item
    /// 54) is that other live sessions, including the one the operator is
    /// using right now, are left alone. A connection with no recorded
    /// session hash (Bearer/app-password auth — `connect_with_session`'s
    /// `None` case) never matches and is untouched.
    pub fn revoke_session(&self, user: UserId, id_hash: &str) {
        let conns = self.conns.lock();
        for c in conns.values().filter(|c| c.user == user && c.session_hash.as_deref() == Some(id_hash)) {
            let _ = c.tx.send(ServerMsg::Revoked);
        }
    }

    pub fn handle_client_msg(&self, id: ConnId, msg: ClientMsg) {
        match msg {
            ClientMsg::Ping => {
                let conns = self.conns.lock();
                if let Some(c) = conns.get(&id) {
                    let _ = c.tx.send(ServerMsg::Pong);
                }
            }
            ClientMsg::Sub { paths } => {
                // Collect which paths are newly held by *this* connection
                // before calling out to `watch_subscribe` — sc_watch's own
                // sticky refcount (per share+path, across all connections)
                // is the source of truth for "is anyone watching this", so
                // the hook must fire exactly once per 0→1 transition here,
                // never on a repeat sub of a path this connection already
                // holds.
                let mut newly_watched = Vec::new();
                let user = {
                    let mut conns = self.conns.lock();
                    conns.get_mut(&id).map(|c| {
                        let user = c.user;
                        for p in paths {
                            // Subscribe-time permission recheck (§7 last bullet).
                            if self.perm.can_read(user, &p) && c.subs.insert(p.clone()) {
                                newly_watched.push(p);
                            }
                        }
                        user
                    })
                };
                if let Some(user) = user {
                    for p in &newly_watched {
                        self.perm.watch_subscribe(user, p);
                    }
                }
            }
            ClientMsg::Unsub { paths } => {
                let mut newly_unwatched = Vec::new();
                let user = {
                    let mut conns = self.conns.lock();
                    conns.get_mut(&id).map(|c| {
                        let user = c.user;
                        for p in paths {
                            if c.subs.remove(&p) {
                                newly_unwatched.push(p);
                            }
                        }
                        user
                    })
                };
                if let Some(user) = user {
                    for p in &newly_unwatched {
                        self.perm.watch_unsubscribe(user, p);
                    }
                }
            }
        }
    }

    /// A directory changed. Every connection subscribed to `path`, for whom
    /// READ is still granted, gets a debounced/coalesced `inval` after
    /// [`DEBOUNCE`].
    pub fn publish_inval(self: &Arc<Self>, path: &str, etag: &str) {
        self.latest_etag.lock().insert(path.to_string(), etag.to_string());

        let subscribed: Vec<ConnId> = {
            let conns = self.conns.lock();
            conns.iter().filter(|(_, c)| c.subs.contains(path)).map(|(id, _)| *id).collect()
        };

        for conn_id in subscribed {
            let key = (conn_id, path.to_string());
            let mut pending = self.pending.lock();
            if pending.contains(&key) {
                continue; // a timer is already in flight for this (conn, path)
            }
            pending.insert(key.clone());
            drop(pending);

            let hub = Arc::clone(self);
            let path = path.to_string();
            tokio::spawn(async move {
                tokio::time::sleep(DEBOUNCE).await;
                hub.pending.lock().remove(&(conn_id, path.clone()));

                let (user, tx) = {
                    let conns = hub.conns.lock();
                    match conns.get(&conn_id) {
                        Some(c) => (c.user, c.tx.clone()),
                        None => return, // disconnected during the debounce window
                    }
                };
                // Send-time permission recheck (§7 last bullet) — a
                // revocation that happened during the debounce window must
                // suppress the event.
                if !hub.perm.can_read(user, &path) {
                    return;
                }
                let etag = hub.latest_etag.lock().get(&path).cloned().unwrap_or_default();
                let _ = tx.send(ServerMsg::Inval { path, etag });
            });
        }
    }

    pub fn send_job(&self, id_str: ConnId, job_id: &str, done: u64, total: u64) {
        let conns = self.conns.lock();
        if let Some(c) = conns.get(&id_str) {
            let _ = c.tx.send(ServerMsg::Job { id: job_id.to_string(), done, total });
        }
    }

    /// Pushes job progress to every connection open for `user` (a job has
    /// one owner but may have zero or several live sockets — one per tab).
    /// `job_status`/`job_cancel` already enforce ownership on the poll path;
    /// this is the WS-push half of the same job (`DESIGN-API.md` §6/§7).
    pub fn send_job_to_user(&self, user: UserId, job_id: &str, done: u64, total: u64) {
        let conns = self.conns.lock();
        for c in conns.values().filter(|c| c.user == user) {
            let _ = c.tx.send(ServerMsg::Job { id: job_id.to_string(), done, total });
        }
    }

    pub fn send_quota(&self, id: ConnId, used: u64, limit: Option<u64>) {
        let conns = self.conns.lock();
        if let Some(c) = conns.get(&id) {
            let _ = c.tx.send(ServerMsg::Quota { used, limit });
        }
    }

    #[cfg(test)]
    pub fn subs_of(&self, id: ConnId) -> HashSet<String> {
        self.conns.lock().get(&id).map(|c| c.subs.clone()).unwrap_or_default()
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::sync::atomic::{AtomicBool, Ordering as O};

    struct AlwaysAllow;
    impl ReadPermCheck for AlwaysAllow {
        fn can_read(&self, _user: UserId, _vpath: &str) -> bool {
            true
        }
    }

    struct Flippable(Arc<AtomicBool>);
    impl ReadPermCheck for Flippable {
        fn can_read(&self, _user: UserId, _vpath: &str) -> bool {
            self.0.load(O::SeqCst)
        }
    }

    #[tokio::test(start_paused = true)]
    async fn subscribe_then_publish_delivers_event() {
        let hub = WsHub::new(Arc::new(AlwaysAllow));
        let (id, mut rx) = hub.connect(UserId::new(1));
        hub.handle_client_msg(id, ClientMsg::Sub { paths: vec!["/photos".into()] });
        hub.publish_inval("/photos", "etag-1");

        tokio::time::advance(DEBOUNCE + Duration::from_millis(10)).await;
        let msg = rx.recv().await.expect("event delivered");
        assert_eq!(msg, ServerMsg::Inval { path: "/photos".into(), etag: "etag-1".into() });
    }

    #[tokio::test(start_paused = true)]
    async fn revoked_permission_mid_subscription_stops_events() {
        let flag = Arc::new(AtomicBool::new(true));
        let hub = WsHub::new(Arc::new(Flippable(flag.clone())));
        let (id, mut rx) = hub.connect(UserId::new(1));
        hub.handle_client_msg(id, ClientMsg::Sub { paths: vec!["/photos".into()] });

        // Revoke before the debounce window elapses (simulates revocation
        // mid-subscription, before the send-time recheck runs).
        hub.publish_inval("/photos", "etag-1");
        flag.store(false, O::SeqCst);
        tokio::time::advance(DEBOUNCE + Duration::from_millis(10)).await;

        // Give the spawned task a chance to run to completion.
        tokio::task::yield_now().await;
        assert!(rx.try_recv().is_err(), "no event should be delivered once READ is revoked");
    }

    #[tokio::test(start_paused = true)]
    async fn denied_at_subscribe_time_never_subscribes() {
        let hub = WsHub::new(Arc::new(Flippable(Arc::new(AtomicBool::new(false)))));
        let (id, _rx) = hub.connect(UserId::new(1));
        hub.handle_client_msg(id, ClientMsg::Sub { paths: vec!["/secret".into()] });
        assert!(hub.subs_of(id).is_empty());
    }

    #[tokio::test(start_paused = true)]
    async fn coalesces_rapid_repeat_invalidations() {
        let hub = WsHub::new(Arc::new(AlwaysAllow));
        let (id, mut rx) = hub.connect(UserId::new(1));
        hub.handle_client_msg(id, ClientMsg::Sub { paths: vec!["/photos".into()] });

        hub.publish_inval("/photos", "etag-1");
        tokio::time::advance(Duration::from_millis(50)).await;
        hub.publish_inval("/photos", "etag-2");
        tokio::time::advance(Duration::from_millis(50)).await;
        hub.publish_inval("/photos", "etag-3");

        tokio::time::advance(DEBOUNCE + Duration::from_millis(10)).await;
        let msg = rx.recv().await.expect("one coalesced event");
        assert_eq!(msg, ServerMsg::Inval { path: "/photos".into(), etag: "etag-3".into() });
        assert!(rx.try_recv().is_err(), "only one event should have been sent");
    }

    #[tokio::test]
    async fn send_job_to_user_reaches_every_connection_of_that_user_and_no_other() {
        let hub = WsHub::new(Arc::new(AlwaysAllow));
        let (_id_a1, mut rx_a1) = hub.connect(UserId::new(1));
        let (_id_a2, mut rx_a2) = hub.connect(UserId::new(1)); // same user, second tab
        let (_id_b, mut rx_b) = hub.connect(UserId::new(2));

        hub.send_job_to_user(UserId::new(1), "J-1", 3, 10);

        assert_eq!(rx_a1.try_recv().unwrap(), ServerMsg::Job { id: "J-1".into(), done: 3, total: 10 });
        assert_eq!(rx_a2.try_recv().unwrap(), ServerMsg::Job { id: "J-1".into(), done: 3, total: 10 });
        assert!(rx_b.try_recv().is_err(), "a different user's connection must not see another account's job progress");
    }

    #[tokio::test]
    async fn revoke_user_closes_every_connection_of_that_user_and_no_other() {
        let hub = WsHub::new(Arc::new(AlwaysAllow));
        let (_id_a1, mut rx_a1) = hub.connect(UserId::new(1));
        let (_id_a2, mut rx_a2) = hub.connect(UserId::new(1)); // same user, second tab
        let (_id_b, mut rx_b) = hub.connect(UserId::new(2));

        hub.revoke_user(UserId::new(1));

        assert_eq!(rx_a1.try_recv().unwrap(), ServerMsg::Revoked);
        assert_eq!(rx_a2.try_recv().unwrap(), ServerMsg::Revoked);
        assert!(rx_b.try_recv().is_err(), "revoking one user's sockets must not touch another user's");
    }

    #[tokio::test]
    async fn revoke_session_closes_only_the_matching_session_socket() {
        let hub = WsHub::new(Arc::new(AlwaysAllow));
        let (_id_a1, mut rx_a1) = hub.connect_with_session(UserId::new(1), Some("hash-a".into()));
        let (_id_a2, mut rx_a2) = hub.connect_with_session(UserId::new(1), Some("hash-b".into())); // same user, different session
        let (_id_a3, mut rx_a3) = hub.connect(UserId::new(1)); // app-password socket, no session hash
        let (_id_b, mut rx_b) = hub.connect_with_session(UserId::new(2), Some("hash-a".into())); // different user, same hash by coincidence

        hub.revoke_session(UserId::new(1), "hash-a");

        assert_eq!(rx_a1.try_recv().unwrap(), ServerMsg::Revoked);
        assert!(rx_a2.try_recv().is_err(), "a different session of the same user must be left alone");
        assert!(rx_a3.try_recv().is_err(), "a connection with no session hash must never match");
        assert!(rx_b.try_recv().is_err(), "a different user's connection must not be touched even with the same hash");
    }

    /// Records every `watch_subscribe`/`watch_unsubscribe` call — stands in
    /// for `sc-server`'s `AclReadCheck`, which forwards these into a real
    /// `sc_watch::Watcher`.
    #[derive(Default)]
    struct RecordingWatch {
        subscribed: Mutex<Vec<String>>,
        unsubscribed: Mutex<Vec<String>>,
    }
    impl ReadPermCheck for RecordingWatch {
        fn can_read(&self, _user: UserId, _vpath: &str) -> bool {
            true
        }
        fn watch_subscribe(&self, _user: UserId, vpath: &str) {
            self.subscribed.lock().push(vpath.to_string());
        }
        fn watch_unsubscribe(&self, _user: UserId, vpath: &str) {
            self.unsubscribed.lock().push(vpath.to_string());
        }
    }

    #[tokio::test]
    async fn sub_calls_watch_subscribe_exactly_once_per_path_even_if_repeated() {
        let watch = Arc::new(RecordingWatch::default());
        let hub = WsHub::new(watch.clone());
        let (id, _rx) = hub.connect(UserId::new(1));

        hub.handle_client_msg(id, ClientMsg::Sub { paths: vec!["/photos".into()] });
        // A repeat sub of the same path from the same connection must not
        // register the OS watch a second time.
        hub.handle_client_msg(id, ClientMsg::Sub { paths: vec!["/photos".into()] });

        assert_eq!(*watch.subscribed.lock(), vec!["/photos".to_string()]);
        assert!(watch.unsubscribed.lock().is_empty());
    }

    #[tokio::test]
    async fn unsub_calls_watch_unsubscribe_once() {
        let watch = Arc::new(RecordingWatch::default());
        let hub = WsHub::new(watch.clone());
        let (id, _rx) = hub.connect(UserId::new(1));
        hub.handle_client_msg(id, ClientMsg::Sub { paths: vec!["/photos".into()] });

        hub.handle_client_msg(id, ClientMsg::Unsub { paths: vec!["/photos".into()] });
        // Unsubscribing a path this connection no longer holds is a no-op.
        hub.handle_client_msg(id, ClientMsg::Unsub { paths: vec!["/photos".into()] });

        assert_eq!(*watch.unsubscribed.lock(), vec!["/photos".to_string()]);
    }

    #[tokio::test]
    async fn disconnect_calls_watch_unsubscribe_for_every_remaining_subscription() {
        let watch = Arc::new(RecordingWatch::default());
        let hub = WsHub::new(watch.clone());
        let (id, _rx) = hub.connect(UserId::new(1));
        hub.handle_client_msg(id, ClientMsg::Sub { paths: vec!["/photos".into(), "/docs".into()] });

        hub.disconnect(id);

        let mut got = watch.unsubscribed.lock().clone();
        got.sort();
        assert_eq!(got, vec!["/docs".to_string(), "/photos".to_string()]);
    }
}
