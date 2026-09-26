import { globalStyle } from '@vanilla-extract/css';
globalStyle(".sc-edit", {
    display: "flex",
    flexDirection: "column",
    height: "100%",
    minHeight: "0",
    background: "rgb(var(--mdui-color-surface-container-low))",
    color: "rgb(var(--mdui-color-on-surface))"
});
globalStyle(".sc-edit-toolbar", {
    zIndex: "2",
    display: "flex",
    alignItems: "center",
    gap: "12px",
    minHeight: "76px",
    padding: "10px 20px",
    background: "rgb(var(--mdui-color-surface))",
    boxShadow: "0 1px 0 rgb(var(--mdui-color-outline-variant))"
});
globalStyle(".sc-edit-file-icon", {
    display: "inline-flex",
    flex: "none",
    alignItems: "center",
    justifyContent: "center",
    width: "40px",
    height: "40px",
    borderRadius: "var(--mdui-shape-corner-medium, 12px)",
    background: "rgb(var(--mdui-color-primary-container))",
    color: "rgb(var(--mdui-color-on-primary-container))",
    fontSize: "1.35rem"
});
globalStyle(".sc-edit-identity", {
    display: "flex",
    flex: "1",
    flexDirection: "column",
    gap: "4px",
    minWidth: "0"
});
globalStyle(".sc-edit-title,\n.sc-edit-details", {
    display: "flex",
    alignItems: "center",
    gap: "8px",
    minWidth: "0"
});
globalStyle(".sc-edit-filename", {
    overflow: "hidden",
    color: "rgb(var(--mdui-color-on-surface))",
    fontSize: "var(--mdui-typescale-title-medium-size, 1rem)",
    fontWeight: "600",
    lineHeight: "1.35",
    textOverflow: "ellipsis",
    whiteSpace: "nowrap"
});
globalStyle(".sc-edit-details", {
    color: "rgb(var(--mdui-color-on-surface-variant))",
    fontSize: "var(--mdui-typescale-body-small-size, .75rem)",
    lineHeight: "1.25rem"
});
globalStyle(".sc-edit-language,\n.sc-edit-badge", {
    display: "inline-flex",
    flex: "none",
    alignItems: "center",
    minHeight: "22px",
    paddingInline: "8px",
    borderRadius: "var(--mdui-shape-corner-full, 999px)",
    fontSize: "var(--mdui-typescale-label-small-size, .6875rem)",
    fontWeight: "600",
    lineHeight: "1"
});
globalStyle(".sc-edit-language", {
    background: "rgb(var(--mdui-color-secondary-container))",
    color: "rgb(var(--mdui-color-on-secondary-container))"
});
globalStyle(".sc-edit-badge-dirty", {
    background: "rgb(var(--mdui-color-primary-container))",
    color: "rgb(var(--mdui-color-on-primary-container))"
});
globalStyle(".sc-edit-badge-readonly", {
    background: "rgb(var(--mdui-color-error-container))",
    color: "rgb(var(--mdui-color-on-error-container))"
});
globalStyle(".sc-edit-meta,\n.sc-edit-actions", {
    flex: "none"
});
globalStyle(".sc-edit-actions", {
    display: "flex",
    alignItems: "center"
});
globalStyle(".sc-edit-body", {
    position: "relative",
    display: "flex",
    flex: "1",
    minHeight: "0",
    padding: "16px 20px 20px"
});
globalStyle(".sc-edit-loading,\n.sc-edit-locked", {
    display: "flex",
    flex: "1",
    alignItems: "center",
    justifyContent: "center"
});
globalStyle(".sc-edit-locked", {
    flexDirection: "column",
    gap: "16px",
    padding: "24px",
    textAlign: "center"
});
globalStyle(".sc-edit-error", {
    margin: "0",
    padding: "24px",
    color: "rgb(var(--mdui-color-error))"
});
globalStyle(".sc-code-editor", {
    flex: "1",
    minHeight: "0",
    overflow: "hidden",
    border: "1px solid rgb(var(--mdui-color-outline-variant))",
    borderRadius: "var(--mdui-shape-corner-large, 16px)",
    background: "rgb(var(--mdui-color-surface))",
    boxShadow: "0 1px 3px rgb(var(--mdui-color-shadow) / .08)",
    transition: "border-color 120ms ease, box-shadow 120ms ease",
    vars: {
        "--sc-code-active-line": "rgb(var(--mdui-color-primary) / .06)",
        "--sc-code-caret": "rgb(var(--mdui-color-primary))",
        "--sc-code-comment": "rgb(var(--mdui-color-on-surface-variant))",
        "--sc-code-definition": "rgb(var(--mdui-color-tertiary))",
        "--sc-code-foreground": "rgb(var(--mdui-color-on-surface))",
        "--sc-code-gutter": "rgb(var(--mdui-color-surface-container))",
        "--sc-code-gutter-text": "rgb(var(--mdui-color-on-surface-variant))",
        "--sc-code-heading": "rgb(var(--mdui-color-primary))",
        "--sc-code-invalid": "rgb(var(--mdui-color-error))",
        "--sc-code-keyword": "rgb(var(--mdui-color-primary))",
        "--sc-code-link": "rgb(var(--mdui-color-primary))",
        "--sc-code-number": "rgb(var(--mdui-color-secondary))",
        "--sc-code-selection": "rgb(var(--mdui-color-primary) / .18)",
        "--sc-code-string": "rgb(var(--mdui-color-tertiary))",
        "--sc-code-type": "rgb(var(--mdui-color-secondary))"
    }
});
globalStyle(".sc-code-editor:focus-within", {
    borderColor: "rgb(var(--mdui-color-primary))",
    boxShadow: "0 0 0 2px rgb(var(--mdui-color-primary) / .16)"
});
globalStyle(".sc-code-editor .cm-editor", {
    height: "100%"
});
globalStyle(".sc-code-editor-status", {
    margin: "0",
    padding: "24px",
    color: "rgb(var(--mdui-color-on-surface-variant))"
});
globalStyle(".sc-code-editor-status-error", {
    color: "rgb(var(--mdui-color-error))"
});
globalStyle(".sc-edit-toolbar", {
    "@media": {
        "(max-width: 600px)": {
            gap: "8px",
            minHeight: "64px",
            padding: "8px"
        }
    }
});
globalStyle(".sc-edit-file-icon,\n  .sc-edit-meta", {
    "@media": {
        "(max-width: 600px)": {
            display: "none"
        }
    }
});
globalStyle(".sc-edit-title", {
    "@media": {
        "(max-width: 600px)": {
            gap: "6px"
        }
    }
});
globalStyle(".sc-edit-badge-dirty", {
    "@media": {
        "(max-width: 600px)": {
            width: "8px",
            minHeight: "8px",
            padding: "0",
            overflow: "hidden",
            color: "transparent",
            fontSize: "0"
        }
    }
});
globalStyle(".sc-edit-body", {
    "@media": {
        "(max-width: 600px)": {
            padding: "0"
        }
    }
});
globalStyle(".sc-code-editor", {
    "@media": {
        "(max-width: 600px)": {
            border: "0",
            borderRadius: "0",
            boxShadow: "none"
        }
    }
});
globalStyle(".sc-code-editor:focus-within", {
    "@media": {
        "(max-width: 600px)": {
            boxShadow: "inset 0 2px 0 rgb(var(--mdui-color-primary))"
        }
    }
});
globalStyle(".sc-code-editor", {
    "@media": {
        "(prefers-reduced-motion: reduce)": {
            transition: "none"
        }
    }
});
