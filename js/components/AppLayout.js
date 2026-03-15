import { defineComponent, h, inject, ref, onMounted, onUnmounted, nextTick } from 'vue'
import WorkspacesPage from '../pages/WorkspacesPage.js'
import ClustersPage from '../pages/ClustersPage.js'
import SettingsPage from '../pages/SettingsPage.js'
import AppToast from './AppToast.js'
import IframePanel from './IframePanel.js'

const NAV_ITEMS = [
  { key: 'workspaces', icon: 'monitor', label: 'Workspaces' },
  { key: 'clusters', icon: 'server', label: 'Clusters' },
  { key: 'settings', icon: 'settings', label: 'Settings' },
]

const PAGE_COMPONENTS = {
  workspaces: WorkspacesPage,
  clusters: ClustersPage,
  settings: SettingsPage,
}

export default defineComponent({
  name: 'AppLayout',
  setup() {
    const currentPage = inject('currentPage')
    const navigate = inject('navigate')
    const clock = ref('')
    let clockTimer = null

    function updateClock() {
      const now = new Date()
      clock.value = now.toLocaleTimeString('en-US', { hour12: false, hour: '2-digit', minute: '2-digit' })
    }

    onMounted(() => {
      updateClock()
      clockTimer = setInterval(updateClock, 1000)
      nextTick(() => lucide.createIcons())
    })

    onUnmounted(() => {
      if (clockTimer) clearInterval(clockTimer)
    })

    return () => {
      const page = currentPage.value

      return h('div', [
        // Ambient effects
        h('div', { class: 'ambient ambient--1' }),
        h('div', { class: 'ambient ambient--2' }),

        // Sidebar (Desktop)
        h('nav', { class: 'sidebar' }, [
          h('div', { class: 'sidebar__brand' }, [
            h('i', { 'data-lucide': 'code-2', style: 'width:20px;height:20px;color:var(--accent);flex-shrink:0' }),
            h('span', { class: 'sidebar__brand-text' }, 'DevZone'),
            h('div', { class: 'sidebar__dot' }),
          ]),
          h('div', { class: 'sidebar__nav' },
            NAV_ITEMS.map(item =>
              h('a', {
                class: `sidebar__item${page === item.key ? ' sidebar__item--active' : ''}`,
                onClick: () => navigate(item.key),
              }, [
                h('i', { 'data-lucide': item.icon, class: 'sidebar__item-icon' }),
                h('span', { class: 'sidebar__item-label' }, item.label),
              ])
            )
          ),
          h('div', { class: 'sidebar__footer' }, [
            h('div', { class: 'sidebar__clock' }, clock.value),
          ]),
        ]),

        // Bottom nav (Mobile)
        h('nav', { class: 'bottomnav' },
          NAV_ITEMS.map(item =>
            h('a', {
              class: `bottomnav__item${page === item.key ? ' bottomnav__item--active' : ''}`,
              onClick: () => navigate(item.key),
            }, [
              h('i', { 'data-lucide': item.icon, style: 'width:20px;height:20px' }),
              h('span', { class: 'bottomnav__label' }, item.label),
            ])
          )
        ),

        // Main content
        h('main', { class: 'main' }, [
          h('div', { class: 'main__content' }, [
            h('div', { class: 'page page--active', key: page }, [
              h(PAGE_COMPONENTS[page] || PAGE_COMPONENTS.workspaces),
            ]),
          ]),
        ]),

        // Toast
        h(AppToast),

        // Iframe Panel
        h(IframePanel),
      ])
    }
  },
})
