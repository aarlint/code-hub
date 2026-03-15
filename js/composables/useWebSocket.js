import { ref } from 'vue'

export function useWebSocket(onData) {
  const wsConnected = ref(false)
  let ws = null
  let retryDelay = 1000
  let pollTimer = null

  function connect() {
    const proto = location.protocol === 'https:' ? 'wss:' : 'ws:'
    ws = new WebSocket(`${proto}//${location.host}/api/ws`)

    ws.onopen = () => {
      console.log('ws connected')
      wsConnected.value = true
      retryDelay = 1000
      stopPolling()
    }

    ws.onmessage = (e) => {
      try {
        onData(JSON.parse(e.data))
      } catch (err) {
        console.error('ws parse error:', err)
      }
    }

    ws.onclose = () => {
      console.log('ws disconnected, falling back to polling')
      wsConnected.value = false
      ws = null
      startPolling()
      setTimeout(connect, retryDelay)
      retryDelay = Math.min(retryDelay * 2, 30000)
    }

    ws.onerror = () => {
      ws.close()
    }
  }

  async function poll() {
    try {
      const res = await fetch('/api/instances')
      if (!res.ok) throw new Error('Failed to fetch')
      onData(await res.json())
    } catch (e) {
      console.error('poll error:', e)
    }
  }

  function startPolling() {
    if (pollTimer) return
    pollTimer = setInterval(poll, 10000)
  }

  function stopPolling() {
    if (pollTimer) {
      clearInterval(pollTimer)
      pollTimer = null
    }
  }

  function init() {
    poll()
    startPolling()
    connect()
  }

  return { wsConnected, init, poll }
}
