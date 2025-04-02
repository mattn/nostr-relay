globalThis.addEventListener('DOMContentLoaded', () => {
  const u = new URL(location.href)
  const e = document.querySelector('#makibishi')
  e.setAttribute('data-content', '🤙')
  e.setAttribute('data-relays', 'wss://relay.nostr.band,wss://nos.lol,wss://relay.damus.io,wss://yabu.me,wss://cagliostr.compile-error.net,wss://nostr.compile-error.net')
  e.setAttribute('data-allow-anonymous-reaction', true)
  e.setAttribute('data-url', u.protocol + '//' + u.host + u.pathname.replace(/\/$/, ''))
  globalThis.makibishi.initTarget(e)
}, false)
