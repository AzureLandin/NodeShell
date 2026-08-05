import './theme.css'
import './App.css'
import './i18n'
import './api'

import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import App from './App'

// theme-boot.js (index.html) already set data-theme from prefers-color-scheme;
// only fall back if the boot script did not run (e.g. tests).
if (!document.documentElement.dataset.theme) {
  document.documentElement.dataset.theme = 'dark'
}

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <App />
  </StrictMode>
)
