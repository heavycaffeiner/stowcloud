import { globalStyle } from '@vanilla-extract/css';
globalStyle(".sc-auth-page", {
    minHeight: "100dvh",
    minWidth: "0",
    overflowY: "auto",
    display: "flex",
    alignItems: "center",
    justifyContent: "center",
    padding: "var(--sc-page-pad)",
    background: "radial-gradient(circle at 20% 15%, rgba(var(--mdui-color-primary), 0.17), transparent 24rem),\n    radial-gradient(circle at 85% 80%, rgba(var(--mdui-color-secondary), 0.12), transparent 28rem),\n    var(--sc-page-surface)"
});
globalStyle(".sc-auth-card", {
    width: "min(100%, 42rem)",
    minWidth: "0",
    display: "flex",
    flexDirection: "column",
    gap: "16px",
    padding: "24px",
    borderRadius: "var(--sc-radius-auth)",
    background: "var(--sc-raised-surface)",
    border: "1px solid var(--sc-outline-variant)",
    boxShadow: "var(--sc-elevation-1)",
    animation: "sc-scale-up 240ms cubic-bezier(0.2, 0, 0, 1)"
});
globalStyle(".sc-auth-card--login", {
    width: "min(100%, 36rem)",
    marginInline: "auto",
    borderRadius: "var(--sc-radius-large)",
    boxShadow: "none"
});
globalStyle(".sc-auth-card-title", {
    minWidth: "0",
    margin: "0",
    overflowWrap: "anywhere",
    fontSize: "var(--mdui-typescale-headline-small-size)",
    fontWeight: "var(--mdui-typescale-headline-small-weight)",
    lineHeight: "var(--mdui-typescale-headline-small-line-height)",
    letterSpacing: "var(--mdui-typescale-headline-small-tracking)",
    textAlign: "center"
});
globalStyle(".sc-auth-card-subtitle,\n.sc-auth-card-licence,\n.sc-auth-card-hint", {
    margin: "0",
    color: "var(--sc-content-secondary)",
    overflowWrap: "anywhere"
});
globalStyle(".sc-auth-card-subtitle,\n.sc-auth-card-licence", {
    textAlign: "center"
});
globalStyle(".sc-auth-card-error,\n.sc-auth-card-success,\n.sc-auth-card-warning", {
    margin: "0",
    padding: "12px 16px",
    borderRadius: "var(--sc-radius-small)",
    overflowWrap: "anywhere"
});
globalStyle(".sc-auth-card-error", {
    background: "var(--sc-state-error)",
    color: "var(--sc-state-error-content)"
});
globalStyle(".sc-auth-card-success", {
    background: "var(--sc-mdui-primary-container, var(--sc-state-selection))",
    color: "var(--sc-mdui-on-primary-container, var(--sc-state-selection-content))"
});
globalStyle(".sc-auth-card-warning", {
    display: "flex",
    flexDirection: "column",
    gap: "8px",
    background: "var(--sc-state-warning)",
    color: "var(--sc-state-warning-content)"
});
globalStyle(".sc-auth-card-warning p", {
    margin: "0"
});
globalStyle(".sc-auth-card-actions", {
    display: "flex",
    alignItems: "center",
    justifyContent: "flex-end",
    flexWrap: "wrap",
    gap: "8px",
    marginTop: "8px",
    width: "100%"
});
globalStyle(".sc-auth-card-divider", {
    display: "flex",
    alignItems: "center",
    gap: "12px",
    color: "var(--sc-content-secondary)"
});
globalStyle(".sc-auth-card-divider::before,\n.sc-auth-card-divider::after", {
    content: "''",
    flex: "1",
    borderTop: "1px solid var(--sc-content-secondary)"
});
globalStyle(".sc-auth-card-setup-link", {
    paddingBlock: "4px",
    color: "rgb(var(--mdui-color-primary))",
    textAlign: "center",
    textDecoration: "none",
    minHeight: "40px",
    display: "flex",
    alignItems: "center",
    justifyContent: "center",
    overflowWrap: "anywhere"
});
globalStyle(".sc-auth-card-setup-link:hover", {
    textDecoration: "underline"
});
globalStyle(".sc-auth-card-licence a", {
    color: "rgb(var(--mdui-color-primary))"
});
globalStyle(".sc-auth-card-steps", {
    display: "flex",
    gap: "8px",
    flexWrap: "wrap",
    padding: "8px 0",
    color: "var(--sc-content-secondary)"
});
globalStyle(".sc-auth-card-steps span", {
    padding: "8px 12px",
    borderRadius: "var(--sc-radius-small)",
    border: "1px solid var(--sc-outline-variant)"
});
globalStyle(".sc-auth-card-steps .is-active", {
    color: "rgb(var(--mdui-color-primary))",
    borderColor: "rgb(var(--mdui-color-primary))"
});
globalStyle(".sc-auth-card-section", {
    margin: "8px 0 0",
    fontSize: "var(--mdui-typescale-title-medium-size)",
    fontWeight: "var(--mdui-typescale-title-medium-weight)",
    lineHeight: "var(--mdui-typescale-title-medium-line-height)",
    letterSpacing: "var(--mdui-typescale-title-medium-tracking)"
});
globalStyle(".sc-auth-card-path-row", {
    display: "flex",
    gap: "8px",
    alignItems: "center"
});
globalStyle(".sc-auth-card-path-row > .sc-field", {
    flex: "1 1 auto",
    minWidth: "0"
});
globalStyle(".sc-auth-card-strength", {
    display: "flex",
    alignItems: "center",
    gap: "8px",
    marginTop: "-8px"
});
globalStyle(".sc-auth-card-strength mdui-linear-progress", {
    flex: "1"
});
globalStyle(".sc-auth-card-strength-label", {
    flex: "0 0 auto",
    minWidth: "48px",
    color: "var(--sc-content-secondary)"
});
globalStyle(".sc-auth-page", {
    "@media": {
        "(max-width: 599.98px)": {
            alignItems: "flex-start",
            paddingBlock: "16px"
        }
    }
});
globalStyle(".sc-auth-card", {
    "@media": {
        "(max-width: 599.98px)": {
            gap: "14px",
            padding: "20px 16px",
            borderRadius: "var(--sc-radius-auth)"
        }
    }
});
globalStyle(".sc-auth-card-setup-link,\n  .sc-auth-card .sc-button-wrap > mdui-button", {
    "@media": {
        "(max-width: 599.98px)": {
            minHeight: "44px"
        }
    }
});
globalStyle(".sc-auth-card-path-row", {
    "@media": {
        "(max-width: 599.98px)": {
            alignItems: "stretch",
            flexDirection: "column"
        }
    }
});
globalStyle(".sc-auth-card-path-row > .sc-button-wrap,\n  .sc-auth-card-path-row > .sc-button-wrap > mdui-button", {
    "@media": {
        "(max-width: 599.98px)": {
            width: "100%"
        }
    }
});
globalStyle(".sc-auth-card-actions > .sc-button-wrap", {
    "@media": {
        "(max-width: 599.98px)": {
            flex: "1 1 8rem",
            maxWidth: "100%"
        }
    }
});
globalStyle(".sc-auth-card-actions > .sc-button-wrap > mdui-button", {
    "@media": {
        "(max-width: 599.98px)": {
            width: "100%",
            maxWidth: "100%"
        }
    }
});
