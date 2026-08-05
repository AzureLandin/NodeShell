/**
 * Runs before the React bundle. Aligns data-theme with prefers-color-scheme
 * so the first paint is not stuck on the dark default when the OS is light
 * (and avoids a white flash when CSS variables are not yet defined).
 */
;(function () {
  try {
    var dark = window.matchMedia('(prefers-color-scheme: dark)').matches
    var theme = dark ? 'dark' : 'light'
    var root = document.documentElement
    root.dataset.theme = theme
    root.style.colorScheme = theme
    root.style.background = dark ? '#121212' : '#eef1f5'
  } catch (_) {
    /* keep index.html dark defaults */
  }
})()
