# Frontend UX Audit and Material You Redesign Specification

## 1. Overview

This document presents a comprehensive audit of the Stowcloud web frontend user experience and defines the target specification for aligning its interface with Google Drive layout patterns and Material You (Material Design 3) design principles.

The audit evaluates every route, UI component, state store, and stylesheet under `web/src/`. It addresses layout fragmentation, the unintuitive tree navigation model, and the disorganized settings architecture while preserving all existing system capabilities and settings controls.

## 2. Current Architecture Survey

### 2.1 App Shell and Navigation Structure

The authenticated shell is defined in `web/src/routes/(app)/+layout.svelte`. It uses responsive branching driven by `uiState.compact` (`window.innerWidth < 905px`):

1. Desktop (905px and wider):
   - `NavigationRail.svelte`: A fixed 96px width vertical rail on the far left. It displays navigation items: Files, Recent, Trash, Admin (for administrators), and Settings.
   - `NavigationDrawer.svelte`: When "Files" is active, a 280px drawer docks immediately to the right of the rail. Main content padding is adjusted to 376px (`96px + 280px`).
   - `FileTree.svelte`: Inside `web/src/routes/(app)/b/[...path]/+page.svelte`, a collapsible folder tree side panel occupies an additional 240px.
   - `DetailsPanel.svelte`: When toggled open, an inspector panel takes 320px on the right edge.
   - Resulting layout friction: On a standard desktop viewport, sidebars alone can consume up to 936px (`96px + 280px + 240px + 320px`), leaving minimal space for file content.
2. Compact Viewports (under 905px):
   - `NavigationBar.svelte`: A fixed 64px bottom bar with items for primary navigation.
   - Modal drawer overlay for root switching.
   - Modal dialog for folder tree.
   - Bottom sheet for details panel.
   - Resulting layout friction: Users experience modal layering where selecting a folder or switching roots repeatedly opens and closes full-screen dialogs.

### 2.2 Tree View Architecture and Usability Flaws

The folder tree currently suffers from several structural and interaction design flaws:

1. Structural Disconnection:
   - The user shares (roots) are rendered inside `NavigationDrawer.svelte` as a flat, non-hierarchical list.
   - Subfolder hierarchies are completely separate, locked inside `FileTree.svelte` within the file browser page.
   - To inspect a nested folder structure, users must first pick a root in the left drawer, then find and click the "folder-tree" toggle button in the toolbar, opening yet another 240px side panel.
2. Interaction and Visual Flaws in `FileTreeItem.svelte`:
   - Tight row height: Rows are pinned at 32px (`height: 32px`), which is cramped and falls below the standard 40px/48px touch and mouse target baseline.
   - Tiny twisty targets: The collapse/expand twisty is a 20px box with no state layer, making it difficult to target reliably.
   - Static icon representation: The folder glyph remains a static closed folder icon whether the item is expanded, collapsed, active, or idle. In Google Drive, active or expanded folders switch to open folder iconography.
   - Lack of hierarchy guidelines: Nested child lists render without visual guide lines or branch connectors. In deep hierarchies, users cannot discern which ancestor a subfolder belongs to.
   - Faint selection feedback: Active items only gain a subtle background tint (`var(--m3c-secondary-container)`), lacking the distinct full-pill highlight used by Google Drive and Material You.
   - No auto-synchronization: Navigating through the file table or breadcrumb does not auto-scroll or expand the tree view to the current location.

### 2.3 Settings Structure and Usability Flaws

The current personal settings page (`web/src/routes/(app)/settings/+page.svelte`) lacks logical grouping, visual hierarchy, and intuitive navigation:

1. Disjointed Categorization:
   - Account tab: Displays user name and Sign out button, followed immediately by Password change. Password is an authentication credential, not a profile attribute.
   - Security tab: Bundles Two-Factor Authentication (TOTP), Single Sign-On (OIDC), App Passwords, and Active Sessions into a single vertical scroll.
   - Connections tab: Holds only the SMB configuration. If the server does not have SMB enabled, the tab disappears, leaving SMB settings with no clear home.
   - Appearance tab: Isolates Theme and Language into a separate tab with empty surrounding space.
2. Visual and Layout Flaws:
   - Flat forms and thin dividers: Settings items render as plain uncontained form fields separated only by 1px horizontal `<Divider />` lines. There are no container cards or elevation boundaries.
   - Missing status indicators: Critical security states (such as whether 2FA is active, SSO is linked, or how many active sessions exist) are not summarized at a glance.
   - Unresponsive desktop presentation: The horizontal radio tabs float across the top of an otherwise empty 100vh canvas, while the content is crammed into a centered 640px column.

### 2.4 Top Header and Global Search

1. Current State:
   - There is no persistent global top app bar in `(app)/+layout.svelte`.
   - Each route renders its own independent header.
   - In the file browser (`/b/[...path]`), search is an icon button in the right toolbar. Clicking it opens an inline `TextField` that compresses or wraps the breadcrumb path.
   - Search results appear in an absolute dropdown list below the toolbar.
   - Other routes (`/recent`, `/trash`, `/settings`, `/admin`) render standalone heading text with no search access.
   - Theme toggle, profile info, and storage quota are unavailable in the main viewing chrome.
2. Comparison with Google Drive:
   - Google Drive provides a single, prominent, centered search bar with rounded pill geometry (`border-radius: 9999px` or `28px`).
   - Search remains persistently available across all views.
   - Top right controls provide direct access to theme switching, settings, and user profile management.

### 2.5 Creation and Upload Flow ("+ New")

1. Current State:
   - On desktop, three text and tonal buttons sit in the right side of the browse toolbar: "Upload folder", "Upload", and "New folder".
   - On viewports below 905px, only a floating action button (FAB) for file upload is displayed; folder creation and folder uploads are hidden in an overflow menu.
   - The three desktop buttons occupy 320px of toolbar width, causing aggressive toolbar wrapping on medium screens.
2. Comparison with Google Drive:
   - Google Drive uses a single "+ New" button at the top of the left navigation drawer.
   - The button uses an Extended FAB style with subtle elevation and rounded corners (`16px`).
   - Clicking "+ New" opens a consolidated menu with options for New folder, File upload, and Folder upload.

### 2.6 File Table and Grid Views

1. FileTable (List View):
   - Located in `web/src/lib/ui/FileTable.svelte`.
   - Missing table header row: Users cannot view column names (Name, Last modified, File size) and cannot click column headers to sort. Sorting is accessible only through a toolbar dropdown menu.
   - Rows (`web/src/lib/ui/FileRow.svelte`) use rigid border-bottom lines with limited hover feedback.
2. FileGrid (Card View):
   - Located in `web/src/lib/ui/FileGrid.svelte`.
   - Divides folders and files into two distinct virtualized window sections.
   - Cards use `border: 1px solid var(--m3c-outline-variant)` and `border-radius: var(--m3-shape-medium)` (12px), but lack elevation and contrast.

## 3. Redesign Blueprint

### 3.1 Intuitive Integrated Tree View Specification

The folder tree is consolidated directly into the left navigation drawer rather than living as a secondary panel inside the browse page:

```
+-------------------------------------------------------------+
| Left Sidebar (256px)                                        |
+-------------------------------------------------------------+
| [+ New                      v]                              |
|                                                             |
| v [Folder] My Files                                         |
|   v [Folder] Projects                                       |
|     [Folder] 2026-Q3                                        |
|     [Folder] Marketing                                      |
|   > [Folder] Documents                                      |
|   > [Folder] Personal                                       |
|                                                             |
| [History] Recent                                            |
| [Trash]   Trash                                             |
| [Admin]   Administration                                    |
| [Gear]    Settings                                          |
|                                                             |
| Storage: 12.4 GB of 50 GB used                              |
+-------------------------------------------------------------+
```

Component Specifications:
1. Row Geometry:
   - Row height: 40px (conforming to the 4px grid scale: 40px touch and click target).
   - Border radius: Full pill shape (`border-radius: var(--m3-shape-full)`), matching Google Drive left navigation items.
   - Indentation: `depth * 16px + 8px`, cleanly separating tree depth levels.
2. State and Visual Hierarchy:
   - Twisty icon: 24px clickable box with hover ripple layer. Uses `chevron-right` with a 90-degree smooth CSS transition on expansion.
   - Folder glyph: `icons.folder` (`folder-outline`) styled with primary color tint (`var(--m3c-primary)`) when active or expanded.
   - Active route: Bold font weight (`600`), primary text color (`var(--m3c-on-secondary-container)`), and solid background pill (`var(--m3c-secondary-container)`).
3. Unified Navigation Behavior:
   - Clicking the label navigates to `/b/[path]` and expands the subfolder if collapsed.
   - Clicking the twisty toggles expansion without navigation.
   - Subfolder counts: Empty directories show a subtle muted indicator without blocking layout.
   - Ancestor auto-expansion: Opening any nested directory automatically expands all ancestor folders in the tree.

### 3.2 Redesigned Settings Architecture (100% Item Retention)

The settings view is reorganized into an intuitive card-based dashboard with Material 3 Container Cards. Every existing configuration item, form input, button, and dynamic import is preserved:

#### Category Grouping:

1. Appearance (`appearance` tab):
   - Theme Card (화면 테마):
     - ConnectedButtons for Theme selection: System (`system`), Light (`light`), Dark (`dark`).
     - Explanatory note: Device setting synchronization.
   - Language Card (언어 설정):
     - ConnectedButtons for Language selection: Korean (`한국어`), English (`English`).
     - Explanatory note: Browser-scoped locale storage.

2. Profile and Account (`account` tab):
   - User Profile Card:
     - Avatar circle with user initial and role badge (`common.administrator` vs `common.user_2`).
     - Display name and system username (`@{user.name}`).
     - Sign Out button (`Button variant="outlined"`).
   - Password Management Card:
     - Password update form via `PasswordSection.svelte`.
     - Placed in the Account tab alongside the profile card to preserve the per-tab `minElements: 30` layout invariant and group user credential lifecycle management.
     - Inputs: Current password, new password, confirm password, and strength indicator.

3. Security and Authentication (`security` tab):
   - Two-Factor Authentication Card (2단계 인증):
     - TOTP management via `TotpSection.svelte`.
     - QR code setup, 6-digit code verification, and recovery code view.
   - Single Sign-On Card (통합 로그인 / SSO):
     - OIDC provider connection via `OidcSection.svelte`.
     - Status: Linked identity provider, subject hint, and link/unlink action buttons.
   - App Passwords Card (앱 비밀번호):
     - WebDAV and external client credentials via `AppPasswordsSection.svelte`.
     - Token generator, expiry controls, and active app token revocation list.
   - Active Sessions Card (로그인된 세션):
     - Active session list via `SessionsSection.svelte`.
     - Current session badge, client user-agent strings, IP addresses, and sign out all other devices.
4. Network Drive and Services (네트워크 드라이브 / SMB) (Conditional on server SMB feature):
   - SMB Credentials Card:
     - Dedicated SMB password and access mode via `SmbSection.svelte`.
     - Opt-out toggle, credentials setup, and permission status notices.

#### Visual Card Design (Material 3 Surface Cards):

Every setting card follows M3 elevated container principles:
- Background: `var(--m3c-surface-container-low)`.
- Border: `1px solid var(--m3c-outline-variant)`.
- Border radius: `var(--m3-shape-large)` (16px).
- Internal padding: 24px (`--sc-card-pad: 24px`, conforming to 4px grid).
- Card Header:
  - Leading icon: 24px icon inside a 40px rounded container circle (`background: var(--m3c-surface-container-highest)`).
  - Title: Set with `--m3-title-medium` (Google Sans Flex).
  - Subtitle / Description: Set with `--m3-body-small` (`var(--m3c-on-surface-variant)`).
  - The Account card features an M3 tonal pill badge displaying the user role (`common.administrator` or `common.user_2`).

#### Layout Presentation:

- Sticky M3 Tab Bar:
  - Sticky at top: `.sc-settings__head` pins the M3 `Tabs` bar with icon and text labels for each section.
  - Centered reading column: `.sc-settings__inner` displays the vertical stack of container cards for the active tab.
- Full preservation of `#account`, `#security`, `#connections`, `#appearance` URL hash synchronization via `syncTabHash`.

#### Architectural Constraints and Lazy Boundary Invariants:

1. Per-Tab Dynamic Import Boundary:
   - `AppPasswordsSection` and `SessionsSection` fetch live server state in `onMount`.
   - `TotpSection` checks recovery code status in an active `$effect`.
   - Flattening all sections into a single un-tabbed vertical scroll would execute these API requests every time any user visits Settings simply to adjust appearance or language.
   - All subcomponent modules must remain partitioned behind `#await import(...)` boundaries so that unvisited tabs incur zero network traffic and zero execution overhead.

2. Deep Link and Fallback Contract:
   - The `#hash` routing contract using `TAB_VALUES = ['account', 'security', 'connections', 'appearance']` and `syncTabHash` must be maintained without regression.
   - Deep links (such as `/settings#security`) must load the specified tab immediately upon entry.
   - The SMB fallback rule (`if (tab === 'connections' && features && !features.smb) tab = 'account'`) must remain active, ensuring users on deployments without SMB are redirected safely rather than encountering an empty view.

### 3.3 Top App Bar and Global Search

1. Bar Geometry:
   - Height: 64px (`--sc-top-bar-height`).
   - Background: `var(--m3c-surface)`.
   - Border bottom: `1px solid var(--m3c-outline-variant)`.
2. Left Brand Area:
   - Hamburger icon button (`menu`) to collapse or expand the sidebar on desktop, or open drawer modal on mobile.
   - Product glyph and wordmark: "Stowcloud" set in Google Sans Flex medium.
3. Centered Pill Search:
   - Box height: 48px, max-width: 720px.
   - Shape: `border-radius: 9999px` (pill).
   - Background: `var(--m3c-surface-container-high)`.
   - Icon: `search` (20px).
   - Autocomplete dropdown card: Elevation 3 shadow, rounded corners (16px), showing instant file and folder results.
4. Right System Controls:
   - Direct theme toggle button (Sun/Moon icon).
   - Settings shortcut button.
   - User profile button with avatar circle.

### 3.4 FileTable and FileGrid Enhancements

1. FileTable (List View):
   - Sticky table header row at `top: 0`, z-index 2.
   - Header columns: Checkbox (40px), Name (flex 1) with clickable sort arrow, Last modified (176px), File size (112px), Actions (48px).
   - Rows: 48px comfortable row height, 8px rounded corners on hover, inline action icons (Download, Share, More) revealed on hover.
2. FileGrid (Card View):
   - Folder cards: Compact 48px horizontal pills with primary-colored folder icons.
   - File cards: 16px rounded corners, prominent thumbnail container, clear typography hierarchy.

3. DetailsPanel Data Grounding:
   - The inspector panel strictly renders properties backed by the real `Entry` shape (`name`, `kind`, `size`, `mtime_ns`, `perms`, `link`, `preview`, `confusable`) and measured recursive size via `/files/size`.
   - It avoids unbacked synthetic attributes (such as per-file activity logs, artificial owner fields, or nonexistent per-file ACL graphs) that have no real backend implementation.

### 3.5 Material You Color Tokens (Google Drive Tone)

```css
@layer tokens {
  :root {
    /* Primary brand palette (Google Drive Blue tone) */
    --m3c-primary: light-dark(#0b57d0, #a8c7fa);
    --m3c-on-primary: light-dark(#ffffff, #062e6f);
    --m3c-primary-container: light-dark(#d3e3fd, #0842a0);
    --m3c-on-primary-container: light-dark(#041e49, #d3e3fd);

    /* Secondary palette */
    --m3c-secondary: light-dark(#00639b, #7fcfff);
    --m3c-on-secondary: light-dark(#ffffff, #003355);
    --m3c-secondary-container: light-dark(#c2e7ff, #004a75);
    --m3c-on-secondary-container: light-dark(#001d32, #c2e7ff);

    /* Surface tonal hierarchy */
    --m3c-surface: light-dark(#f8fafd, #111318);
    --m3c-surface-dim: light-dark(#d8dadf, #111318);
    --m3c-surface-bright: light-dark(#f8fafd, #37393e);
    --m3c-surface-container-lowest: light-dark(#ffffff, #0c0e13);
    --m3c-surface-container-low: light-dark(#f3f4f9, #191c20);
    --m3c-surface-container: light-dark(#edeeF3, #1d2024);
    --m3c-surface-container-high: light-dark(#e7e8ed, #282a2f);
    --m3c-surface-container-highest: light-dark(#e1e2e7, #33353a);

    --m3c-on-surface: light-dark(#191c20, #e1e2e7);
    --m3c-on-surface-variant: light-dark(#44474e, #c4c6cf);
    --m3c-outline: light-dark(#74777f, #8e9199);
    --m3c-outline-variant: light-dark(#c4c6cf, #44474e);
  }
}
```

### 3.6 Mobile UX Enhancements

Mobile interactions across viewports below 905px and below 600px are refined to match Material You touch patterns:

1. Responsive Settings Cards:
   - Below 600px (`@media (max-width: 599.98px)`), card internal padding relaxes from 24px to 16px, recovering 16px of horizontal space for form inputs and action rows on narrow 360px viewports.
   - Flex gap relaxes to 16px, and card header elements wrap gracefully (`gap: 12px; flex-wrap: wrap`) to prevent text truncation against trailing role badges.

2. Tree View Touch Geometry:
   - Node heights are increased to 40px with touch-target expansions, replacing the previous 32px targets.
   - The twisty expand icon provides a 24px circular hit target with touch feedback.
   - Active folder ancestors auto-expand on load, removing the friction of manual multi-level tree traversal on mobile dialogs.

3. Bottom Navigation and Overlays:
   - `NavigationBar` pins to the bottom at 64px height with safe-area insets.
   - Selection actions and floating trays calculate dynamic offsets to clear the navigation bar and prevent overlap.
   - Secondary metadata (such as modification timestamps) in file rows drops under 600px to prioritize filename legibility on phone displays, while maintaining 48px checkbox hit targets.

## 4. Verification and Compliance Standards

1. 4px Design Grid:
   - Every layout property (margin, padding, width, height, gap) must align with the approved grid scale: `0, 4, 8, 12, 16, 24, 32, 48, 64`.
   - Radius values must map strictly to approved M3 shape tokens (`--m3-shape-small: 8px`, `--m3-shape-medium: 12px`, `--m3-shape-large: 16px`, `--m3-shape-full: 9999px`).
2. Zero Feature Regression:
   - All existing authentication and session bootstrap flows must function identically.
   - All subcomponents in Settings (`PasswordSection`, `TotpSection`, `OidcSection`, `AppPasswordsSection`, `SessionsSection`, `SmbSection`) must retain their existing event handlers and state triggers.
   - Per-tab dynamic `import(...)` boundaries must be preserved to prevent premature network requests on page load.
   - The `#hash` deep-link contract (`TAB_VALUES`, `syncTabHash`, and the connections/SMB fallback) must remain intact.
3. Automated Quality Gate:
   - `pnpm check`: SvelteKit type checks with 0 errors.
   - `pnpm test`: Full test suite passes without regressions.
   - `pnpm check:design`: Static stylelint, Vitest component audits, and Playwright runtime audits pass with 0 violations.
4. Backend Capability Alignment:
   - Every setting exposed in the user interface corresponds to a live backend endpoint in the v1 API table (`go/engine/http/server/v1table.go`):
     - Password update: `POST /api/v1/account/password` (`account.password`).
     - Session management: `GET /api/v1/account/sessions` and `DELETE /api/v1/account/sessions/{id}` (`account.sessions.*`).
     - App passwords: `GET /api/v1/account/app-passwords`, `POST /api/v1/account/app-passwords`, `DELETE /api/v1/account/app-passwords/{id}`, and `POST /api/v1/account/app-passwords/{id}/wipe` (`account.app-passwords.*`).
     - Two-factor authentication: `POST /api/v1/account/totp/setup`, `POST /api/v1/account/totp/enroll`, `POST /api/v1/account/totp/disable`, `GET /api/v1/account/totp/recovery-codes`, and `POST /api/v1/account/totp/recovery-codes` (`account.totp.*`).
     - SMB credentials and opt-out: `POST /api/v1/account/smb`, `POST /api/v1/account/smb/password`, and `DELETE /api/v1/account/smb/password` (`account.smb.*`).
     - Single sign-on: `POST /api/v1/account/oidc-link/start` and `DELETE /api/v1/account/oidc-link` (`account.oidc-link.*`).
     - Sign out: `POST /api/v1/auth/logout` (`auth.logout`).
     - Theme and locale: Client browser preferences persisted in local storage (`sc.theme`, `sc.locale`).
   - No mock-only, fabricated, or non-functional settings exist in the interface.
   - Data displayed across the application is strictly verified against real HTTP endpoints (`web/src/lib/api/http.ts`) rather than client-side mock artifacts (`mock.ts`).
   - No view introduces controls or displays that rely on mock-only data shapes.

### 4.5 Admin Server Settings Alignment and Clean Architecture

1. Eradication of Obsolete Configuration References:
   - All references to `sc.toml` across interface copy, tooltips, and locale catalogs have been eliminated.
   - Server configuration is grounded entirely on the database-backed v1 settings endpoints (`GET /api/v1/admin/settings` and `PATCH /api/v1/admin/settings/{section}`).
   - Path read-only hints accurately describe process arguments and container mounts rather than file editing.

2. Material 3 Admin Cards Architecture:
   - Admin server settings are reorganized into dedicated M3 container cards (`.sc-admin-card`) with 40px icon badges, clean headers, 16px responsive padding below 600px, and inline save outcomes.
   - Sections include SMB, Search, Zip Download, Network, Database & Size Guard, Home Folders, Request Rate Limiting, File Watching, Single Sign-On (OIDC), and Runtime Storage Paths.
   - Navigation tabs on `/admin` are enhanced with contextual Material Symbols (`admin`, `folder`, `grid`, `settings`, `recent`).

3. Unified Google Drive Navigation Drawer:
   - The cramped multi-sidebar layout (96px rail beside a 280px drawer beside a 240px docked tree) is replaced by a single unified 256px Navigation Drawer.
   - Header displays the brand logo and title.
   - Prominent Material 3 Extended FAB "+ New" button offers immediate folder creation and file/folder upload across the application.
   - "Files" destination integrates expandable root folder shares directly inside the sidebar with active pill indicators and folder icons.
   - Browse file views dedicate full width to file table and grid presentations without duplicate docked panels.
