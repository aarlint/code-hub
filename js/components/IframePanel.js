import { defineComponent, h, Teleport, watch, onMounted, onUnmounted, nextTick, inject, ref } from 'vue'

export default defineComponent({
  name: 'IframePanel',
  setup() {
    const {
      tabs,
      activeTabId,
      expanded,
      panelHeight,
      hasAnyTabs,
      activeTab,
      closeTab,
      selectTab,
      minimize,
      openInNewTab,
    } = inject('iframePanel')

    const dragging = ref(false)

    function onKeydown(e) {
      if (e.key === 'Escape' && expanded.value) {
        minimize()
      }
    }

    // --- Drag resize ---
    let dragStartY = 0
    let dragStartHeight = 0

    function onDragStart(e) {
      // Support both mouse and touch
      const clientY = e.touches ? e.touches[0].clientY : e.clientY
      dragStartY = clientY
      dragStartHeight = panelHeight.value
      dragging.value = true

      if (e.touches) {
        document.addEventListener('touchmove', onDragMove, { passive: false })
        document.addEventListener('touchend', onDragEnd)
      } else {
        document.addEventListener('mousemove', onDragMove)
        document.addEventListener('mouseup', onDragEnd)
      }
      e.preventDefault()
    }

    function onDragMove(e) {
      const clientY = e.touches ? e.touches[0].clientY : e.clientY
      const deltaY = clientY - dragStartY
      const deltaPct = (deltaY / window.innerHeight) * 100
      const newHeight = Math.max(15, Math.min(95, dragStartHeight - deltaPct))
      panelHeight.value = newHeight
      if (e.touches) e.preventDefault()
    }

    function onDragEnd() {
      dragging.value = false
      document.removeEventListener('mousemove', onDragMove)
      document.removeEventListener('mouseup', onDragEnd)
      document.removeEventListener('touchmove', onDragMove)
      document.removeEventListener('touchend', onDragEnd)
      // Snap to minimize if dragged below threshold
      if (panelHeight.value < 20) {
        panelHeight.value = 85
        minimize()
      }
    }

    onMounted(() => {
      document.addEventListener('keydown', onKeydown)
    })

    onUnmounted(() => {
      document.removeEventListener('keydown', onKeydown)
    })

    watch([hasAnyTabs, expanded, activeTabId], () => {
      nextTick(() => lucide.createIcons())
    })

    function renderTabBar() {
      return h('div', { class: 'iframe-tab-bar' }, [
        h('div', { class: 'iframe-tab-bar__tabs' },
          tabs.value.map(tab =>
            h('div', {
              class: ['iframe-tab', tab.id === activeTabId.value && expanded.value ? 'iframe-tab--active' : ''],
              key: tab.id,
              onClick: (e) => {
                e.stopPropagation()
                selectTab(tab.id)
              },
            }, [
              h('span', { class: 'iframe-tab__title' }, tab.title),
              h('button', {
                class: 'iframe-tab__close',
                onClick: (e) => {
                  e.stopPropagation()
                  closeTab(tab.id)
                },
                innerHTML: '<i data-lucide="x" style="width:12px;height:12px"></i>',
              }),
            ])
          )
        ),
        h('div', { class: 'iframe-tab-bar__actions' }, [
          expanded.value
            ? h('button', {
                class: 'iframe-tab-bar__btn',
                onClick: minimize,
                title: 'Minimize',
                innerHTML: '<i data-lucide="chevron-down" style="width:16px;height:16px"></i>',
              })
            : h('button', {
                class: 'iframe-tab-bar__btn',
                onClick: () => {
                  if (activeTabId.value) expanded.value = true
                },
                title: 'Expand',
                innerHTML: '<i data-lucide="chevron-up" style="width:16px;height:16px"></i>',
              }),
        ]),
      ])
    }

    function renderPanelTabChips() {
      return h('div', { class: 'iframe-panel__tabs' },
        tabs.value.map(tab =>
          h('div', {
            class: ['iframe-panel__tab', tab.id === activeTabId.value ? 'iframe-panel__tab--active' : ''],
            key: tab.id,
            onClick: () => { activeTabId.value = tab.id },
          }, [
            h('span', { class: 'iframe-panel__tab-title' }, tab.title),
            h('button', {
              class: 'iframe-panel__tab-close',
              onClick: (e) => {
                e.stopPropagation()
                closeTab(tab.id)
              },
              innerHTML: '<i data-lucide="x" style="width:11px;height:11px"></i>',
            }),
          ])
        )
      )
    }

    function renderExpandedPanel() {
      if (!expanded.value || !activeTab.value) return null

      const panelStyle = {
        height: panelHeight.value + 'vh',
        // Disable slide-up animation while dragging
        animation: dragging.value ? 'none' : undefined,
      }

      return [
        // Overlay
        h('div', {
          class: 'iframe-panel-overlay',
          onClick: minimize,
        }),
        // Panel
        h('div', { class: 'iframe-panel', style: panelStyle }, [
          // Drag handle
          h('div', {
            class: 'iframe-panel__drag-handle',
            onMousedown: onDragStart,
            onTouchstart: onDragStart,
          }, [
            h('div', { class: 'iframe-panel__drag-bar' }),
          ]),
          // Header with inline tabs
          h('div', { class: 'iframe-panel__header' }, [
            renderPanelTabChips(),
            h('div', { class: 'iframe-panel__actions' }, [
              h('button', {
                class: 'btn',
                onClick: openInNewTab,
                innerHTML: '<i data-lucide="external-link" style="width:14px;height:14px"></i> New Tab',
              }),
              h('button', {
                class: 'iframe-panel__close',
                onClick: minimize,
                title: 'Minimize',
                innerHTML: '<i data-lucide="minus" style="width:18px;height:18px"></i>',
              }),
              h('button', {
                class: 'iframe-panel__close',
                onClick: () => closeTab(activeTab.value.id),
                title: 'Close tab',
                innerHTML: '<i data-lucide="x" style="width:18px;height:18px"></i>',
              }),
            ]),
          ]),
          // Iframes — all tabs rendered, only active visible
          h('div', { class: 'iframe-panel__frames' }, [
            // Pointer shield while dragging (prevents iframe from stealing events)
            dragging.value
              ? h('div', { class: 'iframe-panel__drag-shield' })
              : null,
            ...tabs.value.map(tab =>
              h('iframe', {
                key: tab.id,
                class: 'iframe-panel__frame',
                src: tab.url,
                allow: 'clipboard-read; clipboard-write',
                style: {
                  display: tab.id === activeTabId.value ? 'block' : 'none',
                },
              })
            ),
          ]),
        ]),
      ]
    }

    return () => {
      if (!hasAnyTabs.value) return null

      return h(Teleport, { to: 'body' }, [
        renderTabBar(),
        ...( expanded.value && activeTab.value ? renderExpandedPanel() : []),
      ])
    }
  },
})
