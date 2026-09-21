// This package has no renderer of its own.
// Electron loads the web app served by packages/web instead.
Object.assign(globalThis, { __desktopShellHasNoRenderer: true });
