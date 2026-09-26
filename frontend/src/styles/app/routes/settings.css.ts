import { globalStyle } from '@vanilla-extract/css';
globalStyle(".sc-settings-page-grid", {
    display: "grid",
    gap: "0",
    minWidth: "0"
});
globalStyle(".sc-settings-card-icon", {
    flex: "none",
    display: "flex",
    alignItems: "center",
    justifyContent: "center",
    width: "40px",
    height: "40px",
    borderRadius: "var(--mdui-shape-corner-small, 8px)",
    background: "rgb(var(--mdui-color-surface-container-highest))",
    color: "rgb(var(--mdui-color-primary))"
});
globalStyle(".sc-settings-card-meta", {
    flex: "1 1 auto",
    minWidth: "0",
    overflowWrap: "anywhere"
});
globalStyle(".sc-settings-avatar", {
    flex: "none",
    display: "flex",
    alignItems: "center",
    justifyContent: "center",
    width: "40px",
    height: "40px",
    borderRadius: "var(--mdui-shape-corner-full, 999px)",
    background: "rgb(var(--mdui-color-primary))",
    color: "rgb(var(--mdui-color-on-primary))",
    fontSize: "var(--mdui-typescale-title-medium-size, 1rem)",
    fontWeight: "600"
});
globalStyle(".sc-settings-account-name", {
    margin: "0 0 4px",
    overflowWrap: "anywhere",
    fontSize: "var(--mdui-typescale-body-large-size, 1rem)",
    fontWeight: "500"
});
globalStyle(".sc-settings-username", {
    margin: "0",
    overflowWrap: "anywhere",
    color: "rgb(var(--mdui-color-on-surface-variant))",
    fontSize: "var(--mdui-typescale-body-small-size, 0.75rem)"
});
globalStyle(".sc-settings-card h2,\n.sc-settings-card-hint,\n.sc-settings-card p", {
    overflowWrap: "anywhere"
});
globalStyle(".sc-settings-card p", {
    margin: "0"
});
globalStyle(".sc-settings-card-hint", {
    maxWidth: "30rem",
    margin: "0 0 4px",
    color: "rgb(var(--mdui-color-on-surface-variant))",
    fontSize: "var(--mdui-typescale-body-small-size)",
    fontWeight: "var(--mdui-typescale-body-small-weight)",
    letterSpacing: "var(--mdui-typescale-body-small-tracking)",
    lineHeight: "var(--mdui-typescale-body-small-line-height)"
});
globalStyle(".sc-settings-card-buttons,\n.sc-smb-actions,\n.sc-password-form-actions,\n.sc-app-passwords-actions", {
    display: "flex",
    flexWrap: "wrap",
    alignItems: "center",
    justifyContent: "flex-start",
    gap: "8px"
});
globalStyle(".sc-settings-card input[type='number']", {
    inlineSize: "8rem",
    minBlockSize: "3.5rem",
    boxSizing: "border-box",
    padding: "0 16px",
    border: "none",
    borderRadius: "var(--mdui-shape-corner-extra-small)",
    background: "rgb(var(--mdui-color-surface-container-highest))",
    color: "rgb(var(--mdui-color-on-surface))"
});
globalStyle(".sc-settings-row", {
    display: "flex",
    alignItems: "center",
    flexWrap: "wrap",
    gap: "12px",
    minWidth: "0"
});
globalStyle(".sc-settings-row--segmented", {
    display: "block",
    maxWidth: "100%",
    overflowX: "auto",
    paddingBlock: "2px",
    scrollbarWidth: "none"
});
globalStyle(".sc-settings-row--segmented::-webkit-scrollbar", {
    display: "none"
});
globalStyle(".sc-settings-row--segmented mdui-segmented-button-group", {
    width: "max-content",
    minWidth: "max-content"
});
globalStyle(".sc-settings-list,\n.sc-sessions-list,\n.sc-app-passwords-list", {
    listStyle: "none",
    padding: "0",
    margin: "0",
    overflow: "hidden",
    border: "1px solid var(--sc-outline-variant)",
    borderRadius: "var(--sc-radius-medium)",
    background: "transparent",
    display: "flex",
    flexDirection: "column",
    gap: "0"
});
globalStyle(".sc-settings-list li,\n.sc-sessions-list li,\n.sc-app-passwords-list li", {
    display: "flex",
    alignItems: "center",
    justifyContent: "space-between",
    gap: "16px",
    minBlockSize: "56px",
    padding: "12px 16px",
    borderRadius: "0",
    background: "transparent",
    transition: "background-color 140ms ease"
});
globalStyle(".sc-settings-list li > :first-child,\n.sc-sessions-list li > :first-child,\n.sc-app-passwords-list li > :first-child", {
    minWidth: "0"
});
globalStyle(".sc-settings-list li:hover,\n.sc-sessions-list li:hover,\n.sc-app-passwords-list li:hover", {
    background: "rgb(var(--mdui-color-surface-container-low))"
});
globalStyle(".sc-settings-list li + li,\n.sc-sessions-list li + li,\n.sc-app-passwords-list li + li", {
    borderTop: "1px solid var(--sc-outline-variant)"
});
globalStyle(".sc-settings-list p,\n.sc-sessions-list p,\n.sc-app-passwords-list p", {
    margin: "4px 0 0",
    color: "rgb(var(--mdui-color-on-surface-variant))",
    fontSize: "var(--mdui-typescale-body-small-size)",
    lineHeight: "var(--mdui-typescale-body-small-line-height)"
});
globalStyle(".sc-settings-badge, .sc-sessions-badge", {
    display: "inline-flex",
    alignItems: "center",
    minBlockSize: "24px",
    marginInlineStart: "8px",
    paddingInline: "8px",
    borderRadius: "var(--mdui-shape-corner-full)",
    background: "rgb(var(--mdui-color-surface-container-highest))",
    color: "rgb(var(--mdui-color-on-surface-variant))",
    fontSize: "var(--mdui-typescale-label-small-size)",
    fontWeight: "var(--mdui-typescale-label-small-weight)",
    lineHeight: "var(--mdui-typescale-label-small-line-height)",
    whiteSpace: "nowrap",
    verticalAlign: "middle"
});
globalStyle(".sc-settings-card-head > .sc-settings-badge", {
    flex: "none",
    marginInlineStart: "auto"
});
globalStyle(".sc-settings-badge--on", {
    background: "rgb(var(--mdui-color-primary-container))",
    color: "rgb(var(--mdui-color-on-primary-container))"
});
globalStyle(".sc-settings-state, .sc-smb-state", {
    margin: "0",
    padding: "0",
    background: "transparent",
    color: "rgb(var(--mdui-color-on-surface-variant))",
    fontSize: "var(--mdui-typescale-body-medium-size)",
    lineHeight: "var(--mdui-typescale-body-medium-line-height)",
    overflowWrap: "anywhere"
});
globalStyle(".sc-settings-error, .sc-password-form-error, .sc-app-passwords-error,\n.sc-sessions-error, .sc-app-passwords-copy-feedback,\n.sc-totp-copy-feedback, .sc-oidc-error", {
    margin: "0",
    color: "rgb(var(--mdui-color-error))",
    overflowWrap: "anywhere"
});
globalStyle(".sc-settings-success, .sc-password-form-success", {
    margin: "0",
    color: "rgb(var(--mdui-color-primary))"
});
globalStyle(".sc-settings-codes, .sc-totp-codes", {
    display: "grid",
    gridTemplateColumns: "repeat(2, minmax(0, 1fr))",
    gap: "8px",
    listStyle: "none",
    margin: "0",
    padding: "0"
});
globalStyle(".sc-settings-codes input, .sc-token-row input, .sc-settings-token,\n.sc-totp-code, .sc-totp-secret", {
    width: "100%",
    boxSizing: "border-box",
    padding: "8px 12px",
    minHeight: "40px",
    height: "40px",
    lineHeight: "24px",
    border: "0",
    borderRadius: "var(--mdui-shape-corner-extra-small)",
    background: "rgb(var(--mdui-color-surface-container-highest))",
    color: "rgb(var(--mdui-color-on-surface))",
    font: "inherit",
    overflowWrap: "anywhere"
});
globalStyle(".sc-settings-dialog-body", {
    display: "flex",
    flexDirection: "column",
    gap: "16px",
    minWidth: "0"
});
globalStyle(".sc-token-row, .sc-webdav-token-row, .sc-totp-secret-row", {
    display: "flex",
    alignItems: "center",
    gap: "8px",
    minWidth: "0"
});
globalStyle(".sc-webdav, .sc-oidc, .sc-smb, .sc-totp, .sc-sessions, .sc-app-passwords", {
    display: "flex",
    flexDirection: "column",
    gap: "16px",
    minWidth: "0"
});
globalStyle(".sc-webdav section.sc-webdav-os", {
    display: "flex",
    flexDirection: "column",
    gap: "12px",
    padding: "16px 0 0",
    border: "0",
    borderTop: "1px solid var(--sc-outline-variant)",
    borderRadius: "0",
    background: "transparent",
    boxShadow: "none"
});
globalStyle(".sc-webdav section h3", {
    margin: "0",
    fontSize: "var(--mdui-typescale-title-medium-size)",
    lineHeight: "var(--mdui-typescale-title-medium-line-height)",
    color: "rgb(var(--mdui-color-primary))",
    fontWeight: "600"
});
globalStyle(".sc-webdav section h4", {
    margin: "12px 0 4px",
    fontSize: "var(--mdui-typescale-title-small-size)",
    lineHeight: "var(--mdui-typescale-title-small-line-height)"
});
globalStyle(".sc-webdav ol", {
    margin: "0",
    paddingInlineStart: "20px",
    display: "flex",
    flexDirection: "column",
    gap: "6px",
    color: "rgb(var(--mdui-color-on-surface-variant))",
    fontSize: "var(--mdui-typescale-body-medium-size)"
});
globalStyle(".sc-webdav pre", {
    maxWidth: "100%",
    margin: "0",
    whiteSpace: "pre-wrap",
    overflowWrap: "anywhere"
});
globalStyle(".sc-password-form", {
    display: "flex",
    flexDirection: "column",
    gap: "16px",
    width: "100%",
    maxWidth: "28rem"
});
globalStyle(".sc-password-form-actions", {
    marginTop: "8px"
});
globalStyle(".sc-password-form-strength", {
    display: "flex",
    alignItems: "center",
    gap: "8px",
    marginTop: "-8px"
});
globalStyle(".sc-password-form-strength progress", {
    flex: "1"
});
globalStyle(".sc-password-form-strength-label", {
    flex: "0 0 auto",
    minInlineSize: "48px",
    color: "rgb(var(--mdui-color-on-surface-variant))"
});
globalStyle(".sc-settings-switch", {
    display: "flex",
    alignItems: "center",
    gap: "8px",
    minBlockSize: "48px"
});
globalStyle(".sc-totp-status, .sc-oidc-status, .sc-totp-recovery", {
    display: "flex",
    alignItems: "center",
    flexWrap: "wrap",
    gap: "16px"
});
globalStyle(".sc-totp-badge, .sc-oidc-badge", {
    display: "inline-flex",
    alignItems: "center",
    minBlockSize: "24px",
    paddingInline: "8px",
    borderRadius: "var(--mdui-shape-corner-full)",
    background: "rgb(var(--mdui-color-surface-container-highest))",
    color: "rgb(var(--mdui-color-on-surface-variant))",
    fontSize: "var(--mdui-typescale-label-small-size)",
    lineHeight: "var(--mdui-typescale-label-small-line-height)"
});
globalStyle(".sc-totp-badge--on, .sc-oidc-badge--on", {
    background: "rgb(var(--mdui-color-primary-container))",
    color: "rgb(var(--mdui-color-on-primary-container))"
});
globalStyle(".sc-totp-recovery-count", {
    margin: "0",
    color: "rgb(var(--mdui-color-on-surface-variant))"
});
globalStyle(".sc-totp-recovery-count--low", {
    color: "rgb(var(--mdui-color-error))",
    fontWeight: "500"
});
globalStyle(".sc-totp-smb-warning, .sc-oidc-warning", {
    display: "flex",
    alignItems: "flex-start",
    gap: "8px",
    margin: "0",
    padding: "12px",
    borderRadius: "var(--mdui-shape-corner-extra-small)",
    background: "rgb(var(--mdui-color-error-container))",
    color: "rgb(var(--mdui-color-on-error-container))"
});
globalStyle(".sc-totp-url, .sc-oidc-detail, .sc-oidc-error, .sc-smb-note, .sc-smb-announce,\n.sc-webdav-credentials, .sc-webdav-nfc-note, .sc-webdav-announce", {
    margin: "0",
    color: "rgb(var(--mdui-color-on-surface-variant))",
    overflowWrap: "anywhere"
});
globalStyle(".sc-app-passwords-name", {
    minWidth: "0",
    overflowWrap: "anywhere"
});
globalStyle(".sc-app-passwords-actions", {
    marginTop: "0"
});
globalStyle(".sc-webdav-label", {
    display: "block",
    marginBottom: "4px",
    color: "rgb(var(--mdui-color-on-surface-variant))"
});
globalStyle(".sc-webdav-token", {
    flex: "1",
    minWidth: "0",
    display: "inline-flex",
    alignItems: "center",
    minHeight: "40px",
    margin: "0",
    padding: "8px 12px",
    boxSizing: "border-box",
    fontFamily: "ui-monospace, monospace",
    fontSize: "var(--mdui-typescale-body-medium-size)",
    borderRadius: "var(--mdui-shape-corner-extra-small)",
    background: "rgb(var(--mdui-color-surface-container-highest))",
    color: "rgb(var(--mdui-color-on-surface))",
    overflowWrap: "anywhere"
});
globalStyle(".sc-settings-codes input:focus-visible,\n.sc-token-row input:focus-visible,\n.sc-totp-code:focus-visible,\n.sc-totp-secret:focus-visible", {
    outline: "var(--sc-focus-ring-width) solid var(--sc-state-focus)",
    outlineOffset: "var(--sc-focus-ring-offset)"
});
globalStyle(".sc-settings-row mdui-segmented-button-group", {
    maxWidth: "100%"
});
globalStyle(".sc-settings-card-head > .sc-settings-badge", {
    "@media": {
        "(max-width: 599.98px)": {
            marginInlineStart: "0"
        }
    }
});
globalStyle(".sc-settings-list li, .sc-sessions-list li, .sc-app-passwords-list li", {
    "@media": {
        "(max-width: 599.98px)": {
            alignItems: "stretch",
            flexDirection: "column",
            padding: "12px"
        }
    }
});
globalStyle(".sc-settings-list li > div:last-child, .sc-sessions-list li > button, .sc-app-passwords-list li > div:last-child", {
    "@media": {
        "(max-width: 599.98px)": {
            alignSelf: "stretch"
        }
    }
});
globalStyle(".sc-settings-card-buttons, .sc-smb-actions, .sc-password-form-actions, .sc-app-passwords-actions", {
    "@media": {
        "(max-width: 599.98px)": {
            alignItems: "stretch"
        }
    }
});
globalStyle(".sc-token-row, .sc-webdav-token-row, .sc-totp-secret-row", {
    "@media": {
        "(max-width: 599.98px)": {
            alignItems: "stretch"
        }
    }
});
globalStyle(".sc-settings-codes, .sc-totp-codes", {
    "@media": {
        "(max-width: 599.98px)": {
            gridTemplateColumns: "1fr"
        }
    }
});
globalStyle(".sc-settings-row--segmented", {
    "@media": {
        "(max-width: 599.98px)": {
            maxWidth: "calc(100vw - (2 * var(--sc-content-pad)))"
        }
    }
});
