globalThis.addEventListener('DOMContentLoaded', () => {
  document.querySelectorAll('.makibishi').forEach((x) => {
    x.setAttribute("data-url", location.href)
  })
}, false)
