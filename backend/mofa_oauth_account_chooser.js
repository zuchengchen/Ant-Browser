async ([address, email]) => {
  const onChooser = () => window === window.top && location.href === address &&
    location.origin === 'https://auth.openai.com' && location.pathname === '/choose-an-account'
  if (!onChooser()) return false

  const rendered = element => {
    if (element.closest('[hidden], [aria-hidden="true"], [inert]') ||
        !element.checkVisibility({checkOpacity: true, checkVisibilityCSS: true})) return false
    const {width, height} = element.getBoundingClientRect()
    return width > 2 && height > 2
  }

  let manual = false
  const startManual = event => { if (event.isTrusted) manual = true }
  window.addEventListener('pointerdown', startManual, true)
  window.addEventListener('keydown', startManual, true)
  try {
    let previous
    for (let attempt = 0; attempt < 48; attempt++) {
      if (manual || !onChooser()) return false
      if (document.readyState !== 'loading') {
        const matches = Array.from(document.querySelectorAll('li button[name="session_id"]')).filter(button =>
          rendered(button) && Array.from(button.querySelectorAll('span')).some(label =>
            !label.childElementCount && label.textContent.trim().toLowerCase() === email && rendered(label)))
        if (matches.length > 1) return false
        const button = matches[0]
        if (button && button === previous && !button.matches(':disabled') &&
            !button.closest('[aria-disabled="true"]')) {
          const {x, y, width, height} = button.getBoundingClientRect()
          const hit = document.elementFromPoint(x + width / 2, y + height / 2)
          if (hit && button.contains(hit)) {
            button.click()
            return true
          }
        }
        previous = button
      }
      await new Promise(resolve => setTimeout(resolve, 250))
    }
    return false
  } finally {
    window.removeEventListener('pointerdown', startManual, true)
    window.removeEventListener('keydown', startManual, true)
  }
}
