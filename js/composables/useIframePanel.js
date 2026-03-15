import { ref, computed } from 'vue'

const tabs = ref([])
const activeTabId = ref(null)
const expanded = ref(false)
const panelHeight = ref(85) // vh units
let nextId = 1

export function useIframePanel() {
  const hasAnyTabs = computed(() => tabs.value.length > 0)
  const activeTab = computed(() => tabs.value.find(t => t.id === activeTabId.value) || null)

  function openPanel(url, title) {
    const existing = tabs.value.find(t => t.url === url)
    if (existing) {
      activeTabId.value = existing.id
      expanded.value = true
      return
    }
    const id = nextId++
    tabs.value.push({ id, url, title: title || url })
    activeTabId.value = id
    expanded.value = true
  }

  function closeTab(id) {
    const idx = tabs.value.findIndex(t => t.id === id)
    if (idx === -1) return
    tabs.value.splice(idx, 1)
    if (activeTabId.value === id) {
      if (tabs.value.length > 0) {
        const newIdx = Math.min(idx, tabs.value.length - 1)
        activeTabId.value = tabs.value[newIdx].id
      } else {
        activeTabId.value = null
        expanded.value = false
      }
    }
  }

  function selectTab(id) {
    if (activeTabId.value === id && expanded.value) {
      expanded.value = false
    } else {
      activeTabId.value = id
      expanded.value = true
    }
  }

  function minimize() {
    expanded.value = false
  }

  function openInNewTab() {
    if (activeTab.value) {
      window.open(activeTab.value.url, '_blank')
    }
  }

  // Keep backward compat: panelOpen is true when expanded with tabs
  const panelOpen = computed(() => expanded.value && hasAnyTabs.value)

  return {
    tabs,
    activeTabId,
    expanded,
    panelHeight,
    hasAnyTabs,
    activeTab,
    panelOpen,
    openPanel,
    closeTab,
    selectTab,
    minimize,
    openInNewTab,
  }
}
