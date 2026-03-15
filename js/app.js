import { createApp, ref, nextTick } from 'vue'
import { useToast } from './composables/useToast.js'
import { useIframePanel } from './composables/useIframePanel.js'
import { useApi } from './composables/useApi.js'
import { useWebSocket } from './composables/useWebSocket.js'
import AppLayout from './components/AppLayout.js'

const app = createApp(AppLayout)

// State
const instances = ref([])
const clusters = ref([])
const globalStats = ref({ total: 0, running: 0, stopped: 0 })
const pendingActions = ref({})
const clusterIdleTimeout = ref(8 * 60 * 60 * 1000) // default 8h, updated from server
const currentPage = ref('workspaces')

// Toast
const { toasts, toast } = useToast()

// Iframe Panel
const iframePanel = useIframePanel()

// API
const api = useApi()

// WebSocket
const { wsConnected, init: initWs, poll } = useWebSocket((data) => {
  instances.value = data.instances
  globalStats.value = data.global
  clusters.value = data.clusters || []
  if (data.clusterIdleTimeout) clusterIdleTimeout.value = data.clusterIdleTimeout
  // Clean up stale pending actions
  const ids = new Set(instances.value.map(i => i.id))
  const copy = { ...pendingActions.value }
  let changed = false
  for (const id in copy) {
    if (!id.startsWith('cluster-') && !ids.has(id)) {
      delete copy[id]
      changed = true
    }
  }
  if (changed) pendingActions.value = copy
  nextTick(() => lucide.createIcons())
})

// Navigation
function navigate(page) {
  currentPage.value = page
  window.location.hash = page
  nextTick(() => lucide.createIcons())
}

// Hash routing
const hash = window.location.hash.replace('#', '')
if (['workspaces', 'clusters', 'settings'].includes(hash)) {
  currentPage.value = hash
}
window.addEventListener('hashchange', () => {
  const h = window.location.hash.replace('#', '')
  if (['workspaces', 'clusters', 'settings'].includes(h)) {
    currentPage.value = h
  }
})

// Provide
app.provide('instances', instances)
app.provide('clusters', clusters)
app.provide('globalStats', globalStats)
app.provide('pendingActions', pendingActions)
app.provide('currentPage', currentPage)
app.provide('navigate', navigate)
app.provide('api', api)
app.provide('toast', { toasts, toast })
app.provide('iframePanel', iframePanel)
app.provide('wsConnected', wsConnected)
app.provide('clusterIdleTimeout', clusterIdleTimeout)

app.mount('#app')
initWs()
