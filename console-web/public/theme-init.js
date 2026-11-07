// Same as Mantine ColorSchemeScript: set the scheme before first paint.
// A file, not an inline script, so the Content-Security-Policy needs no unsafe-inline.
try {
  var s = localStorage.getItem('mantine-color-scheme-value') || 'auto';
  if (s === 'auto') s = matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light';
  document.documentElement.setAttribute('data-mantine-color-scheme', s);
} catch {
  // storage blocked: Mantine falls back to its default scheme
}
