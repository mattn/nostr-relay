globalThis.addEventListener('DOMContentLoaded', () => {
  const u = new URL(location.href)
  const relayName = u.protocol.replace(/^http/, 'ws') + '//' + u.host + u.pathname.replace(/\/$/, '')
  document.querySelector('#relay-name').textContent = relayName
  const m = document.querySelector('#makibishi')
  m.setAttribute('data-content', '🤙')
  m.setAttribute('data-relays', 'wss://relay.nostr.band,wss://nos.lol,wss://relay.damus.io,wss://yabu.me,wss://cagliostr.compile-error.net,wss://nostr.compile-error.net')
  m.setAttribute('data-allow-anonymous-reaction', true)
  m.setAttribute('data-url', relayName)
  globalThis.makibishi.initTarget(m)
}, false)
