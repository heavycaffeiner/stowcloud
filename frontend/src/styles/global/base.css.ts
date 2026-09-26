import { globalKeyframes, globalStyle } from '@vanilla-extract/css';
globalStyle(":root", {
    colorScheme: "light dark",
    fontFamily: "'Google Sans Flex Variable', system-ui, -apple-system, BlinkMacSystemFont, 'Segoe UI', sans-serif",
    vars: {
        "--mdui-color-primary-light": "14, 56, 94",
        "--mdui-color-primary-dark": "154, 203, 255",
        "--sc-page-surface": "rgb(var(--mdui-color-surface))",
        "--sc-container-surface": "rgb(var(--mdui-color-surface-container-low))",
        "--sc-raised-surface": "rgb(var(--mdui-color-surface-container))",
        "--sc-overlay-surface": "rgb(var(--mdui-color-surface-container-high))",
        "--sc-content-primary": "rgb(var(--mdui-color-on-surface))",
        "--sc-content-secondary": "rgb(var(--mdui-color-on-surface-variant))",
        "--sc-icon-color": "#475569",
        "--sc-outline": "rgb(var(--mdui-color-outline))",
        "--sc-outline-variant": "rgb(var(--mdui-color-outline-variant))",
        "--sc-state-selection": "rgb(var(--mdui-color-secondary-container))",
        "--sc-state-selection-content": "rgb(var(--mdui-color-on-secondary-container))",
        "--sc-state-focus": "rgb(var(--mdui-color-secondary))",
        "--sc-state-warning": "rgb(var(--mdui-color-tertiary-container))",
        "--sc-state-warning-content": "rgb(var(--mdui-color-on-tertiary-container))",
        "--sc-state-error": "rgb(var(--mdui-color-error-container))",
        "--sc-state-error-content": "rgb(var(--mdui-color-on-error-container))",
        "--m3c-surface": "var(--sc-page-surface)",
        "--m3c-surface-container-low": "var(--sc-container-surface)",
        "--m3c-surface-container": "var(--sc-raised-surface)",
        "--m3c-surface-container-high": "var(--sc-overlay-surface)",
        "--m3c-surface-container-highest": "rgb(var(--mdui-color-surface-container-highest))",
        "--m3c-on-surface": "var(--sc-content-primary)",
        "--m3c-on-surface-variant": "var(--sc-content-secondary)",
        "--m3c-outline": "var(--sc-outline)",
        "--m3c-outline-variant": "var(--sc-outline-variant)",
        "--m3c-primary": "rgb(var(--mdui-color-primary))",
        "--m3c-on-primary": "rgb(var(--mdui-color-on-primary))",
        "--m3c-primary-container": "rgb(var(--mdui-color-primary-container))",
        "--m3c-on-primary-container": "rgb(var(--mdui-color-on-primary-container))",
        "--m3c-secondary": "var(--sc-state-focus)",
        "--m3c-secondary-container": "var(--sc-state-selection)",
        "--m3c-on-secondary-container": "var(--sc-state-selection-content)",
        "--m3c-tertiary": "rgb(var(--mdui-color-tertiary))",
        "--m3c-tertiary-container": "var(--sc-state-warning)",
        "--m3c-on-tertiary-container": "var(--sc-state-warning-content)",
        "--m3c-error": "rgb(var(--mdui-color-error))",
        "--m3c-error-container": "var(--sc-state-error)",
        "--m3c-on-error-container": "var(--sc-state-error-content)",
        "--m3c-inverse-surface": "rgb(var(--mdui-color-inverse-surface))",
        "--m3c-inverse-on-surface": "rgb(var(--mdui-color-inverse-on-surface))",
        "--m3c-scrim": "rgb(var(--mdui-color-scrim))",
        "--sc-page-pad": "16px",
        "--sc-page-max": "72rem",
        "--sc-section-gap": "24px",
        "--sc-nav-bar-height": "4rem",
        "--sc-nav-drawer-width": "240px",
        "--sc-nav-drawer-collapsed-width": "68px",
        "--sc-shell-header-height": "56px",
        "--sc-row-height": "48px",
        "--sc-content-pad": "24px",
        "--sc-radius-small": "8px",
        "--sc-radius-medium": "12px",
        "--sc-radius-large": "16px",
        "--sc-radius-auth": "20px",
        "--sc-radius-full": "999px",
        "--sc-radius-card": "var(--sc-radius-medium)",
        "--sc-radius-control": "var(--sc-radius-small)",
        "--sc-elevation-1": "0 1px 2px rgb(0 0 0 / 0.06)",
        "--sc-elevation-2": "0 4px 12px rgb(0 0 0 / 0.10)",
        "--sc-elevation-3": "0 8px 24px rgb(0 0 0 / 0.18)",
        "--sc-elevation-4": "0 16px 40px rgb(0 0 0 / 0.22)",
        "--sc-shadow-card": "var(--sc-elevation-1)",
        "--sc-control-min-desktop": "40px",
        "--sc-control-min-compact": "44px",
        "--sc-control-min": "var(--sc-control-min-desktop)",
        "--sc-focus-ring-width": "3px",
        "--sc-focus-ring-offset": "2px",
        "--sc-focus-ring-inset-offset": "-3px",
        "--mdui-typescale-label-small-line-height": "1rem"
    }
});
globalStyle(":root", {
    "@media": {
        "(max-width: 599.98px)": {
            vars: {
                "--sc-content-pad": "16px",
                "--sc-control-min": "var(--sc-control-min-compact)"
            }
        }
    }
});
globalStyle(":root", {
    "@media": {
        "(min-width: 600px)": {
            vars: { "--sc-page-pad": "24px" }
        }
    }
});
globalStyle(":root", {
    "@media": {
        "(min-width: 905px)": {
            vars: { "--sc-page-pad": "32px" }
        }
    }
});
globalStyle(".mdui-theme-dark", {
    vars: { "--sc-icon-color": "#cbd5e1" }
});
globalStyle(":root:not(.mdui-theme-light)", {
    "@media": {
        "(prefers-color-scheme: dark)": {
            vars: { "--sc-icon-color": "#cbd5e1" }
        }
    }
});
globalKeyframes("sc-fade-in-up", {
    "from": {
        opacity: "0",
        transform: "translateY(8px)"
    },
    "to": {
        opacity: "1",
        transform: "translateY(0)"
    }
});
globalKeyframes("sc-scale-up", {
    "from": {
        opacity: "0",
        transform: "scale(0.96)"
    },
    "to": {
        opacity: "1",
        transform: "scale(1)"
    }
});
globalKeyframes("sc-fade-in", {
    "from": {
        opacity: "0"
    },
    "to": {
        opacity: "1"
    }
});
globalStyle("*", {
    boxSizing: "border-box",
    WebkitTapHighlightColor: "transparent"
});
globalStyle("html, body, #root", {
    minHeight: "100%",
    margin: "0"
});
globalStyle("html", {
    height: "100%"
});
globalStyle("body", {
    minWidth: "320px",
    background: "var(--sc-page-surface)",
    color: "var(--sc-content-primary)",
    fontFamily: "inherit",
    fontFeatureSettings: "'tnum' 1",
    fontSize: "var(--mdui-typescale-body-large-size)",
    fontWeight: "var(--mdui-typescale-body-large-weight)",
    lineHeight: "var(--mdui-typescale-body-large-line-height)",
    letterSpacing: "var(--mdui-typescale-body-large-tracking)"
});
globalStyle(":where(button, input, select, textarea)", {
    font: "inherit"
});
globalStyle("svg", {
    display: "inline-block",
    verticalAlign: "middle",
    flexShrink: "0"
});
globalStyle("h1, h2, h3, h4, h5, h6", {
    margin: "0",
    wordBreak: "keep-all"
});
globalStyle("h1", {
    fontSize: "var(--mdui-typescale-headline-small-size)",
    fontWeight: "var(--mdui-typescale-headline-small-weight)",
    lineHeight: "var(--mdui-typescale-headline-small-line-height)",
    letterSpacing: "var(--mdui-typescale-headline-small-tracking)"
});
globalStyle(".sc-headline", {
    fontSize: "var(--mdui-typescale-headline-large-size)",
    fontWeight: "var(--mdui-typescale-headline-large-weight)",
    lineHeight: "var(--mdui-typescale-headline-large-line-height)",
    letterSpacing: "var(--mdui-typescale-headline-large-tracking)"
});
globalStyle("h2, .sc-title", {
    fontSize: "var(--mdui-typescale-title-large-size)",
    fontWeight: "var(--mdui-typescale-title-large-weight)",
    lineHeight: "var(--mdui-typescale-title-large-line-height)",
    letterSpacing: "var(--mdui-typescale-title-large-tracking)"
});
globalStyle("h3, .sc-title-medium", {
    fontSize: "var(--mdui-typescale-title-medium-size)",
    fontWeight: "var(--mdui-typescale-title-medium-weight)",
    lineHeight: "var(--mdui-typescale-title-medium-line-height)",
    letterSpacing: "var(--mdui-typescale-title-medium-tracking)"
});
globalStyle("h4, h5, h6, .sc-title-small", {
    fontSize: "var(--mdui-typescale-title-small-size)",
    fontWeight: "var(--mdui-typescale-title-small-weight)",
    lineHeight: "var(--mdui-typescale-title-small-line-height)",
    letterSpacing: "var(--mdui-typescale-title-small-tracking)"
});
globalStyle(".sc-body-large", {
    fontSize: "var(--mdui-typescale-body-large-size)",
    fontWeight: "var(--mdui-typescale-body-large-weight)",
    lineHeight: "var(--mdui-typescale-body-large-line-height)",
    letterSpacing: "var(--mdui-typescale-body-large-tracking)"
});
globalStyle(".sc-body-medium", {
    fontSize: "var(--mdui-typescale-body-medium-size)",
    fontWeight: "var(--mdui-typescale-body-medium-weight)",
    lineHeight: "var(--mdui-typescale-body-medium-line-height)",
    letterSpacing: "var(--mdui-typescale-body-medium-tracking)"
});
globalStyle(".sc-body-small, .sc-hint", {
    fontSize: "var(--mdui-typescale-body-small-size)",
    fontWeight: "var(--mdui-typescale-body-small-weight)",
    lineHeight: "var(--mdui-typescale-body-small-line-height)",
    letterSpacing: "var(--mdui-typescale-body-small-tracking)"
});
globalStyle(".sc-label-large", {
    fontSize: "var(--mdui-typescale-label-large-size)",
    fontWeight: "var(--mdui-typescale-label-large-weight)",
    lineHeight: "var(--mdui-typescale-label-large-line-height)",
    letterSpacing: "var(--mdui-typescale-label-large-tracking)"
});
globalStyle(".sc-label-medium", {
    fontSize: "var(--mdui-typescale-label-medium-size)",
    fontWeight: "var(--mdui-typescale-label-medium-weight)",
    lineHeight: "var(--mdui-typescale-label-medium-line-height)",
    letterSpacing: "var(--mdui-typescale-label-medium-tracking)"
});
globalStyle(".sc-label-small", {
    fontSize: "var(--mdui-typescale-label-small-size)",
    fontWeight: "var(--mdui-typescale-label-small-weight)",
    lineHeight: "var(--mdui-typescale-label-small-line-height)",
    letterSpacing: "var(--mdui-typescale-label-small-tracking)"
});
globalStyle(".sc-page", {
    width: "min(100%, var(--sc-page-max))",
    marginInline: "auto",
    padding: "var(--sc-page-pad)",
    animation: "sc-fade-in-up 220ms cubic-bezier(0.2, 0, 0, 1)"
});
globalStyle(".sc-page-section, .sc-section", {
    marginBlock: "var(--sc-section-gap)"
});
globalStyle(".sc-page-card, .sc-card", {
    padding: "24px",
    border: "1px solid var(--sc-outline-variant)",
    borderRadius: "var(--sc-radius-card)",
    background: "var(--sc-container-surface)",
    boxShadow: "var(--sc-shadow-card)",
    transition: "transform 180ms cubic-bezier(0.2, 0, 0, 1),\n              box-shadow 180ms cubic-bezier(0.2, 0, 0, 1),\n              background-color 180ms cubic-bezier(0.2, 0, 0, 1)"
});
globalStyle(".sc-page-card:hover, .sc-card:hover", {
    background: "var(--sc-raised-surface)",
    boxShadow: "var(--sc-elevation-1)"
});
globalStyle(".sc-page-heading, .sc-section-heading", {
    marginBlockEnd: "8px"
});
globalStyle(".sc-page-hint, .sc-hint", {
    color: "var(--sc-content-secondary)"
});
globalStyle(".sc-focus-ring:focus-visible, .sc-focus-ring-within:has(:focus-visible)", {
    outline: "var(--sc-focus-ring-width) solid var(--sc-state-focus)",
    outlineOffset: "var(--sc-focus-ring-offset)"
});
globalStyle(".sc-touch-target", {
    position: "relative"
});
globalStyle(".sc-touch-target::before", {
    content: "''",
    position: "absolute",
    inset: "50% auto auto 50%",
    width: "max(100%, var(--sc-control-min-compact))",
    height: "max(100%, var(--sc-control-min-compact))",
    translate: "-50% -50%"
});
globalStyle(".sc-danger", {
    display: "inline-flex",
    alignItems: "center",
    verticalAlign: "middle",
    vars: {
        "--mdui-color-primary": "var(--mdui-color-error)",
        "--mdui-color-on-primary": "var(--mdui-color-on-error)",
        "--mdui-color-primary-container": "var(--mdui-color-error-container)",
        "--mdui-color-on-primary-container": "var(--mdui-color-on-error-container)",
        "--mdui-color-secondary": "var(--mdui-color-error)",
        "--mdui-color-on-secondary": "var(--mdui-color-on-error)",
        "--mdui-color-secondary-container": "var(--mdui-color-error-container)",
        "--mdui-color-on-secondary-container": "var(--mdui-color-on-error-container)",
        "--mdui-color-outline": "var(--mdui-color-error)",
        "--mdui-color-outline-variant": "var(--mdui-color-error)",
        "--mdui-color-on-surface-variant": "var(--mdui-color-error)"
    }
});
globalStyle(".sc-sr-only", {
    position: "absolute",
    width: "1px",
    height: "1px",
    padding: "0",
    margin: "-1px",
    overflow: "hidden",
    clip: "rect(0, 0, 0, 0)",
    whiteSpace: "nowrap",
    border: "0"
});
globalStyle("mdui-button,\n.sc-button-wrap", {
    transition: "transform 120ms cubic-bezier(0.2, 0, 0, 1),\n              box-shadow 150ms cubic-bezier(0.2, 0, 0, 1)"
});
globalStyle("mdui-button:active,\n.sc-button-wrap:active", {
    transform: "scale(0.97)"
});
globalStyle(".sc-icon-button", {
    display: "inline-flex",
    flex: "none",
    transition: "transform 120ms cubic-bezier(0.2, 0, 0, 1)"
});
globalStyle(".sc-icon-button:active", {
    transform: "scale(0.92)"
});
globalStyle(".sc-icon-button-tip", {
    position: "fixed",
    zIndex: "40",
    inset: "auto",
    margin: "0",
    border: "none",
    transform: "translateX(-50%)",
    maxWidth: "240px",
    padding: "4px 8px",
    borderRadius: "var(--mdui-shape-corner-extra-small)",
    background: "rgb(var(--mdui-color-inverse-surface))",
    color: "rgb(var(--mdui-color-inverse-on-surface))",
    fontSize: "var(--mdui-typescale-body-small-size)",
    fontWeight: "var(--mdui-typescale-body-small-weight)",
    letterSpacing: "var(--mdui-typescale-body-small-tracking)",
    lineHeight: "var(--mdui-typescale-body-small-line-height)",
    whiteSpace: "nowrap",
    overflow: "hidden",
    textOverflow: "ellipsis",
    pointerEvents: "none",
    opacity: "0",
    transition: "opacity 120ms ease, transform 120ms ease"
});
globalStyle(".sc-icon-button-tip--placed", {
    opacity: "1"
});
globalStyle(".sc-thumb-wrap", {
    position: "relative",
    width: "100%",
    height: "100%",
    overflow: "hidden"
});
globalStyle(".sc-thumb-img", {
    width: "100%",
    height: "100%",
    objectFit: "cover",
    display: "block"
});
globalStyle(".sc-thumb-badge", {
    position: "absolute",
    bottom: "8px",
    right: "8px",
    display: "inline-flex",
    alignItems: "center",
    justifyContent: "center",
    width: "24px",
    height: "24px",
    borderRadius: "var(--mdui-shape-corner-small, 4px)",
    background: "rgb(0 0 0 / 65%)",
    color: "#fff",
    pointerEvents: "none"
});
globalStyle(".sc-thumb-icon", {
    display: "flex",
    alignItems: "center",
    justifyContent: "center",
    width: "100%",
    height: "100%",
    color: "rgb(var(--mdui-color-on-surface-variant))"
});
globalStyle(".sc-loading-page, .sc-error-page", {
    minHeight: "100dvh",
    display: "grid",
    placeItems: "center",
    padding: "var(--sc-page-pad)",
    background: "var(--sc-page-surface)"
});
globalStyle(".sc-error-page-card", {
    width: "min(100%, 34rem)",
    padding: "24px",
    borderRadius: "var(--sc-radius-auth)",
    background: "var(--sc-raised-surface)",
    border: "1px solid var(--sc-outline-variant)",
    boxShadow: "var(--sc-elevation-1)",
    animation: "sc-scale-up 220ms cubic-bezier(0.2, 0, 0, 1)"
});
globalStyle("*, *::before, *::after", {
    "@media": {
        "(prefers-reduced-motion: reduce)": {
            scrollBehavior: "auto !important" as 'auto',
            animationDuration: ".01ms !important",
            animationIterationCount: "1 !important",
            transitionDuration: ".01ms !important",
            transform: "none !important"
        }
    }
});
