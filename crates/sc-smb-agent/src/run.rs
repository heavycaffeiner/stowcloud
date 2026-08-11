use std::path::PathBuf;
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::Arc;
use std::time::Duration;

use clap::Parser;

use crate::control;
use crate::smbd::Mode;
use crate::sync::{self, Agent, Paths};

/// Long enough not to spin, short enough that a scope change nobody announced
/// is noticed while the operator is still looking at the screen. Not the main
/// path: `sc-core` pushes.
const POLL: Duration = Duration::from_secs(2);

#[derive(Parser, Debug)]
#[command(name = "sc-smb-agent", version, about = "Applies what sc-server renders for Samba")]
struct Cli {
    /// Where `sc-server` writes the rendered files.
    #[arg(long, env = "SC_SMB_CONFIG_DIR", default_value = "/config/smb")]
    config_dir: PathBuf,
    /// Where `sc-core` connects to ask for an apply.
    #[arg(long, env = "SC_SMB_SOCKET", default_value = sc_smb::agent::DEFAULT_SOCKET)]
    socket: PathBuf,
    #[arg(long, env = "SC_SMB_STATE_DIR", default_value = "/var/lib/sc-smb-agent")]
    state_dir: PathBuf,
    #[arg(long, env = "SC_SMB_CONF", default_value = "/etc/samba/smb.conf")]
    smb_conf: PathBuf,
    #[arg(long, env = "SC_SMB_PASSDB", default_value = "/var/lib/samba/private/passdb.tdb")]
    passdb: PathBuf,
    /// Own the smbd process rather than asking a service manager. The default
    /// is to use systemd or OpenRC when one is there, which is what a
    /// bare-metal install wants and what a container does not have.
    #[arg(long)]
    supervise: bool,
    /// Apply once and exit, printing the report as JSON. The operator's
    /// "apply now", and how the test harness drives this on a host with no
    /// service manager.
    #[arg(long)]
    once: bool,
}

pub fn main() -> std::process::ExitCode {
    let cli = Cli::parse();
    tracing_subscriber::fmt()
        .with_env_filter(
            tracing_subscriber::EnvFilter::try_from_env("SC_SMB_LOG")
                .unwrap_or_else(|_| tracing_subscriber::EnvFilter::new("info")),
        )
        .with_target(false)
        .init();

    // It edits /etc/passwd and the samba passdb, and binds :445 through smbd.
    // Saying so here beats a permission error three layers down.
    if unsafe { libc::geteuid() } != 0 {
        tracing::error!("sc-smb-agent must run as root");
        return std::process::ExitCode::from(1);
    }

    let paths = Paths {
        config_dir: cli.config_dir.clone(),
        state_dir: cli.state_dir,
        smb_conf: cli.smb_conf,
        passdb: cli.passdb,
        ..Paths::default()
    };
    if let Some(parent) = paths.passdb.parent() {
        let _ = std::fs::create_dir_all(parent);
    }
    let _ = std::fs::create_dir_all("/var/log/samba");

    let mode = if cli.supervise { Mode::Supervise } else { Mode::detect() };
    tracing::info!(?mode, config_dir = %cli.config_dir.display(), "starting");
    let agent = Arc::new(Agent::new(paths, mode.clone()));

    if cli.once {
        let report = agent.apply();
        println!("{}", serde_json::to_string_pretty(&report).unwrap_or_default());
        return if report.ok {
            std::process::ExitCode::SUCCESS
        } else {
            std::process::ExitCode::FAILURE
        };
    }

    // The first apply is what starts smbd, so it happens before the socket:
    // an "apply now" that arrives during startup would otherwise race it.
    let first = agent.apply();
    sync::log_report(&first, "startup");
    if matches!(mode, Mode::Supervise) {
        // After the first apply, because the jail's log path is the one
        // `conf::candidate` pointed smbd at and fail2ban treats a missing log
        // file as a hard configuration error for the whole daemon.
        let _ = std::fs::File::options()
            .create(true)
            .append(true)
            .open("/var/log/samba/log.smbd");
        agent.start_fail2ban();
    }

    {
        let agent = agent.clone();
        let socket = cli.socket.clone();
        let config_dir = cli.config_dir.clone();
        std::thread::Builder::new()
            .name("sc-smb-control".into())
            .spawn(move || {
                if let Err(e) = control::serve(&socket, agent, &config_dir) {
                    // Not fatal: the poll below still applies changes, which
                    // is exactly what this deployment had before the socket.
                    tracing::error!(
                        error = %e,
                        socket = %socket.display(),
                        "the control socket is not listening; changes will be picked up by the poll instead"
                    );
                }
            })
            .expect("spawning the control thread");
    }

    install_signal_handler();
    let mut last = agent.fingerprint();
    while !STOP.load(Ordering::Relaxed) {
        std::thread::sleep(POLL);
        let now = agent.fingerprint();
        if now != last {
            last = now;
            sync::log_report(&agent.apply(), "poll");
            continue;
        }
        // A supervised smbd that died takes the whole service with it, and
        // nothing else is watching.
        //
        // Guarded on the last apply having wanted smbd up. Without that, a
        // deployment with SMB turned off (no rendered config, so smbd is
        // deliberately stopped) reads as "smbd died" on every pass and tears
        // down again every two seconds, `pdbedit` and an `/etc/passwd`
        // rewrite included.
        let wanted_up = agent.last().smbd != sc_smb::agent::SmbdAction::Stopped;
        if matches!(mode, Mode::Supervise) && wanted_up && !agent.smbd_running() {
            tracing::warn!("smbd is not running; starting it again");
            sync::log_report(&agent.apply(), "poll");
        }
    }
    tracing::info!("stopping");
    std::process::ExitCode::SUCCESS
}

static STOP: AtomicBool = AtomicBool::new(false);

extern "C" fn on_signal(_: libc::c_int) {
    STOP.store(true, Ordering::Relaxed);
}

/// SIGTERM has to leave the loop rather than end the process, or a supervised
/// smbd outlives the agent that owns it (`Smbd`'s `Drop` is what stops it)
/// and the next start finds :445 taken.
fn install_signal_handler() {
    // SAFETY: the handler only stores into a static atomic, which is
    // async-signal-safe.
    unsafe {
        libc::signal(libc::SIGTERM, on_signal as libc::sighandler_t);
        libc::signal(libc::SIGINT, on_signal as libc::sighandler_t);
    }
}
