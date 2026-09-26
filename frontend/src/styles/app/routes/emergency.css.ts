import { globalStyle } from '@vanilla-extract/css';
globalStyle(".sc-emergency", {
    display: "flex",
    alignItems: "center",
    justifyContent: "center",
    minHeight: "100dvh",
    minWidth: "0",
    overflowY: "auto",
    padding: "var(--sc-page-pad)"
});
globalStyle(".sc-emergency-card", {
    display: "flex",
    flexDirection: "column",
    gap: "16px",
    width: "min(640px, 100%)",
    minWidth: "0",
    padding: "24px",
    borderRadius: "24px",
    background: "var(--sc-container-surface)",
    boxShadow: "0 8px 24px rgb(0 0 0 / 0.2)"
});
globalStyle(".sc-emergency-title", {
    margin: "0",
    overflowWrap: "anywhere",
    fontSize: "var(--mdui-typescale-headline-small-size)",
    fontWeight: "var(--mdui-typescale-headline-small-weight)",
    lineHeight: "var(--mdui-typescale-headline-small-line-height)",
    letterSpacing: "var(--mdui-typescale-headline-small-tracking)"
});
globalStyle(".sc-emergency-subtitle,\n.sc-emergency-hint", {
    margin: "0",
    color: "var(--sc-content-secondary)",
    overflowWrap: "anywhere"
});
globalStyle(".sc-emergency-form", {
    display: "flex",
    flexDirection: "column",
    gap: "12px"
});
globalStyle(".sc-emergency-actions", {
    display: "flex",
    gap: "8px",
    flexWrap: "wrap",
    alignItems: "center"
});
globalStyle(".sc-emergency-label", {
    fontSize: "var(--mdui-typescale-label-large-size)",
    fontWeight: "var(--mdui-typescale-label-large-weight)",
    lineHeight: "var(--mdui-typescale-label-large-line-height)",
    letterSpacing: "var(--mdui-typescale-label-large-tracking)"
});
globalStyle(".sc-emergency-select,\n.sc-emergency-textarea", {
    width: "100%",
    minWidth: "0",
    padding: "8px 12px",
    border: "none",
    borderRadius: "var(--mdui-shape-corner-small, 8px)",
    background: "rgb(var(--mdui-color-surface-container-highest))",
    color: "var(--sc-content-primary)",
    font: "inherit"
});
globalStyle(".sc-emergency-select", {
    minHeight: "48px"
});
globalStyle(".sc-emergency-select:focus-visible,\n.sc-emergency-textarea:focus-visible", {
    outline: "var(--sc-focus-ring-width) solid var(--sc-state-focus)",
    outlineOffset: "var(--sc-focus-ring-offset)"
});
globalStyle(".sc-emergency-textarea", {
    minHeight: "18rem",
    fontFamily: "ui-monospace, monospace",
    maxWidth: "100%",
    resize: "vertical"
});
globalStyle(".sc-emergency-facts", {
    display: "grid",
    gridTemplateColumns: "auto 1fr",
    gap: "4px 16px",
    margin: "0"
});
globalStyle(".sc-emergency-facts dt", {
    color: "var(--sc-content-secondary)"
});
globalStyle(".sc-emergency-facts dd", {
    minWidth: "0",
    margin: "0",
    overflowWrap: "anywhere"
});
globalStyle(".sc-emergency-banner,\n.sc-emergency-warning,\n.sc-emergency-error", {
    margin: "0",
    padding: "12px 16px",
    borderRadius: "8px",
    overflowWrap: "anywhere"
});
globalStyle(".sc-emergency-banner,\n.sc-emergency-warning", {
    background: "var(--sc-state-warning)",
    color: "var(--sc-state-warning-content)"
});
globalStyle(".sc-emergency-error", {
    background: "var(--sc-state-error)",
    color: "var(--sc-state-error-content)"
});
globalStyle(".sc-emergency-ok", {
    margin: "0",
    color: "var(--mdui-color-primary, var(--sc-state-focus))",
    overflowWrap: "anywhere"
});
globalStyle(".sc-emergency", {
    "@media": {
        "(max-width: 599.98px)": {
            alignItems: "flex-start",
            paddingBlock: "16px"
        }
    }
});
globalStyle(".sc-emergency-card", {
    "@media": {
        "(max-width: 599.98px)": {
            gap: "14px",
            padding: "20px 16px",
            borderRadius: "20px"
        }
    }
});
globalStyle(".sc-emergency-actions > .sc-button-wrap > mdui-button", {
    "@media": {
        "(max-width: 599.98px)": {
            minHeight: "44px"
        }
    }
});
globalStyle(".sc-emergency-facts", {
    "@media": {
        "(max-width: 599.98px)": {
            gridTemplateColumns: "minmax(0, 1fr)",
            gap: "2px"
        }
    }
});
globalStyle(".sc-emergency-facts dd + dt", {
    "@media": {
        "(max-width: 599.98px)": {
            marginTop: "8px"
        }
    }
});
globalStyle(".sc-emergency-actions > .sc-button-wrap", {
    "@media": {
        "(max-width: 599.98px)": {
            flex: "1 1 12rem"
        }
    }
});
globalStyle(".sc-emergency-actions > .sc-button-wrap > mdui-button", {
    "@media": {
        "(max-width: 599.98px)": {
            width: "100%"
        }
    }
});
