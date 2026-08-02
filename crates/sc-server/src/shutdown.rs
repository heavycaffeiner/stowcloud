//! Graceful shutdown sequence: stop accepting new
//! connections, drain in-flight uploads, flush dirty aggregates, checkpoint
//! the WAL, then exit. Uploads are resumable and the DB is a rebuildable
//! cache, so a hard kill never loses data — a clean shutdown just makes the
//! next resume point exact.

/// Resolves once SIGTERM (Unix) or Ctrl-C (any platform, incl. the mandated
/// Windows dev box) is received.
pub async fn wait_for_shutdown_signal() {
    #[cfg(unix)]
    {
        use tokio::signal::unix::{signal, SignalKind};
        let ctrl_c = tokio::signal::ctrl_c();
        match signal(SignalKind::terminate()) {
            Ok(mut term) => {
                tokio::select! {
                    _ = ctrl_c => {}
                    _ = term.recv() => {}
                }
            }
            Err(_) => {
                let _ = ctrl_c.await;
            }
        }
    }
    #[cfg(not(unix))]
    {
        let _ = tokio::signal::ctrl_c().await;
    }
}

#[derive(Debug, Clone, Copy, Default, PartialEq, Eq)]
pub struct ShutdownSteps {
    pub stopped_accepting: bool,
    /// In-flight upload sessions swept to a clean resume point.
    pub uploads_drained: bool,
    /// Dirty directory aggregates recomputed and written back
    /// (`ARCHITECTURE.md` §4.2).
    pub aggregates_flushed: bool,
    /// SQLite WAL folded back into the main database file.
    pub wal_checkpointed: bool,
}

/// The sequence, in order.
///
/// None of these steps protect data: uploads are resumable, and the metadata
/// DB is a rebuildable cache whose only source of truth is the filesystem
/// (`ARCHITECTURE.md` §0.1). A `kill -9` therefore loses nothing. What a
/// clean shutdown buys is that the next start does not have to re-derive any
/// of it — an exact resume offset, a warm aggregate cache, and a WAL that
/// does not need replaying.
///
/// Every step is best-effort and independently reported: a failure here must
/// not turn a successful shutdown into a non-zero exit.
pub fn run_shutdown_sequence(app: &crate::app::App) -> ShutdownSteps {
    tracing::info!("shutdown: no longer accepting new connections");
    let mut steps = ShutdownSteps {
        stopped_accepting: true,
        ..Default::default()
    };

    // Stop the periodic upload-GC sweep (`app.rs::spawn_upload_gc`) *before*
    // draining: both ultimately call `UploadEngine::gc()`, so leaving the
    // background thread running here would let it fire mid-shutdown, racing
    // the drain below on the same DB/part-file state for no benefit -- the
    // drain about to run is a strictly more current sweep.
    app.upload_gc.stop();
    tracing::info!("shutdown: periodic upload gc stopped");

    // Same reasoning: an in-flight merge holds the index's write lock
    // (`sc_search::index::NameIndex::merge`), so leaving this running past
    // shutdown only risks a slower exit, never lost data — but there is no
    // reason to pay even that.
    app.idle_merge.stop();
    tracing::info!("shutdown: idle name index merge stopped");

    // Not just stopped: `PassdbPublisher::stop` publishes anything still
    // marked before it joins. A credential revoked seconds before `SIGTERM`
    // would otherwise stay live in the published `smbpasswd`, because nothing
    // republishes at the next start either.
    if let Some(passdb) = app.passdb.get() {
        passdb.stop();
        tracing::info!("shutdown: passdb publisher flushed and stopped");
    }

    let drained = sc_http::upload_api::UploadApi::drain(app.http.uploads.as_ref());
    tracing::info!(sessions = drained, "shutdown: upload sessions drained");
    steps.uploads_drained = true;

    // Recomputing a dirty aggregate is what writes it back: `Core::aggregate`
    // is cache-through, so asking for each share root's value is the flush.
    let mut flushed = 0usize;
    for def in app.core.share_defs() {
        match app.core.aggregate(def.id, &sc_vfs::SafePath::root()) {
            Ok(_) => flushed += 1,
            Err(e) => tracing::warn!(share = %def.name, error = %e, "aggregate flush failed"),
        }
    }
    tracing::info!(shares = flushed, "shutdown: dirty aggregates flushed");
    steps.aggregates_flushed = true;

    match app.meta.wal_checkpoint() {
        Ok(()) => {
            tracing::info!("shutdown: WAL checkpointed");
            steps.wal_checkpointed = true;
        }
        Err(e) => tracing::warn!(error = %e, "shutdown: WAL checkpoint failed"),
    }

    steps
}
