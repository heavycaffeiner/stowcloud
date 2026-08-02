import { redirect } from '@sveltejs/kit'

/**
 * `/settings/security` exists because the server sends people here.
 *
 * A link-mode OIDC callback lands on `/settings/security`, with
 * `?oidc_error=<code>` when it failed
 * (`docs/proposals/stowcloud-0-oidc-login.md` §5-1, and
 * `OidcLanding::Link` in `crates/sc-http/src/routes.rs`). That is a `Location`
 * header on a `302`, so it is a real URL a browser navigates to, not something
 * this app can route around. This screen's own address for the same thing is
 * `/settings#security` (the tab lives in the hash), and without this route the
 * SPA fallback would serve `index.html` for a path the router does not know
 * and the person would land on a 404 after a sign-in that actually worked.
 *
 * The query string is carried across unchanged: it is where the failure code
 * is, and `OidcSection` is what reads it.
 */
export function load({ url }: { url: URL }): never {
  redirect(307, `/settings${url.search}#security`)
}
