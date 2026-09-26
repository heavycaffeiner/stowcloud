import { globalStyle } from '@vanilla-extract/css';
globalStyle(".sc-app-shell", {
    width: "100%",
    minWidth: "0",
    height: ["100vh", "100dvh"],
    display: "flex",
    flexDirection: "column",
    overflow: "hidden",
    background: "var(--sc-page-surface)"
});
globalStyle(".sc-app-shell--compact", {
    flexDirection: "column",
    vars: { "--sc-control-min": "var(--sc-control-min-compact)" }
});
globalStyle(".sc-shell-header", {
    position: "sticky",
    top: "0",
    left: "0",
    right: "0",
    height: "var(--sc-shell-header-height, 56px)",
    minHeight: "var(--sc-shell-header-height, 56px)",
    zIndex: "30",
    display: "flex",
    alignItems: "center",
    justifyContent: "space-between",
    padding: "0 20px",
    background: "color-mix(in srgb, var(--sc-page-surface) 96%, transparent)",
    backdropFilter: "blur(12px)",
    borderBottom: "1px solid var(--sc-outline-variant)",
    color: "var(--sc-content-primary)",
    boxSizing: "border-box"
});
globalStyle(".sc-app-shell--compact .sc-shell-header", {
    paddingTop: "env(safe-area-inset-top, 0px)",
    paddingRight: "max(8px, env(safe-area-inset-right, 0px))",
    paddingLeft: "max(8px, env(safe-area-inset-left, 0px))",
    gap: "8px",
    height: "calc(var(--sc-shell-header-height, 56px) + env(safe-area-inset-top, 0px))"
});
globalStyle(".sc-app-shell--compact .sc-shell-header-left", {
    flex: "1 1 auto",
    minWidth: "0"
});
globalStyle(".sc-app-shell--compact .sc-shell-header-brand-btn", {
    minWidth: "0",
    flex: "1 1 auto"
});
globalStyle(".sc-app-shell--compact .sc-shell-header-brand", {
    display: "block",
    minWidth: "0",
    overflow: "hidden",
    textOverflow: "ellipsis",
    whiteSpace: "nowrap"
});
globalStyle(".sc-app-shell--compact .sc-shell-header-center", {
    flex: "0 0 44px",
    width: "44px",
    minWidth: "44px",
    margin: "0"
});
globalStyle(".sc-app-shell--compact .sc-shell-header-search", {
    width: "44px",
    height: "44px",
    minWidth: "44px",
    padding: "0",
    boxSizing: "border-box",
    justifyContent: "center"
});
globalStyle(".sc-app-shell--compact .sc-shell-header-search-placeholder,\n.sc-app-shell--compact .sc-shell-header-search-hints", {
    display: "none"
});
globalStyle(".sc-app-shell--compact .sc-shell-header-right", {
    flex: "none",
    minWidth: "0",
    gap: "0"
});
globalStyle(".sc-app-shell--compact .sc-shell-header-avatar-btn", {
    marginLeft: "0"
});
globalStyle(".sc-shell-header-left", {
    display: "flex",
    alignItems: "center",
    gap: "8px",
    flex: "none"
});
globalStyle(".sc-shell-header-menu-btn", {
    width: "var(--sc-control-min)",
    height: "var(--sc-control-min)",
    display: "inline-flex",
    alignItems: "center",
    justifyContent: "center",
    borderRadius: "50%",
    border: "none",
    background: "transparent",
    color: "var(--sc-content-primary)",
    cursor: "pointer",
    transition: "background-color 150ms ease"
});
globalStyle(".sc-shell-header-menu-btn:hover", {
    background: "color-mix(in srgb, var(--sc-content-primary) 8%, transparent)"
});
globalStyle(".sc-shell-header-brand-btn", {
    background: "none",
    border: "none",
    padding: "0 4px",
    cursor: "pointer",
    display: "flex",
    alignItems: "center",
    color: "inherit"
});
globalStyle(".sc-shell-header-brand", {
    fontSize: "var(--mdui-typescale-title-large-size)",
    lineHeight: "var(--mdui-typescale-title-large-line-height)",
    fontWeight: "700",
    color: "var(--sc-content-primary)",
    letterSpacing: "-0.02em"
});
globalStyle(".sc-shell-header-center", {
    flex: "1 1 auto",
    maxWidth: "600px",
    margin: "0 24px",
    display: "flex",
    justifyContent: "center"
});
globalStyle(".sc-shell-header-search", {
    width: "100%",
    height: "40px",
    display: "flex",
    alignItems: "center",
    gap: "10px",
    padding: "0 14px",
    border: "1px solid var(--sc-outline-variant)",
    borderRadius: "12px",
    background: "var(--sc-container-surface)",
    color: "var(--sc-content-secondary)",
    cursor: "pointer",
    textAlign: "left",
    transition: "background-color 150ms ease, border-color 150ms ease, box-shadow 150ms ease"
});
globalStyle(".sc-shell-header-search:hover", {
    background: "var(--sc-overlay-surface)",
    borderColor: "var(--sc-outline)"
});
globalStyle(".sc-shell-header-search-icon", {
    display: "inline-flex",
    alignItems: "center",
    color: "var(--sc-content-secondary)",
    flex: "none"
});
globalStyle(".sc-shell-header-search-placeholder", {
    flex: "1",
    minWidth: "0",
    overflow: "hidden",
    textOverflow: "ellipsis",
    whiteSpace: "nowrap",
    fontSize: "var(--mdui-typescale-label-large-size)",
    lineHeight: "var(--mdui-typescale-label-large-line-height, 1.25rem)",
    color: "var(--sc-content-secondary)"
});
globalStyle(".sc-shell-header-search-hints", {
    display: "flex",
    alignItems: "center",
    gap: "8px",
    flex: "none"
});
globalStyle(".sc-shell-header-shortcut", {
    fontFamily: "inherit",
    fontSize: "var(--mdui-typescale-label-medium-size)",
    lineHeight: "var(--mdui-typescale-label-medium-line-height)",
    padding: "1px 7px",
    borderRadius: "4px",
    background: "var(--sc-overlay-surface)",
    border: "1px solid var(--sc-outline-variant)",
    color: "var(--sc-content-secondary)"
});
globalStyle(".sc-shell-header-filter-icon", {
    display: "inline-flex",
    alignItems: "center",
    color: "var(--sc-content-secondary)"
});
globalStyle(".sc-shell-header-right", {
    display: "flex",
    alignItems: "center",
    gap: "6px",
    flex: "none"
});
globalStyle(".sc-shell-header-icon-btn", {
    width: "var(--sc-control-min)",
    height: "var(--sc-control-min)",
    display: "inline-flex",
    alignItems: "center",
    justifyContent: "center",
    borderRadius: "50%",
    border: "none",
    background: "transparent",
    color: "var(--sc-content-primary)",
    cursor: "pointer",
    transition: "background-color 150ms ease"
});
globalStyle(".sc-shell-header-icon-btn:hover", {
    background: "color-mix(in srgb, var(--sc-content-primary) 8%, transparent)"
});
globalStyle(".sc-shell-header-avatar-btn", {
    width: "44px",
    height: "44px",
    display: "inline-flex",
    alignItems: "center",
    justifyContent: "center",
    border: "none",
    background: "none",
    padding: "0",
    cursor: "pointer",
    borderRadius: "50%",
    marginLeft: "4px"
});
globalStyle(".sc-shell-header-avatar", {
    width: "32px",
    height: "32px",
    borderRadius: "50%",
    background: "var(--sc-state-selection)",
    color: "var(--sc-state-selection-content)",
    fontSize: "var(--mdui-typescale-label-medium-size)",
    lineHeight: "var(--mdui-typescale-label-medium-line-height)",
    fontWeight: "600",
    display: "inline-flex",
    alignItems: "center",
    justifyContent: "center",
    border: "1px solid var(--sc-outline-variant)",
    transition: "transform 120ms ease"
});
globalStyle(".sc-shell-header-avatar-btn:hover .sc-shell-header-avatar", {
    transform: "scale(1.05)"
});
globalStyle(".sc-shell-header-account-wrap", {
    position: "relative",
    display: "flex",
    alignItems: "center"
});
globalStyle(".sc-shell-header-account-menu", {
    position: "absolute",
    top: "calc(100% + 8px)",
    right: "0",
    zIndex: "60",
    minWidth: "200px",
    padding: "6px",
    display: "flex",
    flexDirection: "column",
    gap: "2px",
    border: "1px solid var(--sc-outline-variant)",
    borderRadius: "12px",
    background: "var(--sc-overlay-surface)",
    boxShadow: "0 12px 32px rgba(0, 0, 0, 0.28)"
});
globalStyle(".sc-shell-header-account-name", {
    padding: "8px 10px 6px",
    fontSize: "var(--mdui-typescale-label-large-size)",
    lineHeight: "var(--mdui-typescale-label-large-line-height)",
    fontWeight: "600",
    color: "var(--sc-content-primary)",
    overflow: "hidden",
    textOverflow: "ellipsis",
    whiteSpace: "nowrap"
});
globalStyle(".sc-shell-header-account-menu button", {
    display: "flex",
    alignItems: "center",
    gap: "10px",
    minHeight: "var(--sc-control-min)",
    padding: "0 10px",
    border: "none",
    borderRadius: "8px",
    background: "transparent",
    color: "var(--sc-content-primary)",
    font: "inherit",
    fontSize: "var(--mdui-typescale-label-large-size)",
    lineHeight: "var(--mdui-typescale-label-large-line-height, 1.25rem)",
    textAlign: "left",
    cursor: "pointer",
    transition: "background-color 120ms ease"
});
globalStyle(".sc-shell-header-account-menu button:hover", {
    background: "color-mix(in srgb, var(--sc-content-primary) 8%, transparent)"
});
globalStyle(".sc-shell-body", {
    flex: "1",
    minHeight: "0",
    display: "flex",
    overflow: "hidden",
    position: "relative"
});
globalStyle(".sc-app-shell-main", {
    flex: "1",
    minWidth: "0",
    display: "flex",
    flexDirection: "column",
    overflowY: "auto",
    overflowX: "hidden",
    background: "var(--sc-page-surface)",
    transition: "padding-left 200ms cubic-bezier(0.2, 0, 0, 1)"
});
globalStyle(".sc-app-shell-main--drawer", {
    paddingLeft: "var(--sc-nav-drawer-width, 240px)"
});
globalStyle(".sc-app-shell-main--collapsed", {
    paddingLeft: "var(--sc-nav-drawer-collapsed-width, 68px)"
});
globalStyle(".sc-app-shell--compact .sc-app-shell-main", {
    paddingLeft: "0",
    paddingBottom: "calc(var(--sc-nav-bar-height) + env(safe-area-inset-bottom, 0px))"
});
globalStyle(".sc-nav-drawer", {
    position: "fixed",
    zIndex: "10",
    top: "var(--sc-shell-header-height, 56px)",
    bottom: "0",
    left: "0",
    width: "var(--sc-nav-drawer-width, 240px)",
    display: "flex",
    flexDirection: "column",
    overflow: "hidden",
    background: "color-mix(in srgb, var(--sc-container-surface) 78%, var(--sc-page-surface))",
    borderRight: "1px solid var(--sc-outline-variant)",
    color: "var(--sc-content-primary)",
    transition: "width 200ms cubic-bezier(0.2, 0, 0, 1), transform 220ms cubic-bezier(0.2, 0, 0, 1)"
});
globalStyle(".sc-nav-drawer--collapsed", {
    width: "var(--sc-nav-drawer-collapsed-width, 68px)"
});
globalStyle(".sc-nav-drawer-body", {
    minHeight: "0",
    flex: "1",
    display: "flex",
    flexDirection: "column",
    overflowY: "auto",
    overflowX: "hidden",
    overscrollBehavior: "contain",
    scrollbarWidth: "thin",
    scrollbarColor: "color-mix(in srgb, var(--sc-content-secondary) 35%, transparent) transparent"
});
globalStyle(".sc-nav-drawer-body::-webkit-scrollbar", {
    width: "6px"
});
globalStyle(".sc-nav-drawer-body::-webkit-scrollbar-track", {
    background: "transparent"
});
globalStyle(".sc-nav-drawer-body::-webkit-scrollbar-thumb", {
    borderRadius: "3px",
    background: "color-mix(in srgb, var(--sc-content-secondary) 30%, transparent)"
});
globalStyle(".sc-nav-drawer-body::-webkit-scrollbar-thumb:hover", {
    background: "color-mix(in srgb, var(--sc-content-secondary) 50%, transparent)"
});
globalStyle(".sc-nav-drawer-new-wrap", {
    padding: "16px 12px 12px",
    flex: "none",
    order: "-2"
});
globalStyle(".sc-nav-drawer-roots-section", {
    order: "-1"
});
globalStyle(".sc-nav-drawer-new-btn", {
    width: "100%",
    height: "44px",
    borderRadius: "12px",
    background: "var(--sc-raised-surface)",
    border: "1px solid var(--sc-outline-variant)",
    color: "var(--sc-content-primary)",
    display: "flex",
    alignItems: "center",
    justifyContent: "center",
    gap: "14px",
    padding: "0 13px",
    cursor: "pointer",
    fontSize: "var(--mdui-typescale-label-large-size)",
    lineHeight: "var(--mdui-typescale-label-large-line-height, 1.25rem)",
    fontWeight: "500",
    textAlign: "center",
    transition: "background-color 150ms ease, transform 100ms ease"
});
globalStyle(".sc-nav-drawer-new-btn:hover", {
    background: "var(--sc-overlay-surface)"
});
globalStyle(".sc-nav-drawer-new-btn:active", {
    transform: "scale(0.98)"
});
globalStyle(".sc-nav-drawer-new-btn--collapsed", {
    width: "44px",
    height: "44px",
    borderRadius: "12px",
    padding: "0",
    justifyContent: "center",
    margin: "0 auto"
});
globalStyle(".sc-nav-drawer-section-title", {
    margin: "12px 16px 6px 16px",
    color: "var(--sc-content-secondary)",
    fontSize: "var(--mdui-typescale-label-medium-size)",
    lineHeight: "var(--mdui-typescale-label-medium-line-height)",
    fontWeight: "600",
    letterSpacing: "0.04em",
    textTransform: "uppercase"
});
globalStyle(".sc-nav-drawer-section-header", {
    display: "flex",
    alignItems: "center",
    justifyContent: "space-between",
    padding: "8px 16px 4px",
    minHeight: "32px"
});
globalStyle(".sc-nav-drawer-section-header .sc-nav-drawer-section-title", {
    margin: "0"
});
globalStyle(".sc-nav-drawer-list,\n.sc-nav-drawer-sublist", {
    listStyle: "none",
    margin: "0",
    display: "flex",
    flexDirection: "column",
    gap: "2px"
});
globalStyle(".sc-nav-drawer-list", {
    padding: "0 10px"
});
globalStyle(".sc-nav-drawer-sublist", {
    padding: "0"
});
globalStyle(".sc-nav-drawer-entry", {
    margin: "0"
});
globalStyle(".sc-nav-drawer-item", {
    width: "100%",
    minHeight: "var(--sc-control-min)",
    padding: "0 14px",
    display: "flex",
    alignItems: "center",
    gap: "14px",
    border: "0",
    borderRadius: "10px",
    background: "transparent",
    color: "var(--sc-content-secondary)",
    cursor: "pointer",
    font: "inherit",
    fontSize: "var(--mdui-typescale-label-large-size)",
    lineHeight: "var(--mdui-typescale-label-large-line-height, 1.25rem)",
    fontWeight: "500",
    textAlign: "left",
    transition: "background-color 140ms ease, color 140ms ease"
});
globalStyle(".sc-nav-drawer-item:hover", {
    background: "color-mix(in srgb, var(--sc-content-primary) 6%, transparent)",
    color: "var(--sc-content-primary)"
});
globalStyle(".sc-nav-drawer-item--active", {
    background: "var(--sc-state-selection)",
    color: "var(--sc-state-selection-content)",
    fontWeight: "600"
});
globalStyle(".sc-nav-drawer-item--active:hover", {
    background: "color-mix(in srgb, var(--sc-state-selection) 88%, var(--sc-content-primary))",
    color: "var(--sc-state-selection-content)"
});
globalStyle(".sc-nav-drawer-item-icon", {
    display: "inline-flex",
    alignItems: "center",
    justifyContent: "center",
    flex: "none"
});
globalStyle(".sc-nav-drawer-item--active .sc-nav-drawer-item-icon", {
    color: "var(--sc-state-selection-content)"
});
globalStyle(".sc-nav-drawer-item-label", {
    minWidth: "0",
    flex: "1",
    overflow: "hidden",
    textOverflow: "ellipsis",
    whiteSpace: "nowrap"
});
globalStyle(".sc-nav-drawer--collapsed .sc-nav-drawer-list", {
    padding: "8px 0"
});
globalStyle(".sc-nav-drawer--collapsed .sc-nav-drawer-body", {
    paddingTop: "12px"
});
globalStyle(".sc-nav-drawer--collapsed .sc-nav-drawer-item", {
    width: "44px",
    height: "44px",
    minHeight: "44px",
    padding: "0",
    justifyContent: "center",
    borderRadius: "12px",
    margin: "2px auto"
});
globalStyle(".sc-nav-drawer-divider", {
    height: "1px",
    background: "var(--sc-outline-variant)",
    margin: "10px 12px"
});
globalStyle(".sc-nav-drawer-subitem", {
    width: "100%",
    minHeight: "40px",
    padding: "0 14px",
    display: "flex",
    alignItems: "center",
    gap: "14px",
    border: "0",
    borderRadius: "10px",
    background: "transparent",
    color: "var(--sc-content-secondary)",
    cursor: "pointer",
    font: "inherit",
    fontSize: "var(--mdui-typescale-label-large-size)",
    lineHeight: "var(--mdui-typescale-label-large-line-height, 1.25rem)",
    textAlign: "left",
    transition: "background-color 140ms ease, color 140ms ease"
});
globalStyle(".sc-nav-drawer-subitem:hover", {
    background: "color-mix(in srgb, var(--sc-content-primary) 6%, transparent)",
    color: "var(--sc-content-primary)"
});
globalStyle(".sc-nav-drawer-subitem--active", {
    background: "var(--sc-state-selection)",
    color: "var(--sc-state-selection-content)",
    fontWeight: "600"
});
globalStyle(".sc-nav-drawer-subitem--active:hover", {
    background: "color-mix(in srgb, var(--sc-state-selection) 88%, var(--sc-content-primary))",
    color: "var(--sc-state-selection-content)"
});
globalStyle(".sc-nav-drawer-subitem--active .sc-nav-drawer-item-icon", {
    color: "var(--sc-state-selection-content)"
});
globalStyle(".sc-nav-drawer-subitem-label", {
    minWidth: "0",
    flex: "1",
    overflow: "hidden",
    textOverflow: "ellipsis",
    whiteSpace: "nowrap"
});
globalStyle(".sc-nav-drawer-reorder-toggle", {
    minHeight: "var(--sc-control-min)",
    display: "inline-flex",
    alignItems: "center",
    background: "transparent",
    border: "none",
    color: "rgb(var(--mdui-color-primary))",
    fontSize: "var(--mdui-typescale-label-large-size)",
    lineHeight: "var(--mdui-typescale-label-large-line-height)",
    cursor: "pointer",
    padding: "0 8px",
    borderRadius: "8px"
});
globalStyle(".sc-nav-drawer-subitem--reorder", {
    display: "flex",
    alignItems: "center",
    gap: "8px",
    minHeight: "48px",
    padding: "2px 12px",
    fontSize: "var(--mdui-typescale-body-medium-size)",
    lineHeight: "var(--mdui-typescale-body-medium-line-height)"
});
globalStyle(".sc-nav-drawer-reorder-actions", {
    display: "flex",
    alignItems: "center",
    gap: "2px",
    marginLeft: "auto"
});
globalStyle(".sc-nav-drawer-reorder-chevron", {
    display: "flex",
    alignItems: "center",
    justifyContent: "center"
});
globalStyle(".sc-nav-drawer-reorder-chevron--up", {
    rotate: "-90deg"
});
globalStyle(".sc-nav-drawer-reorder-chevron--down", {
    rotate: "90deg"
});
globalStyle(".sc-nav-drawer-reorder-error", {
    margin: "0 12px",
    padding: "4px 12px",
    color: "var(--sc-state-error-content)",
    fontSize: "var(--mdui-typescale-body-small-size)",
    lineHeight: "var(--mdui-typescale-body-small-line-height)"
});
globalStyle(".sc-nav-drawer--overlay", {
    position: "fixed",
    inset: "0 auto 0 0",
    top: "0",
    bottom: "0",
    width: "min(300px, 85vw)",
    maxWidth: "none",
    height: ["100vh", "100dvh"],
    minHeight: "100dvh",
    margin: "0",
    padding: "0",
    border: "none",
    borderRadius: "0 16px 16px 0",
    background: "var(--sc-container-surface)",
    boxShadow: "4px 0 24px rgba(0, 0, 0, 0.35)",
    zIndex: "60",
    display: "flex",
    flexDirection: "column",
    overflow: "hidden"
});
globalStyle(".sc-nav-drawer--overlay::backdrop", {
    background: "rgba(var(--mdui-color-scrim), 0.5)",
    backdropFilter: "blur(4px)"
});
globalStyle(".sc-nav-drawer-overlay-header", {
    height: "56px",
    display: "flex",
    alignItems: "center",
    justifyContent: "space-between",
    padding: "0 16px",
    borderBottom: "1px solid var(--sc-outline-variant)",
    flex: "none"
});
globalStyle(".sc-nav-drawer-app-name", {
    fontSize: "var(--mdui-typescale-title-medium-size)",
    lineHeight: "var(--mdui-typescale-title-medium-line-height)",
    fontWeight: "700",
    color: "var(--sc-content-primary)"
});
globalStyle(".sc-nav-drawer-user-avatar", {
    width: "32px",
    height: "32px",
    borderRadius: "50%",
    background: "var(--sc-state-selection)",
    color: "var(--sc-state-selection-content)",
    fontSize: "var(--mdui-typescale-label-medium-size)",
    lineHeight: "var(--mdui-typescale-label-medium-line-height)",
    fontWeight: "600",
    display: "inline-flex",
    alignItems: "center",
    justifyContent: "center",
    border: "1px solid var(--sc-outline-variant)"
});
globalStyle(".sc-nav-drawer-overlay-close", {
    width: "44px",
    height: "44px",
    display: "inline-flex",
    alignItems: "center",
    justifyContent: "center",
    borderRadius: "50%",
    border: "none",
    background: "transparent",
    color: "var(--sc-content-primary)",
    cursor: "pointer"
});
globalStyle(".sc-nav-bar", {
    position: "fixed",
    zIndex: "40",
    inset: "auto 0 0",
    height: "calc(var(--sc-nav-bar-height) + env(safe-area-inset-bottom, 0px))",
    boxSizing: "border-box",
    display: "flex",
    alignItems: "stretch",
    justifyContent: "center",
    gap: "4px",
    padding: "4px max(8px, env(safe-area-inset-right, 0px)) calc(4px + env(safe-area-inset-bottom, 0px)) max(8px, env(safe-area-inset-left, 0px))",
    background: "var(--sc-container-surface)",
    borderTop: "1px solid var(--sc-outline-variant)",
    boxShadow: "0 -4px 16px rgba(0, 0, 0, 0.12)",
    userSelect: "none"
});
globalStyle(".sc-nav-bar-item", {
    flex: "1 1 0",
    minWidth: "0",
    maxWidth: "7rem",
    minHeight: "48px",
    display: "flex",
    flexDirection: "column",
    alignItems: "center",
    justifyContent: "center",
    gap: "2px",
    padding: "2px 2px 4px",
    border: "0",
    borderRadius: "16px",
    background: "transparent",
    color: "var(--sc-content-secondary)",
    cursor: "pointer",
    font: "inherit",
    fontSize: "var(--mdui-typescale-label-medium-size, 0.75rem)",
    lineHeight: "var(--mdui-typescale-label-medium-line-height, 1rem)",
    fontWeight: "var(--mdui-typescale-label-medium-weight, 500)",
    outline: "none",
    transition: "background-color 150ms ease, color 150ms ease"
});
globalStyle(".sc-nav-bar-icon", {
    width: "64px",
    height: "32px",
    display: "grid",
    placeItems: "center",
    flex: "none",
    boxSizing: "border-box",
    borderRadius: "16px",
    transition: "background-color 150ms ease"
});
globalStyle(".sc-nav-bar-label", {
    width: "100%",
    minWidth: "0",
    height: "1rem",
    flex: "0 0 1rem",
    overflow: "hidden",
    textAlign: "center",
    textOverflow: "ellipsis",
    whiteSpace: "nowrap"
});
globalStyle(".sc-nav-bar-item.is-active", {
    color: "var(--sc-state-selection-content)",
    fontWeight: "600"
});
globalStyle(".sc-nav-bar-item.is-active .sc-nav-bar-icon", {
    background: "var(--sc-state-selection)"
});
globalStyle(".sc-tray-stack", {
    position: "fixed",
    right: "24px",
    bottom: "24px",
    zIndex: "30",
    display: "flex",
    flexDirection: "column",
    alignItems: "flex-end",
    gap: "12px"
});
globalStyle(".sc-tray-stack--compact", {
    right: "max(16px, env(safe-area-inset-right, 0px))",
    bottom: "calc(24px + var(--sc-nav-bar-height) + env(safe-area-inset-bottom, 0px))"
});
globalStyle(".sc-app-shell--compact ~ mdui-snackbar[open]", {
    bottom: "max(\n    calc(16px + var(--sc-nav-bar-height) + env(safe-area-inset-bottom, 0px)),\n    var(--sc-tray-stack-top, 0px)\n  ) !important"
});
globalStyle(".sc-upload-tray,\n.sc-job-tray", {
    width: "min(360px, calc(100vw - 32px))",
    maxHeight: "60vh",
    display: "flex",
    flexDirection: "column",
    overflow: "hidden",
    borderRadius: "16px",
    background: "var(--sc-overlay-surface)",
    color: "var(--sc-content-primary)",
    boxShadow: "0 8px 24px rgba(0, 0, 0, 0.24)",
    border: "1px solid var(--sc-outline-variant)"
});
globalStyle(".sc-upload-tray-sr-only,\n.sc-job-tray-sr-only", {
    position: "absolute",
    width: "1px",
    height: "1px",
    overflow: "hidden",
    clip: "rect(0 0 0 0)"
});
globalStyle(".sc-upload-tray-header,\n.sc-job-tray-header", {
    minHeight: "48px",
    display: "flex",
    alignItems: "center",
    justifyContent: "space-between",
    paddingInline: "16px"
});
globalStyle(".sc-upload-tray-title,\n.sc-job-tray-title", {
    display: "inline-flex",
    alignItems: "center",
    gap: "8px",
    border: "0",
    background: "transparent",
    color: "inherit",
    cursor: "pointer",
    font: "inherit",
    fontSize: "var(--mdui-typescale-label-large-size)",
    fontWeight: "var(--mdui-typescale-label-large-weight, 500)",
    lineHeight: "var(--mdui-typescale-label-large-line-height, 1.25rem)"
});
globalStyle(".sc-upload-tray-actions,\n.sc-job-tray-actions,\n.sc-upload-tray-controls,\n.sc-job-tray-controls", {
    display: "flex",
    alignItems: "center",
    gap: "4px"
});
globalStyle(".sc-upload-tray-list,\n.sc-job-tray-list", {
    listStyle: "none",
    margin: "0",
    padding: "8px 16px"
});
globalStyle(".sc-upload-tray-scroll,\n.sc-job-tray-scroll", {
    minHeight: "0",
    overflowY: "auto"
});
globalStyle(".sc-upload-tray-item,\n.sc-job-tray-item", {
    paddingBlock: "10px",
    borderBottom: "1px solid var(--sc-outline-variant)"
});
globalStyle(".sc-upload-tray-row,\n.sc-job-tray-row", {
    display: "flex",
    justifyContent: "space-between",
    gap: "12px",
    marginBottom: "4px"
});
globalStyle(".sc-upload-tray-name,\n.sc-job-tray-name", {
    minWidth: "0",
    display: "inline-flex",
    alignItems: "center",
    gap: "4px",
    overflowWrap: "anywhere",
    fontSize: "var(--mdui-typescale-body-medium-size)",
    lineHeight: "var(--mdui-typescale-body-medium-line-height, 1.25rem)"
});
globalStyle(".sc-upload-tray-meta,\n.sc-job-tray-meta", {
    flexShrink: "0",
    color: "var(--sc-content-secondary)",
    fontSize: "var(--mdui-typescale-body-small-size)",
    lineHeight: "var(--mdui-typescale-body-small-line-height)"
});
globalStyle(".sc-search-sheet", {
    inset: "48px 0 auto",
    width: "min(720px, calc(100% - 32px))",
    maxHeight: "calc(100dvh - 80px)",
    margin: "0 auto",
    padding: "0",
    border: "1px solid var(--sc-outline-variant)",
    borderRadius: "16px",
    background: "var(--sc-overlay-surface)",
    color: "var(--sc-content-primary)",
    boxShadow: "0 16px 48px rgba(0, 0, 0, 0.32)",
    overflow: "visible"
});
globalStyle(".sc-search-sheet::backdrop", {
    background: "rgba(var(--mdui-color-scrim), 0.5)",
    backdropFilter: "blur(6px)"
});
globalStyle(".sc-search-sheet-body", {
    maxHeight: "calc(100dvh - 80px)",
    display: "flex",
    flexDirection: "column",
    padding: "16px",
    boxSizing: "border-box",
    overflow: "visible"
});
globalStyle(".sc-search-sheet", {
    "@media": {
        "(max-height: 400px)": {
            inset: "8px 0 auto",
            maxHeight: "calc(100dvh - 16px)"
        }
    }
});
globalStyle(".sc-search-sheet-body", {
    "@media": {
        "(max-height: 400px)": {
            maxHeight: "calc(100dvh - 16px)"
        }
    }
});
globalStyle(".sc-app-shell-boot", {
    minHeight: ["100vh", "100dvh"],
    display: "grid",
    placeItems: "center"
});
globalStyle(".sc-shell-header button:focus-visible,\n.sc-nav-drawer button:focus-visible,\n.sc-nav-bar button:focus-visible,\n.sc-search-sheet button:focus-visible", {
    outline: "var(--sc-focus-ring-width) solid var(--sc-state-focus)",
    outlineOffset: "var(--sc-focus-ring-offset)"
});
globalStyle(".sc-shell-header-search:active,\n.sc-nav-drawer-item:active,\n.sc-nav-drawer-subitem:active", {
    transform: "scale(0.99)"
});
