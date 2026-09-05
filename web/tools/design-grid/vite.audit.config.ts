// The app's dev config with the API
// proxy removed, used only by the runtime audit.
//
// vite.config.ts proxies `^/api`, `^/dav`, `^/c/`, `^/s/`, `^/ocs`,
// `^/remote.php`, `^/index.php` and `^/status.php` to a separately-running
// the server, so `vite dev` can be pointed at a real backend. The audit runs
// against the in-memory mock (VITE_API_MOCK=1), which answers all of those in
// the browser, so every one of those hops is dead weight -- and `^/s/`
// actively breaks the audit: a document request for a share link is proxied
// away and the SPA never gets to render it.
//
// Nothing else is changed. Same plugins, same CSS pipeline, same mode, so the
// geometry measured is the geometry `npm run dev` produces.
import base from '../../vite.config'

export default {
  ...base,
  server: { ...base.server, proxy: undefined }
}
